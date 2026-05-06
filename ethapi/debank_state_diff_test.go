package ethapi

import (
	"testing"

	"github.com/0xsoniclabs/sonic/inter/state"
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestDebankStateDiffIncludesMetadataForStorageOnlyAccount(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := state.NewMockStateDB(ctrl)

	addr := common.HexToAddress("0x1234")
	slot := common.HexToHash("0x01")
	oldValue := common.Hash{}
	newValue := common.HexToHash("0x02")
	codeHash := common.HexToHash("0x03")
	balance := uint256.NewInt(7)

	gomock.InOrder(
		mockState.EXPECT().Exist(addr).Return(true),
		mockState.EXPECT().GetNonce(addr).Return(uint64(3)),
		mockState.EXPECT().GetCodeHash(addr).Return(codeHash),
		mockState.EXPECT().GetBalance(addr).Return(balance),
		mockState.EXPECT().GetState(addr, slot).Return(oldValue),
		mockState.EXPECT().SetState(addr, slot, newValue).Return(oldValue),
	)
	mockState.EXPECT().HasSelfDestructed(addr).Return(false).AnyTimes()
	mockState.EXPECT().Exist(addr).Return(true).AnyTimes()
	mockState.EXPECT().GetNonce(addr).Return(uint64(3)).AnyTimes()
	mockState.EXPECT().GetCodeHash(addr).Return(codeHash).AnyTimes()
	mockState.EXPECT().GetBalance(addr).Return(balance).AnyTimes()
	mockState.EXPECT().GetState(addr, slot).Return(newValue).AnyTimes()

	diffDB := newDebankStateDiffDB(mockState)
	diffDB.SetState(addr, slot, newValue)

	diff := diffDB.BuildStateDiff(common.HexToHash("0x10"), common.HexToHash("0x20"))
	require.Len(t, diff.NewAccounts, 1)
	require.Equal(t, debankAddressHash(addr), diff.NewAccounts[0].Address)
	require.Equal(t, uint64(3), diff.NewAccounts[0].Nonce)
	require.Equal(t, codeHash, diff.NewAccounts[0].CodeHash)
	require.Equal(t, 0, diff.NewAccounts[0].Balance.Cmp(balance))
	require.Len(t, diff.StorageDiff, 1)
	require.Equal(t, debankAddressHash(addr), diff.StorageDiff[0].Address)
	require.Len(t, diff.StorageDiff[0].Values, 1)
}
