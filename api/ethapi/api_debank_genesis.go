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
	"fmt"
	"math/big"
	"sort"
	"strings"

	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

func buildDebankGenesisBlockFile(block *types.Block, alloc types.GenesisAlloc) *ptypes.BlockFile {
	file := &ptypes.BlockFile{
		Block:            util.BuildPipelineBlock(block),
		Txs:              make([]ptypes.Transaction, 0),
		Events:           make([]ptypes.Event, 0),
		Traces:           make([]ptypes.Trace, 0),
		ErrorEvents:      make([]ptypes.Event, 0),
		ErrorTraces:      make([]ptypes.Trace, 0),
		StorageContracts: make([]string, 0),
	}
	addresses := make([]common.Address, 0, len(alloc))
	for addr := range alloc {
		addresses = append(addresses, addr)
	}
	// Match pipeline's ordering so replay produces the same synthetic indexes.
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].Hex() < addresses[j].Hex() })
	for _, addr := range addresses {
		account := alloc[addr]
		address := strings.ToLower(addr.Hex())
		if len(account.Storage) > 0 {
			file.StorageContracts = append(file.StorageContracts, address)
		}
		if account.Balance != nil && account.Balance.Sign() > 0 {
			appendDebankGenesisRecord(file, 1, address, account.Balance, hexutil.Bytes{})
		}
		if len(account.Code) > 0 {
			appendDebankGenesisRecord(file, 2, address, new(big.Int), account.Code)
		}
	}
	appendDebankGenesisRecord(file, 3, "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", new(big.Int), hexutil.Bytes{})
	return file
}

func appendDebankGenesisRecord(file *ptypes.BlockFile, kind int, address string, value *big.Int, code hexutil.Bytes) {
	// Pipeline v0.0.69 uses bytes32 IDs: kind + 22 zeroes + the 40-digit address.
	txID := fmt.Sprintf("0x%02d%022d%s", kind, 0, strings.TrimPrefix(address, "0x"))
	zeroAddress := (common.Address{}).Hex()
	file.Txs = append(file.Txs, ptypes.Transaction{
		ID:               txID,
		From:             zeroAddress,
		To:               address,
		Gas:              new(big.Int),
		GasPrice:         new(big.Int),
		GasUsed:          new(big.Int),
		Status:           true,
		GasFeeCap:        new(big.Int),
		GasTipCap:        new(big.Int),
		Input:            code,
		Nonce:            new(big.Int),
		TransactionIndex: int64(len(file.Txs)),
		Value:            (*hexutil.Big)(value),
	})
	trace := ptypes.Trace{
		ID:               util.ToHash([]string{txID, "", "0"}),
		From:             zeroAddress,
		To:               address,
		Gas:              new(big.Int),
		GasUsed:          new(big.Int),
		Input:            code,
		Output:           code,
		Value:            (*hexutil.Big)(value),
		CallCreateType:   "create",
		TxID:             txID,
		ParentTraceID:    "",
		PosInParentTrace: 0,
		TraceAddress:     []int64{},
	}
	if kind == 1 {
		trace.CallCreateType = "call"
		trace.CallType = "call"
	}
	file.Traces = append(file.Traces, trace)
}
