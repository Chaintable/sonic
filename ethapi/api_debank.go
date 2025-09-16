package ethapi

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/Fantom-foundation/go-opera/evmcore"
	"github.com/Fantom-foundation/go-opera/inter"
	"github.com/Fantom-foundation/go-opera/opera"
	"github.com/Fantom-foundation/go-opera/utils/signers/internaltx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
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
	fullBlock, err := RPCMarshalBlock(block, receipts, true, true)
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
		if blockDiff != nil {
			stateDiffBytes, err = util.EncodeToRlp(blockDiff)
			if err != nil {
				log.Error("Failed to encode state diff", "err", err)
				stateDiffBytes = []byte{}
			}
		} else {
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

	vmConfig := opera.DefaultVMConfig

	rpcTracer := ptracer.RPCTracer{}
	vmConfig.Tracer = &tracing.Hooks{
		OnTxStart: rpcTracer.OnTxStart,
		OnTxEnd:   rpcTracer.OnTxEnd,
		OnEnter:   rpcTracer.OnEnter,
		OnExit:    rpcTracer.OnExit,
		OnOpcode:  rpcTracer.OnOpcode,
		OnLog:     rpcTracer.OnLog,
	}

	rpcTracer.OnBlockStart(evmBlock)

	var (
		txs     = block.Transactions
		signer  = types.MakeSigner(api.b.ChainConfig(), block.Number, uint64(block.Time.Unix()))
		gp      = new(core.GasPool).AddGas(block.GasLimit)
		usedGas = new(uint64)
	)

	blockCtx := evmcore.NewEVMBlockContext(block.Header(), api.b.GetEvmStateReader(), nil)
	evm := vm.NewEVM(blockCtx, vm.TxContext{}, pStateDB, api.b.ChainConfig(), vmConfig)

	for i, tx := range txs {
		msg, err := TxAsMessage(tx, signer, block.BaseFee)
		if err != nil {
			return nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, tx.Hash().Hex(), err)
		}

		pStateDB.SetTxContext(tx.Hash(), i)

		receipt, err := evmcore.ApplyTransactionWithEVM(msg, api.b.ChainConfig(), gp, pStateDB, evmBlock.Number(), evmBlock.Hash(), tx, usedGas, evm)
		if err != nil {
			return nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, tx.Hash().Hex(), err)
		}

		receipt.SetEffectiveGasPrice(tx, blockCtx.BaseFee)
	}

	res := rpcTracer.GetOutPut(parent.Root, block.Root)

	return res, nil
}

func TxAsMessage(tx *types.Transaction, signer types.Signer, baseFee *big.Int) (*core.Message, error) {
	if !internaltx.IsInternal(tx) {
		return core.TransactionToMessage(tx, signer, baseFee)
	} else {
		return &core.Message{ // internal tx - no signature checking
			From:              internaltx.InternalSender(tx),
			To:                tx.To(),
			Nonce:             tx.Nonce(),
			Value:             tx.Value(),
			GasLimit:          tx.Gas(),
			GasPrice:          tx.GasPrice(),
			GasFeeCap:         tx.GasFeeCap(),
			GasTipCap:         tx.GasTipCap(),
			Data:              tx.Data(),
			AccessList:        tx.AccessList(),
			BlobGasFeeCap:     tx.BlobGasFeeCap(),
			BlobHashes:        tx.BlobHashes(),
			SkipAccountChecks: true, // don't check sender nonce and being EOA
		}, nil
	}
}
