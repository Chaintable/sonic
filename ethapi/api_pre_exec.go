package ethapi

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/Fantom-foundation/go-opera/evmcore"
	"github.com/Fantom-foundation/go-opera/opera"
	"github.com/Fantom-foundation/go-opera/txtrace"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
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

func toPreError(err error, result *evmcore.ExecutionResult) PreError {
	preErr := PreError{
		Code: UnKnown,
	}
	if err != nil {
		preErr.Msg = err.Error()
	}
	if result != nil && result.Err != nil {
		preErr.Msg = result.Err.Error()
	}
	if strings.HasPrefix(preErr.Msg, "execution reverted") {
		preErr.Code = Reverted
		if result != nil {
			preErr.Msg, _ = abi.UnpackRevert(result.Revert())
		}
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
		//msg, err := evmcore.TxAsMessage(txArgs, signer, block.BaseFee)
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
		vmConfig := opera.DefaultVMConfig
		vmConfig.NoBaseFee = true
		vmConfig.Debug = true
		txTracer := txtrace.NewTraceStructLogger2(block, txHash, msg, uint(i), msg.Gas(), msg.Gas())
		vmConfig.Tracer = txTracer

		evm, vmError, err := api.b.GetEVM(ctx, msg, state, header, &vmConfig)
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
		gp := new(evmcore.GasPool).AddGas(math.MaxUint64)
		state.Prepare(txHash, i)
		result, err := evmcore.ApplyMessage(evm, msg, gp)
		if err := vmError(); err != nil {
			preRes := PreResult{
				Error: toPreError(err, result),
			}
			if result != nil {
				preRes.GasUsed = result.MaxUsedGas
			}
			preResList = append(preResList, preRes)
			continue
		}
		if err != nil {
			preRes := PreResult{
				Error: toPreError(err, result),
			}
			if result != nil {
				preRes.GasUsed = result.MaxUsedGas
			}
			preResList = append(preResList, preRes)
			continue
		}
		traceActions := txTracer.GetResult()

		preRes := PreResult{
			Trace: traceActions,
			Logs:  state.GetLogs(txHash, header.Hash),
		}
		if result != nil {
			preRes.GasUsed = result.MaxUsedGas
			if result.Failed() {
				preRes.Error = toPreError(err, result)
			}
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
