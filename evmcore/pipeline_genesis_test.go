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
