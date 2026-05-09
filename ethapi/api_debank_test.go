package ethapi

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/0xsoniclabs/sonic/evmcore"
	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/stretchr/testify/require"
)

func TestValidateDebankReplayReceiptsRejectsReceiptRootMismatch(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21_000, GasPrice: big.NewInt(1)})
	receipt := &types.Receipt{
		TxHash:            tx.Hash(),
		Status:            types.ReceiptStatusSuccessful,
		GasUsed:           21_000,
		CumulativeGasUsed: 21_000,
	}
	block := &evmcore.EvmBlock{
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7)},
		Transactions: types.Transactions{
			tx,
		},
	}

	err := validateDebankReplayReceipts(block, []evmcore.ProcessedTransaction{{
		Transaction: tx,
		Receipt:     receipt,
	}}, common.HexToHash("0x01"), types.Bloom{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "replayed receipt root mismatch")
}

func TestValidateDebankReplayReceiptsRejectsBloomMismatch(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21_000, GasPrice: big.NewInt(1)})
	receipt := &types.Receipt{
		TxHash:            tx.Hash(),
		Status:            types.ReceiptStatusSuccessful,
		GasUsed:           21_000,
		CumulativeGasUsed: 21_000,
		Logs: []*types.Log{{
			Address: common.HexToAddress("0x1001"),
			Topics:  []common.Hash{common.HexToHash("0x01")},
		}},
	}
	receipt.Bloom = types.CreateBloom(receipt)
	block := &evmcore.EvmBlock{
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7)},
		Transactions: types.Transactions{
			tx,
		},
	}
	receiptHash := types.DeriveSha(types.Receipts{receipt}, trie.NewStackTrie(nil))

	err := validateDebankReplayReceipts(block, []evmcore.ProcessedTransaction{{
		Transaction: tx,
		Receipt:     receipt,
	}}, receiptHash, types.Bloom{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "replayed logs bloom mismatch")
}

func TestValidateDebankBlockFileTxsRejectsIDMismatch(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21_000, GasPrice: big.NewInt(1)})
	block := &evmcore.EvmBlock{
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7)},
		Transactions: types.Transactions{
			tx,
		},
	}

	err := validateDebankBlockFileTxs(block, []ptypes.Transaction{{ID: common.HexToHash("0x01").Hex()}})

	require.Error(t, err)
	require.Contains(t, err.Error(), "block_file tx 0 id mismatch")
}

func TestValidateDebankBlockFileRejectsEmptyEventMetadata(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21_000, GasPrice: big.NewInt(1)})
	block := &evmcore.EvmBlock{
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7)},
		Transactions: types.Transactions{
			tx,
		},
	}
	blockFile := &ptypes.BlockFile{
		Txs: []ptypes.Transaction{{ID: tx.Hash().Hex()}},
		Events: []ptypes.Event{{
			ID: "",
		}},
	}

	err := validateDebankBlockFile(block, blockFile)

	require.Error(t, err)
	require.Contains(t, err.Error(), "event 0 id is empty")
}

func TestDebankTraceGuardZeroesInternalTxEffectiveGasPrice(t *testing.T) {
	rpcTracer, guard := newTestDebankRPCTracer(t)
	hooks := guard.Hooks()
	to := common.HexToAddress("0x1001")
	tx := types.NewTx(&types.LegacyTx{
		To:       &to,
		Gas:      100_000,
		GasPrice: big.NewInt(0),
		V:        big.NewInt(0),
		R:        big.NewInt(0),
		S:        big.NewInt(1),
	})

	hooks.OnTxStart(testVMContext(), tx, common.Address{})
	hooks.OnEnter(0, byte(vm.CALL), common.Address{}, to, nil, 79_000, big.NewInt(0))
	hooks.OnExit(0, nil, 1, nil, false)
	hooks.OnTxEnd(&types.Receipt{
		TxHash:            tx.Hash(),
		Status:            types.ReceiptStatusSuccessful,
		GasUsed:           21_001,
		CumulativeGasUsed: 21_001,
		EffectiveGasPrice: big.NewInt(10),
	}, nil)

	out := rpcTracer.GetOutPut(common.Hash{}, common.Hash{}, nil, nil, nil, nil)

	require.Len(t, out.BlockFile.Txs, 1)
	require.Zero(t, out.BlockFile.Txs[0].GasPrice.Sign())
}

func TestDebankTraceGuardKeepsRegularTxEffectiveGasPrice(t *testing.T) {
	rpcTracer, guard := newTestDebankRPCTracer(t)
	hooks := guard.Hooks()
	to := common.HexToAddress("0x1001")
	tx := types.NewTx(&types.LegacyTx{
		To:       &to,
		Gas:      100_000,
		GasPrice: big.NewInt(1),
		V:        big.NewInt(27),
		R:        big.NewInt(1),
		S:        big.NewInt(1),
	})

	hooks.OnTxStart(testVMContext(), tx, common.Address{})
	hooks.OnEnter(0, byte(vm.CALL), common.Address{}, to, nil, 79_000, big.NewInt(0))
	hooks.OnExit(0, nil, 1, nil, false)
	hooks.OnTxEnd(&types.Receipt{
		TxHash:            tx.Hash(),
		Status:            types.ReceiptStatusSuccessful,
		GasUsed:           21_001,
		CumulativeGasUsed: 21_001,
		EffectiveGasPrice: big.NewInt(7),
	}, nil)

	out := rpcTracer.GetOutPut(common.Hash{}, common.Hash{}, nil, nil, nil, nil)

	require.Len(t, out.BlockFile.Txs, 1)
	require.Equal(t, int64(7), out.BlockFile.Txs[0].GasPrice.Int64())
}

func TestDebankTraceGuardBuffersLogsUntilTopCall(t *testing.T) {
	rpcTracer, guard := newTestDebankRPCTracer(t)
	hooks := guard.Hooks()
	to := common.HexToAddress("0x1001")
	tx := types.NewTx(&types.LegacyTx{
		To:       &to,
		Gas:      100_000,
		GasPrice: big.NewInt(1),
		V:        big.NewInt(27),
		R:        big.NewInt(1),
		S:        big.NewInt(1),
	})

	hooks.OnTxStart(testVMContext(), tx, common.Address{})
	hooks.OnLog(&types.Log{
		Address: to,
		Topics:  []common.Hash{common.HexToHash("0xabc")},
		Data:    []byte{0x01},
	})
	hooks.OnEnter(0, byte(vm.CALL), common.Address{}, to, nil, 79_000, big.NewInt(0))
	hooks.OnExit(0, nil, 1, nil, false)
	hooks.OnTxEnd(&types.Receipt{
		TxHash:            tx.Hash(),
		Status:            types.ReceiptStatusSuccessful,
		GasUsed:           21_001,
		CumulativeGasUsed: 21_001,
		EffectiveGasPrice: big.NewInt(7),
	}, nil)

	out := rpcTracer.GetOutPut(common.Hash{}, common.Hash{}, nil, nil, nil, nil)

	require.Len(t, out.BlockFile.Events, 1)
	require.NotEmpty(t, out.BlockFile.Events[0].ID)
	require.NotEmpty(t, out.BlockFile.Events[0].ParentTraceID)
}

func TestAdjustTopTraceGasUsedSubtractsIntrinsicGas(t *testing.T) {
	blockFile := &ptypes.BlockFile{
		Txs: []ptypes.Transaction{{
			ID:      "0xtx",
			GasUsed: big.NewInt(21_010),
		}},
		Traces: []ptypes.Trace{{
			ID:               "trace",
			TxID:             "0xtx",
			GasUsed:          big.NewInt(21_010),
			ParentTraceID:    "",
			PosInParentTrace: 0,
			TraceAddress:     []int64{},
		}},
	}

	adjustTopTraceGasUsed(blockFile, map[string]uint64{"0xtx": 21_000})

	require.Equal(t, int64(10), blockFile.Traces[0].GasUsed.Int64())
}

func TestDebankOutputEventsMarshalTxIDNull(t *testing.T) {
	out := newDebankOutPut(&ptypes.DebankOutPut{
		BlockFile: &ptypes.BlockFile{
			Events: []ptypes.Event{{
				ID:            "event",
				ParentTraceID: "trace",
			}},
		},
	})

	encoded, err := json.Marshal(out)

	require.NoError(t, err)
	require.Contains(t, string(encoded), `"tx_id":null`)
}

func newTestDebankRPCTracer(t *testing.T) (*ptracer.RPCTracer, *debankTraceGuard) {
	t.Helper()
	rpcTracer := &ptracer.RPCTracer{}
	header := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: 1_000_000,
		BaseFee:  big.NewInt(10),
		Time:     1,
	}
	rpcTracer.OnBlockStart(types.NewBlockWithHeader(header))
	guard := newDebankTraceGuard(rpcTracer, params.AllEthashProtocolChanges)
	return rpcTracer, guard
}

func testVMContext() *tracing.VMContext {
	return &tracing.VMContext{
		BlockNumber: big.NewInt(1),
		Time:        1,
		BaseFee:     big.NewInt(10),
	}
}
