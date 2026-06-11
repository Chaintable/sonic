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

package evmstore

import (
	"testing"

	cc "github.com/0xsoniclabs/carmen/go/common"
	"github.com/0xsoniclabs/carmen/go/common/amount"
	carmen "github.com/0xsoniclabs/carmen/go/state"
	"github.com/stretchr/testify/require"
)

func TestArchiveStateDiffByNumberUsesS5Archive(t *testing.T) {
	params := carmen.Parameters{
		Variant:               "go-file",
		Schema:                5,
		Archive:               carmen.S5Archive,
		Directory:             t.TempDir(),
		BackgroundFlushPeriod: -1,
	}
	carmenState, err := carmen.NewState(params)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, carmenState.Close())
	}()

	store := &Store{
		parameters:  params,
		carmenState: carmenState,
	}

	addr := cc.Address{0x01}
	key := cc.Key{0x02}
	value := cc.Value{0x03}
	code := []byte{0x60, 0x01}
	update := cc.Update{}
	update.AppendCreateAccount(addr)
	update.AppendBalanceUpdate(addr, amount.New(42))
	update.AppendNonceUpdate(addr, cc.ToNonce(7))
	update.AppendCodeUpdate(addr, code)
	update.AppendSlotUpdate(addr, key, value)
	require.NoError(t, update.Normalize())
	require.NoError(t, carmenState.Apply(0, update))
	require.NoError(t, carmenState.Flush())

	diff, err := store.ArchiveStateDiffByNumber(0)
	require.NoError(t, err)
	accountDiff := diff[addr]
	require.NotNil(t, accountDiff)
	require.NotNil(t, accountDiff.Balance)
	require.NotNil(t, accountDiff.Nonce)
	require.NotNil(t, accountDiff.Code)
	require.Equal(t, value, accountDiff.Storage[key])
}

func TestArchiveStateDiffByNumberRequiresS5Archive(t *testing.T) {
	params := carmen.Parameters{
		Variant:               "go-file",
		Schema:                5,
		Archive:               carmen.NoArchive,
		Directory:             t.TempDir(),
		BackgroundFlushPeriod: -1,
	}
	carmenState, err := carmen.NewState(params)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, carmenState.Close())
	}()

	store := &Store{
		parameters:  params,
		carmenState: carmenState,
	}

	_, err = store.ArchiveStateDiffByNumber(0)
	require.ErrorContains(t, err, "requires S5 archive")
}

func TestStateUpdateHookRunsAfterSuccessfulApply(t *testing.T) {
	params := carmen.Parameters{
		Variant:               "go-file",
		Schema:                5,
		Archive:               carmen.NoArchive,
		Directory:             t.TempDir(),
		BackgroundFlushPeriod: -1,
	}
	carmenState, err := carmen.NewState(params)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, carmenState.Close())
	}()

	parentRoot, err := carmenState.GetHash()
	require.NoError(t, err)

	var hookBlock uint64
	var hookParentRoot cc.Hash
	var hookNewRoot cc.Hash
	var hookUpdate cc.Update
	recorder := newCarmenStateUpdateRecorder(carmenState, func() StateUpdateHook {
		return func(block uint64, parentRoot cc.Hash, newRoot cc.Hash, update cc.Update) {
			hookBlock = block
			hookParentRoot = parentRoot
			hookNewRoot = newRoot
			hookUpdate = update
			hookUpdate.Codes[0].Code[0] = 0xff
		}
	})

	addr := cc.Address{0x01}
	code := []byte{0x60, 0x01}
	update := cc.Update{}
	update.AppendCreateAccount(addr)
	update.AppendCodeUpdate(addr, code)
	require.NoError(t, update.Normalize())

	require.NoError(t, recorder.Apply(3, update))
	newRoot, err := carmenState.GetHash()
	require.NoError(t, err)

	require.Equal(t, uint64(3), hookBlock)
	require.Equal(t, parentRoot, hookParentRoot)
	require.Equal(t, newRoot, hookNewRoot)
	require.Equal(t, addr, hookUpdate.CreatedAccounts[0])
	require.Equal(t, byte(0x60), update.Codes[0].Code[0])
}
