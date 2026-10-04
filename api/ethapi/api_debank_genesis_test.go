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

	"github.com/Chaintable/pipeline/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestBuildDebankGenesisBlockFile(t *testing.T) {
	addr := common.HexToAddress("0x1")
	codeAddr := common.HexToAddress("0x2")
	storageAddr := common.HexToAddress("0x3")
	code := hexutil.Bytes{0x60, 0x00}
	storage := map[common.Hash]common.Hash{{}: common.HexToHash("0x1")}
	nativeID := "0x030000000000000000000000eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	for _, tc := range []struct {
		name    string
		alloc   types.GenesisAlloc
		ids     []string
		values  []int64
		codes   []hexutil.Bytes
		storage []string
	}{
		{
			name: "empty allocation still creates native token",
			ids:  []string{nativeID}, values: []int64{0}, codes: []hexutil.Bytes{{}}, storage: []string{},
		},
		{
			name:  "skip nil and zero balances",
			alloc: types.GenesisAlloc{addr: {}, codeAddr: {Balance: new(big.Int)}},
			ids:   []string{nativeID}, values: []int64{0}, codes: []hexutil.Bytes{{}}, storage: []string{},
		},
		{
			name: "balance and code are independent records",
			alloc: types.GenesisAlloc{
				storageAddr: {Storage: storage},
				codeAddr:    {Code: code},
				addr:        {Balance: big.NewInt(5), Code: code, Storage: storage},
			},
			ids: []string{
				"0x0100000000000000000000000000000000000000000000000000000000000001",
				"0x0200000000000000000000000000000000000000000000000000000000000001",
				"0x0200000000000000000000000000000000000000000000000000000000000002",
				nativeID,
			},
			values: []int64{5, 0, 0, 0}, codes: []hexutil.Bytes{{}, code, code, {}},
			storage: []string{addr.Hex(), storageAddr.Hex()},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := types.NewBlockWithHeader(&types.Header{Number: new(big.Int)})
			file := buildDebankGenesisBlockFile(block, tc.alloc)
			require.Len(t, file.Txs, len(tc.ids))
			require.Len(t, file.Traces, len(tc.ids))
			require.Equal(t, block.Hash().Hex(), file.Block.ID)
			require.Equal(t, tc.storage, file.StorageContracts)
			for i, tx := range file.Txs {
				require.Equal(t, tc.ids[i], tx.ID)
				decoded, err := hexutil.Decode(tx.ID)
				require.NoError(t, err)
				require.Len(t, decoded, common.HashLength)
				require.Equal(t, "0x"+tx.ID[26:], tx.To)
				require.Equal(t, (common.Address{}).Hex(), tx.From)
				require.Equal(t, int64(i), tx.TransactionIndex)
				require.True(t, tx.Status)
				require.Equal(t, tc.values[i], tx.Value.ToInt().Int64())
				require.Equal(t, tc.codes[i], tx.Input)
				for _, field := range []*big.Int{tx.Gas, tx.GasPrice, tx.GasUsed, tx.GasFeeCap, tx.GasTipCap, tx.Nonce} {
					require.NotNil(t, field)
					require.Zero(t, field.Sign())
				}
				trace := file.Traces[i]
				require.Equal(t, tx.ID, trace.TxID)
				require.Equal(t, util.ToHash([]string{tx.ID, "", "0"}), trace.ID)
				require.Equal(t, tx.From, trace.From)
				require.Equal(t, tx.To, trace.To)
				require.Equal(t, tx.Value, trace.Value)
				require.Equal(t, tc.codes[i], trace.Input)
				require.Equal(t, tc.codes[i], trace.Output)
				if tx.ID[2:4] == "01" {
					require.Equal(t, "call", trace.CallCreateType)
					require.Equal(t, "call", trace.CallType)
				} else {
					require.Equal(t, "create", trace.CallCreateType)
					require.Empty(t, trace.CallType)
				}
				require.Zero(t, trace.Gas.Sign())
				require.Zero(t, trace.GasUsed.Sign())
				require.Empty(t, trace.ParentTraceID)
				require.Zero(t, trace.PosInParentTrace)
				require.Zero(t, trace.Subtraces)
				require.Equal(t, []int64{}, trace.TraceAddress)
				require.False(t, trace.SelfStorageChange)
				require.False(t, trace.StorageChange)
			}
			encoded, err := json.Marshal(file)
			require.NoError(t, err)
			for _, key := range []string{"events", "error_events", "error_traces"} {
				require.Contains(t, string(encoded), `"`+key+`":[]`)
			}
			// Block metadata includes a processing timestamp; compare only allocation output.
			again := buildDebankGenesisBlockFile(block, tc.alloc)
			require.Equal(t, file.Txs, again.Txs)
			require.Equal(t, file.Traces, again.Traces)
			require.Equal(t, file.StorageContracts, again.StorageContracts)
		})
	}
}
