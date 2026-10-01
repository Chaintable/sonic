// Copyright 2026 Sonic Operations Ltd
// This file is part of the Sonic Client
//
// Sonic is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Sonic is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Sonic. If not, see <http://www.gnu.org/licenses/>.

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
	"github.com/stretchr/testify/require"
)

func TestValidateDebankReplayTransactionsCanonicalGasAccounting(t *testing.T) {
	// Mainnet execution gas from callTracer. Stored cumulative gas includes
	// additional accounting, so it cannot validate replay completeness. In
	// block 80175307, tx 1 has derived receipt gas 192796 but executes with
	// 104861 gas, even though every cumulative delta fits its tx gas limit.
	for _, tc := range []struct {
		number    uint64
		blockGas  uint64
		replayGas []uint64
	}{
		{80174946, 1786070, []uint64{371620, 34382, 165188}},
		{80174947, 2248113, []uint64{95312, 553798, 137034, 553798}},
		{80175307, 3334561, []uint64{72308, 104861, 343623, 225670, 423643, 530878, 60326, 104861, 219204, 219206, 313905}},
	} {
		t.Run(new(big.Int).SetUint64(tc.number).String(), func(t *testing.T) {
			block := &evmcore.EvmBlock{EvmHeader: evmcore.EvmHeader{Number: new(big.Int).SetUint64(tc.number), GasUsed: tc.blockGas}}
			var processed []evmcore.ProcessedTransaction
			var cumulative uint64
			for i, gas := range tc.replayGas {
				tx := types.NewTx(&types.LegacyTx{Nonce: uint64(i), Gas: gas, GasPrice: big.NewInt(1)})
				cumulative += gas
				block.Transactions = append(block.Transactions, tx)
				processed = append(processed, evmcore.ProcessedTransaction{
					Transaction: tx,
					Receipt:     &types.Receipt{TxHash: tx.Hash(), Status: types.ReceiptStatusSuccessful, GasUsed: gas, CumulativeGasUsed: cumulative},
				})
			}
			require.Less(t, cumulative, block.GasUsed)
			require.NoError(t, validateDebankReplayTransactions(block, processed))
			require.Equal(t, tc.replayGas[1], processed[1].Receipt.GasUsed, "preserve execution gas in tracer receipts")
		})
	}
}

func TestValidateDebankReplayTransactionsCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*evmcore.EvmBlock, *[]evmcore.ProcessedTransaction)
		want   string
	}{
		{"complete", func(*evmcore.EvmBlock, *[]evmcore.ProcessedTransaction) {}, ""},
		{"empty block", func(b *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction) {
			b.Transactions = nil
			*p = nil
		}, ""},
		{"reverted transaction", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction) {
			(*p)[0].Receipt.Status = types.ReceiptStatusFailed
		}, ""},
		{"missing transaction", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction) {
			*p = (*p)[:1]
		}, "replayed tx count mismatch"},
		{"extra transaction", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction) {
			*p = append(*p, (*p)[0])
		}, "replayed tx count mismatch"},
		{"nil transaction", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction) {
			(*p)[0].Transaction = nil
		}, "nil transaction"},
		{"transaction order", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction) {
			(*p)[0], (*p)[1] = (*p)[1], (*p)[0]
		}, "replayed tx 0 mismatch"},
		{"skipped transaction", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction) {
			(*p)[1].Receipt = nil
		}, "could not replay tx 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := &evmcore.EvmBlock{EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7)}}
			var processed []evmcore.ProcessedTransaction
			for i := range 2 {
				tx := types.NewTx(&types.LegacyTx{Nonce: uint64(i), Gas: 21_000, GasPrice: big.NewInt(1)})
				block.Transactions = append(block.Transactions, tx)
				processed = append(processed, evmcore.ProcessedTransaction{Transaction: tx, Receipt: &types.Receipt{TxHash: tx.Hash(), Status: types.ReceiptStatusSuccessful}})
			}
			tc.modify(block, &processed)
			err := validateDebankReplayTransactions(block, processed)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
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

func TestDebankGenesisStateDiffIncludesFullAllocation(t *testing.T) {
	diff := ptracer.GenesisAllocToStateDiff(evmcore.GenesisAlloc)

	require.Len(t, diff.NewAccounts, 22)
	require.Len(t, diff.NewCodes, 8)

	storageSlots := 0
	for _, account := range diff.StorageDiff {
		storageSlots += len(account.Values)
	}
	require.Equal(t, 6, storageSlots)
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
