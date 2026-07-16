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

package evmcore

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

func TestGenesisAllocMatchesSonicMainnetBlockZero(t *testing.T) {
	codeAccounts := 0
	storageSlots := 0
	for _, account := range GenesisAlloc {
		if len(account.Code) > 0 {
			codeAccounts++
		}
		storageSlots += len(account.Storage)
	}

	require.Len(t, GenesisAlloc, 22)
	require.Equal(t, 8, codeAccounts)
	require.Equal(t, 6, storageSlots)

	block := (&core.Genesis{
		Config: &params.ChainConfig{},
		Alloc:  GenesisAlloc,
	}).ToBlock()
	require.Equal(t, GenesisHeader.Root, block.Root())

	networkInitializer := GenesisAlloc[common.HexToAddress("0xd1005eed00000000000000000000000000000000")]
	require.Equal(t,
		common.HexToHash("0xe794ff9f5c3822faca2fdf6bd1060a61bcbd2e6445285a2216d26a5ea899aba2"),
		crypto.Keccak256Hash(networkInitializer.Code),
	)
}
