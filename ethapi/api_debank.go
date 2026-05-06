package ethapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0xsoniclabs/sonic/evmcore"
	"github.com/0xsoniclabs/sonic/gossip/evmstore"
	"github.com/0xsoniclabs/sonic/inter"
	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/Fantom-foundation/lachesis-base/inter/idx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
)

type DebankAPI struct {
	b Backend
}

func NewDebankAPI(b Backend) *DebankAPI {
	return &DebankAPI{
		b: b,
	}
}

func (api *DebankAPI) getBlockReceipts(ctx context.Context, blkNumber rpc.BlockNumber) (types.Receipts, error) {
	if blkNumber == rpc.EarliestBlockNumber {
		return types.Receipts{}, nil
	}
	return api.b.GetReceiptsByNumber(ctx, blkNumber)
}

func (api *DebankAPI) DebankBlock(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*ptypes.DebankOutPut, error) {
	log.Info("trace DebankBlock number", "number", blockNrOrHash)
	var block *evmcore.EvmBlock
	var err error
	if blockNrOrHash.BlockHash != nil {
		block, err = api.b.BlockByHash(ctx, *blockNrOrHash.BlockHash)
	} else if blockNrOrHash.BlockNumber != nil {
		block, err = api.b.BlockByNumber(ctx, *blockNrOrHash.BlockNumber)
	} else {
		log.Error("Either block number or block hash must be provided")
		return nil, fmt.Errorf("either block number or block hash must be provided")
	}

	if err != nil {
		log.Error("Failed to get block", "err", err, "blockNrOrHash", blockNrOrHash)
		return nil, err
	}

	receipts, err := api.getBlockReceipts(ctx, rpc.BlockNumber(block.NumberU64()))
	if err != nil {
		return nil, err
	}
	fullBlock, err := RPCMarshalBlock(block, receipts, true, true, api.b.ChainID())
	if err != nil {
		return nil, err
	}

	evmBlockHeader := &types.Header{
		ParentHash:  fullBlock.ParentHash,
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.Address{}, // < in Sonic, the coinbase is always 0
		Root:        fullBlock.Root,
		TxHash:      fullBlock.TxHash,
		ReceiptHash: fullBlock.ReceiptHash,
		Bloom:       fullBlock.Bloom,
		Difficulty:  fullBlock.Difficulty.ToInt(),
		Number:      fullBlock.Number.ToInt(),
		GasLimit:    uint64(fullBlock.GasLimit),
		GasUsed:     uint64(fullBlock.GasUsed),
		Time:        uint64(fullBlock.Time),
		Extra: inter.EncodeExtraData(
			block.Time.Time(),
			block.Duration*time.Nanosecond,
		),
		MixDigest: fullBlock.PrevRandao,
		Nonce:     types.BlockNonce{}, // constant 0 in Ethereum
		BaseFee:   fullBlock.BaseFee.ToInt(),

		// Sonic does not have a beacon chain and no withdrawals.
		WithdrawalsHash: &types.EmptyWithdrawalsHash,

		// Sonic does not support blobs, so no blob gas is used and there is
		// no excess blob gas.
		BlobGasUsed:   new(uint64), // = 0
		ExcessBlobGas: new(uint64), // = 0
	}
	evmBlock := block.EthBlock()
	evmBlock = types.NewBlockForPipelineTrace(evmBlockHeader, &types.Body{Transactions: evmBlock.Transactions()}, nil, trie.NewStackTrie(nil))

	if block.NumberU64() == 0 {
		header := util.BuildPilelineBlockHeader(evmBlock)
		blockDiff := ptracer.GenesisAllocToStateDiff(evmcore.GenesisAlloc)
		blockDiff.Hash = evmBlockHeader.Hash()
		blockDiff.ParentHash = types.EmptyRootHash
		blockFile := &ptypes.BlockFile{
			Block:            util.BuildPipelineBlock(evmBlock),
			Txs:              make([]ptypes.Transaction, 0),
			Events:           make([]ptypes.Event, 0),
			Traces:           make([]ptypes.Trace, 0),
			ErrorEvents:      make([]ptypes.Event, 0),
			ErrorTraces:      make([]ptypes.Trace, 0),
			StorageContracts: make([]string, 0),
		}
		for addr, account := range evmcore.GenesisAlloc {
			if len(account.Storage) > 0 {
				blockFile.StorageContracts = append(blockFile.StorageContracts, strings.ToLower(addr.Hex()))
			}
		}
		var stateDiffBytes []byte
		stateDiffBytes, err = util.EncodeToRlp(blockDiff)
		if err != nil {
			log.Error("Failed to encode state diff", "err", err)
			stateDiffBytes = []byte{}
		}

		return &ptypes.DebankOutPut{
			BlockFile:      blockFile,
			Header:         header,
			StateDiff:      stateDiffBytes,
			ValidationHash: blockFile.Validation().ValidationHash,
		}, nil
	}
	// Prepare base state
	parent, err := api.b.BlockByNumber(ctx, rpc.BlockNumber(block.NumberU64()-1))
	if err != nil {
		return nil, err
	}

	pStateDB, _, err := api.b.StateAndHeaderByNumberOrHash(ctx, rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(parent.NumberU64())))
	if pStateDB == nil || err != nil {
		return nil, err
	}
	defer pStateDB.Release()

	vmConfig, err := GetVmConfig(ctx, api.b, idx.Block(block.NumberU64()))
	if err != nil {
		return nil, fmt.Errorf("failed to get vm config: %w", err)
	}
	vmConfig.NoBaseFee = true

	rpcTracer := ptracer.RPCTracer{}
	vmConfig.Tracer = newDebankTraceHooks(&rpcTracer)

	diffStateDB := newDebankStateDiffDB(pStateDB)
	pStateDB = evmstore.WrapStateDbWithLogger(diffStateDB, vmConfig.Tracer)
	pStateDB.BeginBlock(block.NumberU64())

	rpcTracer.OnBlockStart(evmBlock, api.b.ChainConfig(idx.Block(block.NumberU64())))

	var (
		txs     = block.Transactions
		signer  = types.MakeSigner(api.b.ChainConfig(idx.Block(block.NumberU64())), block.Number, uint64(block.Time.Unix()))
		gp      = new(core.GasPool).AddGas(block.GasLimit)
		usedGas = new(uint64)
	)

	evm, _, err := api.b.GetEVM(ctx, pStateDB, block.Header(), &vmConfig, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get EVM for tracing: %w", err)
	}

	if api.b.ChainConfig(idx.Block(block.NumberU64())).IsPrague(block.Number, uint64(block.Time.Unix())) {
		evmcore.ProcessParentBlockHash(block.ParentHash, evm, pStateDB)
	}

	// log.Info("trace DebankBlock info", "txs", len(txs), "block", block.NumberU64(), "hash", block.Hash.Hex(), "vmConfig", vmConfig)
	for i, tx := range txs {
		msg, err := evmcore.TxAsMessage(tx, signer, block.BaseFee)
		if err != nil {
			return nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, tx.Hash().Hex(), err)
		}

		pStateDB.SetTxContext(tx.Hash(), i)

		_, err = evmcore.ApplyTransactionWithEVM(msg, api.b.ChainConfig(idx.Block(block.NumberU64())), gp, pStateDB, evmBlock.Number(), evmBlock.Hash(), tx, usedGas, evm)
		if err != nil {
			return nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, tx.Hash().Hex(), err)
		}
	}

	pStateDB.EndBlock(block.NumberU64())
	replayedRoot := pStateDB.GetStateHash()
	if replayedRoot != block.Root {
		return nil, fmt.Errorf("replayed state root mismatch for block %d: got %s, want %s", block.NumberU64(), replayedRoot, block.Root)
	}

	res := rpcTracer.GetOutPut(parent.Root, parent.Root)
	if parent.Root != block.Root {
		blockDiff := diffStateDB.BuildStateDiff(parent.Root, block.Root)
		stateDiffBytes, err := util.EncodeToRlp(blockDiff)
		if err != nil {
			log.Error("Failed to encode state diff", "err", err)
			stateDiffBytes = []byte{}
		}
		res.StateDiff = stateDiffBytes
	}
	res.BlockFile.StorageContracts = mergeStorageContracts(res.BlockFile.StorageContracts, diffStateDB.StorageContractAddresses())

	return res, nil
}

func mergeStorageContracts(base []string, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	merged := make([]string, 0, len(base)+len(extra))
	for _, addr := range base {
		key := strings.ToLower(addr)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	for _, addr := range extra {
		key := strings.ToLower(addr)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	return merged
}
