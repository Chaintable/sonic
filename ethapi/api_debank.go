package ethapi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/0xsoniclabs/sonic/evmcore"
	"github.com/0xsoniclabs/sonic/gossip/evmstore"
	"github.com/0xsoniclabs/sonic/inter"
	"github.com/0xsoniclabs/sonic/opera"
	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/Fantom-foundation/lachesis-base/inter/idx"
	"github.com/ethereum/go-ethereum/common"
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

type debankHeaderReader struct {
	ctx context.Context
	b   Backend
}

func (r debankHeaderReader) GetHeader(hash common.Hash, number uint64) *evmcore.EvmHeader {
	if hash != (common.Hash{}) {
		header, err := r.b.HeaderByHash(r.ctx, hash)
		if err == nil && header != nil {
			return header
		}
	}

	header, err := r.b.HeaderByNumber(r.ctx, rpc.BlockNumber(number))
	if err != nil {
		log.Warn("Failed to read header for Debank replay", "number", number, "hash", hash, "err", err)
		return nil
	}
	if hash != (common.Hash{}) && header != nil && header.Hash != hash {
		return nil
	}
	return header
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
	if block == nil {
		return nil, fmt.Errorf("block %v not found", blockNrOrHash)
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
		blockDiff.Hash = evmBlockHeader.Root
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
	if parent == nil {
		return nil, fmt.Errorf("parent block %d not found", block.NumberU64()-1)
	}

	pStateDB, _, err := api.b.StateAndHeaderByNumberOrHash(ctx, rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(parent.NumberU64())))
	if err != nil {
		return nil, err
	}
	if pStateDB == nil {
		return nil, fmt.Errorf("state for parent block %d not found", parent.NumberU64())
	}
	defer pStateDB.Release()

	rules, err := api.b.GetNetworkRules(ctx, idx.Block(block.NumberU64()))
	if err != nil {
		return nil, fmt.Errorf("failed to get network rules: %w", err)
	}
	if rules == nil {
		return nil, fmt.Errorf("no network rules found for block height %d", block.NumberU64())
	}
	vmConfig := opera.GetVmConfig(*rules)
	vmConfig.NoBaseFee = true

	rpcTracer := ptracer.RPCTracer{}
	vmConfig.Tracer = newDebankTraceHooks(&rpcTracer)

	diffStateDB := newDebankStateDiffDB(pStateDB)
	pStateDB = evmstore.WrapStateDbWithLogger(diffStateDB, vmConfig.Tracer)
	pStateDB.BeginBlock(block.NumberU64())

	blockIdx := idx.Block(block.NumberU64())
	chainConfig := api.b.ChainConfig(blockIdx)
	rpcTracer.OnBlockStart(evmBlock)

	replayRules := *rules
	replayRules.Upgrades.GasSubsidies = false
	replayHeader := block.EvmHeader
	replayBlock := &evmcore.EvmBlock{
		EvmHeader:    replayHeader,
		Transactions: block.Transactions,
	}
	var usedGas uint64
	processed := evmcore.NewStateProcessor(
		chainConfig,
		debankHeaderReader{ctx: ctx, b: api.b},
		replayRules.Upgrades,
	).Process(replayBlock, pStateDB, vmConfig, block.GasLimit, &usedGas, nil)
	if err := validateDebankReplayReceipts(block, processed, evmBlockHeader.ReceiptHash, evmBlockHeader.Bloom); err != nil {
		return nil, err
	}

	diffStateDB.SetExpectedReplayRoot(block.Root)
	pStateDB.EndBlock(block.NumberU64())
	replayedRoot := pStateDB.GetStateHash()
	if replayedRoot != block.Root {
		return nil, fmt.Errorf("replayed state root mismatch for block %d: got %s, want %s (txs=%d processed=%d usedGas=%d blockGasUsed=%d)", block.NumberU64(), replayedRoot, block.Root, len(block.Transactions), len(processed), usedGas, block.GasUsed)
	}
	if usedGas != block.GasUsed {
		return nil, fmt.Errorf("replayed gas used mismatch for block %d: got %d, want %d (txs=%d processed=%d)", block.NumberU64(), usedGas, block.GasUsed, len(block.Transactions), len(processed))
	}

	destructs, accounts, storages, codes := diffStateDB.StateUpdateMaps()
	res := rpcTracer.GetOutPut(parent.Root, block.Root, destructs, accounts, storages, codes)
	if err := validateDebankBlockFileTxs(block, res.BlockFile.Txs); err != nil {
		return nil, err
	}
	sort.Strings(res.BlockFile.StorageContracts)
	res.ValidationHash = res.BlockFile.Validation().ValidationHash

	return res, nil
}

func validateDebankReplayReceipts(block *evmcore.EvmBlock, processed []evmcore.ProcessedTransaction, receiptHash common.Hash, bloom types.Bloom) error {
	if len(processed) != len(block.Transactions) {
		return fmt.Errorf("replayed tx count mismatch for block %d: got %d, want %d", block.NumberU64(), len(processed), len(block.Transactions))
	}
	replayReceipts := make(types.Receipts, 0, len(processed))
	for i, processedTx := range processed {
		if processedTx.Transaction == nil {
			return fmt.Errorf("replayed tx %d in block %d has nil transaction", i, block.NumberU64())
		}
		if processedTx.Transaction.Hash() != block.Transactions[i].Hash() {
			return fmt.Errorf("replayed tx %d mismatch for block %d: got %s, want %s", i, block.NumberU64(), processedTx.Transaction.Hash(), block.Transactions[i].Hash())
		}
		if processedTx.Receipt == nil {
			return fmt.Errorf("could not replay tx %d [%v] in block %d", i, processedTx.Transaction.Hash().Hex(), block.NumberU64())
		}
		replayReceipts = append(replayReceipts, processedTx.Receipt)
	}
	replayedReceiptHash := types.DeriveSha(replayReceipts, trie.NewStackTrie(nil))
	if replayedReceiptHash != receiptHash {
		return fmt.Errorf("replayed receipt root mismatch for block %d: got %s, want %s (txs=%d)", block.NumberU64(), replayedReceiptHash, receiptHash, len(block.Transactions))
	}
	replayedBloom := types.MergeBloom(replayReceipts)
	if replayedBloom != bloom {
		return fmt.Errorf("replayed logs bloom mismatch for block %d", block.NumberU64())
	}
	return nil
}

func validateDebankBlockFileTxs(block *evmcore.EvmBlock, txs []ptypes.Transaction) error {
	if len(txs) != len(block.Transactions) {
		return fmt.Errorf("block_file tx count mismatch for block %d: got %d, want %d", block.NumberU64(), len(txs), len(block.Transactions))
	}
	for i, tx := range block.Transactions {
		if txs[i].ID != tx.Hash().Hex() {
			return fmt.Errorf("block_file tx %d id mismatch for block %d: got %s, want %s", i, block.NumberU64(), txs[i].ID, tx.Hash().Hex())
		}
	}
	return nil
}
