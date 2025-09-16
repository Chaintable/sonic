package ethapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Fantom-foundation/go-opera/evmcore"
	"github.com/Fantom-foundation/go-opera/inter"
	"github.com/Fantom-foundation/go-opera/opera"
	"github.com/Fantom-foundation/go-opera/txtrace"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
)

type PreExecTx struct {
	ChainId              *big.Int        `json:"chainId,omitempty"`
	From                 *common.Address `json:"from"`
	To                   *common.Address `json:"to"`
	Gas                  *hexutil.Uint64 `json:"gas"`
	GasPrice             *hexutil.Big    `json:"gasPrice"`
	MaxFeePerGas         *hexutil.Big    `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *hexutil.Big    `json:"maxPriorityFeePerGas"`
	Value                *hexutil.Big    `json:"value"`
	Nonce                *hexutil.Uint64 `json:"nonce"`
	Data                 *hexutil.Bytes  `json:"data"`
	Input                *hexutil.Bytes  `json:"input"`
}

// PreExecAPI provides pre exec info for rpc
type PreExecAPI struct {
	b Backend
}

func NewPreExecAPI(b Backend) *PreExecAPI {
	return &PreExecAPI{b}
}

const (
	UnKnown            = 1000
	InsufficientBalane = 1001
	Reverted           = 1002
)

type PreError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type PreResult struct {
	Trace   *[]txtrace.ActionTrace `json:"trace"`
	Logs    []*types.Log           `json:"logs"`
	Error   PreError               `json:"error,omitempty"`
	GasUsed uint64                 `json:"gasUsed"`
}

func toPreError(err error) PreError {
	preErr := PreError{
		Code: UnKnown,
	}
	if err != nil {
		preErr.Msg = err.Error()
	}
	if strings.HasPrefix(preErr.Msg, "execution reverted") {
		preErr.Code = Reverted
	}
	if strings.HasPrefix(preErr.Msg, "out of gas") {
		preErr.Code = Reverted
	}
	if strings.HasPrefix(preErr.Msg, "insufficient") {
		preErr.Code = InsufficientBalane
	}
	return preErr
}

func (api *PreExecAPI) TraceMany(ctx context.Context, origins []PreExecTx) ([]PreResult, error) {
	preResList := make([]PreResult, 0)
	block, _ := api.b.BlockByNumber(ctx, rpc.LatestBlockNumber)
	state, header, err := api.b.StateAndHeaderByNumberOrHash(ctx, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	if state == nil || err != nil {
		return nil, err
	}
	for i := 0; i < len(origins); i++ {
		origin := origins[i]
		if origin.Nonce == nil {
			preResList = append(preResList, PreResult{
				Error: PreError{
					Code: UnKnown,
					Msg:  "nonce is nil",
				},
			})
			continue
		}
		if i > 0 && (uint64)(*origin.Nonce) <= (uint64)(*origins[i-1].Nonce) {
			preResList = append(preResList, PreResult{
				Error: PreError{
					Code: UnKnown,
					Msg:  fmt.Sprintf("nonce decreases, tx index %d has nonce %d, tx index %d has nonce %d", i-1, (uint64)(*origins[i-1].Nonce), i, (uint64)(*origin.Nonce)),
				},
			})
			continue
		}
		txArgs := TransactionArgs{
			From:                 origin.From,
			To:                   origin.To,
			Gas:                  origin.Gas,
			GasPrice:             origin.GasPrice,
			MaxFeePerGas:         origin.MaxFeePerGas,
			MaxPriorityFeePerGas: origin.MaxPriorityFeePerGas,
			Value:                origin.Value,
			Nonce:                origin.Nonce,
			Data:                 origin.Data,
			Input:                origin.Input,
		}
		// Get a new instance of the EVM.
		msg, err := txArgs.ToMessage(api.b.RPCGasCap(), header.BaseFee)
		if err != nil {
			preResList = append(preResList, PreResult{
				Error: PreError{
					Code: UnKnown,
					Msg:  err.Error(),
				},
			})
			continue
		}
		txHash := common.BigToHash(big.NewInt(int64(i)))
		// Providing default config with tracer
		vmConfig := opera.DefaultVMConfig
		txTracer := txtrace.NewTraceStructLogger(block, uint(i))
		vmConfig.Tracer = txTracer.Hooks()
		vmConfig.ChargeExcessGas = false
		vmConfig.NoBaseFee = true

		evm, _, err := api.b.GetEVM(ctx, msg, state, header, &vmConfig)
		if err != nil {
			preResList = append(preResList, PreResult{
				Error: PreError{
					Code: UnKnown,
					Msg:  err.Error(),
				},
			})
			continue
		}
		// Execute the message.
		gp := new(core.GasPool).AddGas(msg.GasLimit)
		state.SetTxContext(txHash, int(i))

		var usedGas uint64
		result, err := evmcore.ApplyTransactionWithEVM(msg, api.b.ChainConfig(), gp, state, header.Number, block.Hash, txArgs.toTransaction(), &usedGas, evm)
		//result, err := core.ApplyMessage(evm, msg, gp)
		if err != nil {
			preRes := PreResult{
				Error: toPreError(err),
			}
			if result != nil {
				preRes.GasUsed = result.GasUsed
			}
			preResList = append(preResList, preRes)
			continue
		}
		traceActions := txTracer.GetResult()

		preRes := PreResult{
			Trace:   traceActions,
			Logs:    state.GetLogs(txHash, block.Hash),
			GasUsed: result.GasUsed,
		}

		if preRes.Error.Msg == "" && len(*preRes.Trace) > 0 && (*preRes.Trace)[0].Error != "" {
			preRes.Error = PreError{
				Code: Reverted,
				Msg:  (*preRes.Trace)[0].Error,
			}
		}
		preResList = append(preResList, preRes)
	}
	return preResList, nil
}

func (api *PreExecAPI) TracePipelineBlock(ctx context.Context, number rpc.BlockNumber, TracerConfig json.RawMessage) ([]PreResult, error) {
	log.Info("param number", "number", number, "TracerConfig", string(TracerConfig))
	block, err := api.b.BlockByNumber(ctx, number)
	if err != nil || block == nil {
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

	preResList := make([]PreResult, 0)

	evmBlock := block.EthBlock()

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

	log.Info("param fullBlock", "evmBlock", evmBlock)
	pipelineTracer, err := tracer.NewPipelineTracer(TracerConfig)
	if err != nil {
		log.Error("Failed to create pipeline tracer", "err", err)
	}

	if number == rpc.EarliestBlockNumber {
		pipelineTracer.OnGenesisBlock(types.NewBlockForPipelineTrace(evmBlockHeader, &types.Body{Transactions: evmBlock.Transactions()}, nil, trie.NewStackTrie(nil)), evmcore.GenesisAlloc)
		return preResList, nil
	}

	pBlock, err := api.b.BlockByNumber(ctx, rpc.BlockNumber(block.NumberU64()-1))
	if err != nil {
		return nil, err
	}

	state, header, err := api.b.StateAndHeaderByNumberOrHash(ctx, rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(pBlock.NumberU64())))
	if state == nil || err != nil {
		return nil, err
	}

	vmConfig := opera.DefaultVMConfig
	vmConfig.Tracer = &tracing.Hooks{
		// VM events
		OnTxStart: pipelineTracer.OnTxStart,
		OnTxEnd:   pipelineTracer.OnTxEnd,
		OnEnter:   pipelineTracer.OnEnter,
		OnExit:    pipelineTracer.OnExit,
		OnOpcode:  pipelineTracer.OnOpcode,
		// Chain events
		OnClose:      pipelineTracer.OnClose,
		OnBlockStart: pipelineTracer.OnBlockStart,
		OnBlockEnd:   pipelineTracer.OnBlockEnd,
		// State events
		OnLog: pipelineTracer.OnLog,
		// custom hook
		OnCommit: pipelineTracer.OnCommit,
	}
	vmConfig.ChargeExcessGas = false
	vmConfig.NoBaseFee = true

	pipelineTracer.OnBlockStart(tracing.BlockEvent{
		Block: types.NewBlockForPipelineTrace(evmBlockHeader, &types.Body{Transactions: evmBlock.Transactions()}, nil, trie.NewStackTrie(nil)),
	})

	txs := block.Transactions
	signer := types.MakeSigner(api.b.ChainConfig(), block.Number, uint64(block.Time.Unix()))

	for i := 0; i < len(txs); i++ {
		tx := txs[i]
		msg, _ := core.TransactionToMessage(tx, signer, block.BaseFee)

		txHash := common.BigToHash(big.NewInt(int64(i)))

		evm, _, err := api.b.GetEVM(ctx, msg, state, header, &vmConfig)
		if err != nil {
			preResList = append(preResList, PreResult{
				Error: PreError{
					Code: UnKnown,
					Msg:  err.Error(),
				},
			})
			continue
		}
		// Execute the message.
		gp := new(core.GasPool).AddGas(msg.GasLimit)
		state.SetTxContext(txHash, int(i))

		var usedGas uint64
		result, err := evmcore.ApplyTransactionWithEVM(msg, api.b.ChainConfig(), gp, state, header.Number, block.Hash, tx, &usedGas, evm)
		//result, err := core.ApplyMessage(evm, msg, gp)
		if err != nil {
			preRes := PreResult{
				Error: toPreError(err),
			}
			if result != nil {
				preRes.GasUsed = result.GasUsed
			}
			preResList = append(preResList, preRes)
			continue
		}

		logs := state.GetLogs(txHash, block.Hash)

		preRes := PreResult{
			Logs:    logs,
			GasUsed: result.GasUsed,
		}

		if preRes.Error.Msg == "" && preRes.Trace != nil && len(*preRes.Trace) > 0 && (*preRes.Trace)[0].Error != "" {
			preRes.Error = PreError{
				Code: Reverted,
				Msg:  (*preRes.Trace)[0].Error,
			}
		}
		preResList = append(preResList, preRes)
	}

	pipelineTracer.OnCommit(pBlock.Root, block.Root, nil, nil, nil, nil, nil, nil)
	pipelineTracer.OnBlockEnd(nil)

	blockChange := &ptypes.BlockChangeNotification{
		ChangeType: 1, // 1 for new, 2 for fork
		NewBlocks: []ptypes.BlockContext{
			{
				Hash:        block.Hash,
				ParentHash:  pBlock.Hash,
				BlockNumber: block.NumberU64(),
				Timestamp:   uint64(block.Time.Unix()),
			},
		},
	}

	start := time.Now()
	err = tracer.NodeXPusher.PushBlockChangeNotification(blockChange)
	if err == nil {
		log.Info("Push kafka", "dropBlocks", blockChange.DropBlocks, "newBlocks", blockChange.NewBlocks, "kafka elapsed", common.PrettyDuration(time.Since(start)))
	} else {
		log.Error("Failed to push kafka", "err", err, "dropBlocks", blockChange.DropBlocks, "newBlocks", blockChange.NewBlocks)
	}
	return preResList, nil
}

func (s *PreExecAPI) getBlockReceipts(ctx context.Context, blkNumber rpc.BlockNumber) (types.Receipts, error) {
	if blkNumber == rpc.EarliestBlockNumber {
		return types.Receipts{}, nil
	}
	return s.b.GetReceiptsByNumber(ctx, blkNumber)
}
