package ethapi

import (
	"math/big"
	"strings"
	"testing"

	"github.com/0xsoniclabs/sonic/evmcore"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDebankBlockFileForRPC(t *testing.T) {
	to := common.HexToAddress("0x1000000000000000000000000000000000000001")
	logAddr := common.HexToAddress("0x2000000000000000000000000000000000000002")
	revertedAddr := common.HexToAddress("0x3000000000000000000000000000000000000003")
	revertedChildAddr := common.HexToAddress("0x4000000000000000000000000000000000000004")
	topic0 := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	topic1 := common.HexToHash("0x0000000000000000000000005000000000000000000000000000000000000005")
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    7,
		To:       &to,
		Gas:      100_000,
		GasPrice: big.NewInt(7),
		Data:     []byte{0x01},
	})
	txID := tx.Hash().Hex()
	rootID := "root-trace"
	childID := "child-trace"
	errorRootID := "error-root"
	errorChildID := "error-child"

	blockFile := &ptypes.BlockFile{
		Txs: []ptypes.Transaction{{
			ID:               txID,
			GasPrice:         big.NewInt(1),
			GasUsed:          big.NewInt(1),
			Status:           false,
			TransactionIndex: 0,
		}},
		Traces: []ptypes.Trace{
			{
				ID:                rootID,
				TxID:              txID,
				GasUsed:           big.NewInt(70_000),
				CallType:          "call",
				To:                strings.ToLower(to.Hex()),
				SelfStorageChange: true,
			},
			{
				ID:               childID,
				TxID:             txID,
				ParentTraceID:    rootID,
				GasUsed:          big.NewInt(12),
				CallType:         "call",
				To:               strings.ToLower(logAddr.Hex()),
				TraceAddress:     []int64{0},
				PosInParentTrace: 0,
			},
		},
		ErrorTraces: []ptypes.Trace{
			{
				ID:                errorRootID,
				TxID:              txID,
				GasUsed:           big.NewInt(99),
				Output:            hexutil.Bytes{0x12},
				Error:             "execution reverted",
				CallType:          "call",
				To:                strings.ToLower(revertedAddr.Hex()),
				SelfStorageChange: true,
				StorageChange:     true,
			},
			{
				ID:                errorChildID,
				TxID:              txID,
				ParentTraceID:     errorRootID,
				GasUsed:           big.NewInt(11),
				CallType:          "call",
				To:                strings.ToLower(revertedChildAddr.Hex()),
				TraceAddress:      []int64{0},
				SelfStorageChange: true,
				StorageChange:     true,
			},
		},
		Events: []ptypes.Event{{
			ID:            "event-id",
			Address:       strings.ToLower(logAddr.Hex()),
			Selector:      topic0.Hex(),
			Topics:        []string{topic1.Hex()},
			Data:          hexutil.Bytes{0xaa},
			ParentTraceID: childID,
			Position:      3,
			LogIndex:      0,
		}},
		ErrorEvents: []ptypes.Event{{
			ID:            "reverted-event",
			Address:       strings.ToLower(revertedAddr.Hex()),
			Selector:      topic0.Hex(),
			Data:          hexutil.Bytes{0xbb},
			ParentTraceID: errorRootID,
		}},
	}

	receipt := &types.Receipt{
		TxHash:            tx.Hash(),
		Status:            types.ReceiptStatusSuccessful,
		GasUsed:           90_000,
		EffectiveGasPrice: big.NewInt(42),
		TransactionIndex:  2,
		Logs: []*types.Log{{
			Address: logAddr,
			Topics:  []common.Hash{topic0, topic1},
			Data:    []byte{0xaa},
			Index:   5,
		}},
	}

	normalizeDebankBlockFileForRPC(&evmcore.EvmBlock{Transactions: types.Transactions{tx}}, types.Receipts{receipt}, blockFile)

	require.Len(t, blockFile.Txs, 1)
	require.Equal(t, "42", blockFile.Txs[0].GasPrice.String())
	require.Equal(t, "90000", blockFile.Txs[0].GasUsed.String())
	require.True(t, blockFile.Txs[0].Status)
	require.EqualValues(t, 2, blockFile.Txs[0].TransactionIndex)

	rootTrace := requireDebankTrace(t, blockFile.Traces, rootID)
	require.Equal(t, "90000", rootTrace.GasUsed.String())
	requireDebankTrace(t, blockFile.Traces, errorChildID)
	require.Len(t, blockFile.ErrorTraces, 1)
	require.Equal(t, errorRootID, blockFile.ErrorTraces[0].ID)
	require.Nil(t, blockFile.ErrorTraces[0].GasUsed)
	require.Empty(t, blockFile.ErrorTraces[0].Output)
	require.False(t, blockFile.ErrorTraces[0].SelfStorageChange)
	require.False(t, blockFile.ErrorTraces[0].StorageChange)

	require.Len(t, blockFile.Events, 1)
	require.EqualValues(t, 5, blockFile.Events[0].LogIndex)
	require.Empty(t, blockFile.ErrorEvents)
	require.Equal(t, []string{strings.ToLower(to.Hex()), strings.ToLower(revertedChildAddr.Hex())}, blockFile.StorageContracts)
}

func requireDebankTrace(t *testing.T, traces []ptypes.Trace, id string) ptypes.Trace {
	t.Helper()
	for _, trace := range traces {
		if trace.ID == id {
			return trace
		}
	}
	require.Failf(t, "trace not found", "missing trace %s", id)
	return ptypes.Trace{}
}
