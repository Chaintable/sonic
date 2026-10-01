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
	"math"
	"math/big"
	"os"
	"testing"

	"github.com/0xsoniclabs/sonic/evmcore"
	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
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
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7), GasUsed: 21_000},
		Transactions: types.Transactions{
			tx,
		},
	}

	err := validateDebankReplayReceipts(block, []evmcore.ProcessedTransaction{{
		Transaction: tx,
		Receipt:     receipt,
	}}, types.Receipts{receipt}, 21_000, common.HexToHash("0x01"), types.Bloom{})

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
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7), GasUsed: 21_000},
		Transactions: types.Transactions{
			tx,
		},
	}
	receiptHash := types.DeriveSha(types.Receipts{receipt}, trie.NewStackTrie(nil))

	err := validateDebankReplayReceipts(block, []evmcore.ProcessedTransaction{{
		Transaction: tx,
		Receipt:     receipt,
	}}, types.Receipts{receipt}, 21_000, receiptHash, types.Bloom{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "replayed logs bloom mismatch")
}

func TestValidateDebankReplayReceiptsCanonicalGasAccounting(t *testing.T) {
	// Captured eth_getBlockByNumber / eth_getBlockReceipts responses for the
	// two mainnet failures. Only fields needed to reproduce the roots are kept.
	data, err := os.ReadFile("testdata/debank_receipt_gas.json")
	require.NoError(t, err)
	var fixtures []struct {
		Number               hexutil.Uint64
		GasUsed              hexutil.Uint64
		ReceiptsRoot         common.Hash
		LogsBloom            types.Bloom
		Transactions         types.Transactions
		Receipts             types.Receipts
		ReplayedReceiptsRoot common.Hash
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	for _, fixture := range fixtures {
		t.Run(new(big.Int).SetUint64(uint64(fixture.Number)).String(), func(t *testing.T) {
			block := &evmcore.EvmBlock{
				EvmHeader: evmcore.EvmHeader{
					Number:  new(big.Int).SetUint64(uint64(fixture.Number)),
					GasUsed: uint64(fixture.GasUsed),
				},
				Transactions: fixture.Transactions,
			}
			require.Len(t, fixture.Receipts, len(block.Transactions))
			processed := make([]evmcore.ProcessedTransaction, len(fixture.Receipts))
			replayed := make(types.Receipts, len(fixture.Receipts))
			var usedGas uint64
			for i, canonical := range fixture.Receipts {
				receipt := *canonical
				usedGas += receipt.GasUsed
				receipt.CumulativeGasUsed = usedGas
				replayed[i] = &receipt
				processed[i] = evmcore.ProcessedTransaction{Transaction: block.Transactions[i], Receipt: &receipt}
			}
			require.Equal(t, fixture.ReceiptsRoot, types.DeriveSha(fixture.Receipts, trie.NewStackTrie(nil)))
			require.Equal(t, fixture.ReplayedReceiptsRoot, types.DeriveSha(replayed, trie.NewStackTrie(nil)))
			require.NotEqual(t, fixture.ReceiptsRoot, fixture.ReplayedReceiptsRoot)
			require.Less(t, usedGas, block.GasUsed)

			canonicalBefore, err := json.Marshal(fixture.Receipts)
			require.NoError(t, err)
			replayedBefore, err := json.Marshal(replayed)
			require.NoError(t, err)
			require.NoError(t, validateDebankReplayReceipts(block, processed, fixture.Receipts, usedGas, fixture.ReceiptsRoot, fixture.LogsBloom))
			canonicalAfter, err := json.Marshal(fixture.Receipts)
			require.NoError(t, err)
			replayedAfter, err := json.Marshal(replayed)
			require.NoError(t, err)
			require.Equal(t, canonicalBefore, canonicalAfter, "cached canonical receipts must not be modified")
			require.Equal(t, replayedBefore, replayedAfter, "tracer receipts must retain execution gas accounting")

			// Exercise the real disk encoding and DeriveFields path as well: a
			// restart evicts receipts with actual per-transaction GasUsed values.
			stored := make([]*types.ReceiptForStorage, len(fixture.Receipts))
			for i, receipt := range fixture.Receipts {
				stored[i] = (*types.ReceiptForStorage)(receipt)
			}
			encoded, err := rlp.EncodeToBytes(stored)
			require.NoError(t, err)
			var decoded []*types.ReceiptForStorage
			require.NoError(t, rlp.DecodeBytes(encoded, &decoded))
			coldReceipts := make(types.Receipts, len(decoded))
			for i, receipt := range decoded {
				coldReceipts[i] = (*types.Receipt)(receipt)
			}
			config := *params.AllEthashProtocolChanges
			config.ChainID = big.NewInt(146)
			require.NoError(t, coldReceipts.DeriveFields(&config, common.Hash{}, block.NumberU64(), 0, big.NewInt(50_000_000_000), new(big.Int), block.Transactions))
			require.NotEqual(t, fixture.Receipts[1].GasUsed, coldReceipts[1].GasUsed)
			require.Equal(t, fixture.ReceiptsRoot, types.DeriveSha(coldReceipts, trie.NewStackTrie(nil)))
			require.NoError(t, validateDebankReplayReceipts(block, processed, coldReceipts, usedGas, fixture.ReceiptsRoot, fixture.LogsBloom))
		})
	}
}

func TestValidateDebankReplayReceiptsRejectsExecutionDifferences(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*types.Receipt)
		want   string
	}{
		{"status", func(r *types.Receipt) { r.Status = types.ReceiptStatusFailed }, "receipt root mismatch"},
		{"type", func(r *types.Receipt) { r.Type = types.DynamicFeeTxType }, "receipt root mismatch"},
		{"log address", func(r *types.Receipt) { r.Logs[0].Address[0]++ }, "receipt root mismatch"},
		{"log topics", func(r *types.Receipt) { r.Logs[0].Topics[0][0]++ }, "receipt root mismatch"},
		{"log data", func(r *types.Receipt) { r.Logs[0].Data[0]++ }, "receipt root mismatch"},
		{"missing log", func(r *types.Receipt) { r.Logs = nil }, "receipt root mismatch"},
		{"bloom", func(r *types.Receipt) { r.Bloom[0]++ }, "receipt root mismatch"},
		{"more gas", func(r *types.Receipt) { r.GasUsed++ }, "gas used mismatch"},
		{"less gas", func(r *types.Receipt) { r.GasUsed-- }, "gas used mismatch"},
		{"cumulative gas", func(r *types.Receipt) { r.CumulativeGasUsed++ }, "cumulative gas mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block, processed, canonical := debankReceiptTestBlock()
			tc.modify(processed[0].Receipt)
			err := validateDebankReplayReceipts(block, processed, canonical, 42_000, types.DeriveSha(canonical, trie.NewStackTrie(nil)), types.MergeBloom(canonical))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestValidateDebankReplayReceiptsGasAndInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*evmcore.EvmBlock, *[]evmcore.ProcessedTransaction, *types.Receipts, *uint64)
		want   string
	}{
		{"canonical gaps", func(*evmcore.EvmBlock, *[]evmcore.ProcessedTransaction, *types.Receipts, *uint64) {}, ""},
		{"ordinary accounting", func(b *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			b.GasUsed = 42_000
			(*c)[0].CumulativeGasUsed = 21_000
			(*c)[1].CumulativeGasUsed = 42_000
		}, ""},
		{"derived gas with trailing block gas", func(_ *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			(*c)[0].GasUsed = 40_000
			(*c)[1].GasUsed = 40_000
		}, ""},
		{"derived gas without evidence of extra accounting", func(b *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			b.GasUsed = 80_000
			(*c)[0].GasUsed = 40_000
			(*c)[1].GasUsed = 40_000
		}, "gas used mismatch"},
		{"ordinary block gas mismatch", func(b *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction, c *types.Receipts, gas *uint64) {
			b.GasUsed = 42_000
			(*c)[0].CumulativeGasUsed = 21_000
			(*c)[1].CumulativeGasUsed = 42_000
			(*p)[0].Receipt.GasUsed--
			(*p)[0].Receipt.CumulativeGasUsed--
			(*p)[1].Receipt.CumulativeGasUsed--
			(*gas)--
		}, "gas used mismatch"},
		{"derived gas upper bound", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			(*c)[0].GasUsed = 40_000
			(*c)[1].GasUsed = 40_000
			(*p)[0].Receipt.GasUsed = 40_001
		}, "gas used mismatch"},
		{"replayed count", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction, _ *types.Receipts, _ *uint64) {
			*p = (*p)[:1]
		}, "replayed tx count mismatch"},
		{"canonical count", func(_ *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			*c = (*c)[:1]
		}, "canonical receipt count mismatch"},
		{"nil transaction", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction, _ *types.Receipts, _ *uint64) {
			(*p)[0].Transaction = nil
		}, "nil transaction"},
		{"transaction order", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction, _ *types.Receipts, _ *uint64) {
			(*p)[0], (*p)[1] = (*p)[1], (*p)[0]
		}, "replayed tx 0 mismatch"},
		{"nil replayed receipt", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction, _ *types.Receipts, _ *uint64) {
			(*p)[0].Receipt = nil
		}, "could not replay tx"},
		{"nil canonical receipt", func(_ *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			(*c)[0] = nil
		}, "canonical receipt 0"},
		{"canonical transaction order", func(_ *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			(*c)[0], (*c)[1] = (*c)[1], (*c)[0]
		}, "canonical receipt tx 0 mismatch"},
		{"total gas", func(_ *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, _ *types.Receipts, gas *uint64) { (*gas)++ }, "replayed gas used mismatch"},
		{"gas redistributed between transactions", func(_ *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction, _ *types.Receipts, _ *uint64) {
			(*p)[0].Receipt.GasUsed++
			(*p)[0].Receipt.CumulativeGasUsed++
			(*p)[1].Receipt.GasUsed--
		}, "gas used mismatch"},
		{"canonical gas decreases", func(_ *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			(*c)[1].CumulativeGasUsed = 1
		}, "invalid canonical cumulative gas"},
		{"canonical gas below transaction gas", func(_ *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			(*c)[1].CumulativeGasUsed = (*c)[0].CumulativeGasUsed + 1
		}, "invalid canonical cumulative gas"},
		{"block gas below receipts", func(b *evmcore.EvmBlock, _ *[]evmcore.ProcessedTransaction, _ *types.Receipts, _ *uint64) {
			b.GasUsed = 42_000
		}, "canonical receipt gas exceeds block gas"},
		{"overflow", func(b *evmcore.EvmBlock, p *[]evmcore.ProcessedTransaction, c *types.Receipts, _ *uint64) {
			b.GasUsed = math.MaxUint64
			(*p)[0].Receipt.GasUsed = math.MaxUint64
			(*p)[0].Receipt.CumulativeGasUsed = math.MaxUint64
			(*c)[0].GasUsed = math.MaxUint64
			(*c)[0].CumulativeGasUsed = math.MaxUint64
		}, "invalid canonical cumulative gas"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block, processed, canonical := debankReceiptTestBlock()
			root := types.DeriveSha(canonical, trie.NewStackTrie(nil))
			bloom := types.MergeBloom(canonical)
			gas := uint64(42_000)
			tc.modify(block, &processed, &canonical, &gas)
			if tc.want == "" {
				root = types.DeriveSha(canonical, trie.NewStackTrie(nil))
			}
			err := validateDebankReplayReceipts(block, processed, canonical, gas, root, bloom)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

func debankReceiptTestBlock() (*evmcore.EvmBlock, []evmcore.ProcessedTransaction, types.Receipts) {
	block := &evmcore.EvmBlock{EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7), GasUsed: 100_000}}
	var processed []evmcore.ProcessedTransaction
	var canonical types.Receipts
	for i := range 2 {
		tx := types.NewTx(&types.LegacyTx{Nonce: uint64(i), Gas: 50_000, GasPrice: big.NewInt(1)})
		receipt := &types.Receipt{
			TxHash: tx.Hash(), Status: types.ReceiptStatusSuccessful,
			GasUsed: 21_000, CumulativeGasUsed: uint64(i+1) * 21_000,
			Logs: []*types.Log{{Address: common.HexToAddress("0x1001"), Topics: []common.Hash{common.HexToHash("0x01")}, Data: []byte{1}}},
		}
		receipt.Bloom = types.CreateBloom(receipt)
		stored := *receipt
		stored.CumulativeGasUsed = uint64(i+1) * 40_000
		stored.Logs = []*types.Log{copyLog(receipt.Logs[0])}
		block.Transactions = append(block.Transactions, tx)
		processed = append(processed, evmcore.ProcessedTransaction{Transaction: tx, Receipt: receipt})
		canonical = append(canonical, &stored)
	}
	return block, processed, canonical
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
