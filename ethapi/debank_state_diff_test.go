package ethapi

import (
	"testing"

	"github.com/0xsoniclabs/sonic/inter/state"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
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

	_, accounts, storages, _ := diffDB.StateUpdateMaps()
	require.Len(t, accounts, 1)
	account, err := types.FullAccount(accounts[debankAddressHash(addr)])
	require.NoError(t, err)
	require.Equal(t, uint64(3), account.Nonce)
	require.Equal(t, codeHash, common.BytesToHash(account.CodeHash))
	require.Equal(t, 0, account.Balance.Cmp(balance))
	require.Len(t, storages, 1)
	storage := storages[debankAddressHash(addr)]
	require.Len(t, storage, 1)
	_, content, _, err := rlp.Split(storage[crypto.Keccak256Hash(slot[:])])
	require.NoError(t, err)
	require.Equal(t, newValue.Bytes(), content)
}

func TestDebankStateDiffDeduplicatesNewCodesByHash(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := state.NewMockStateDB(ctrl)

	addr1 := common.HexToAddress("0x1001")
	addr2 := common.HexToAddress("0x1002")
	code := []byte{0x60, 0x00, 0x60, 0x00}
	codeHash := crypto.Keccak256Hash(code)
	zeroBalance := uint256.NewInt(0)

	mockState.EXPECT().Exist(gomock.Any()).Return(false).AnyTimes()
	mockState.EXPECT().GetNonce(gomock.Any()).Return(uint64(0)).AnyTimes()
	mockState.EXPECT().GetCodeHash(gomock.Any()).Return(codeHash).AnyTimes()
	mockState.EXPECT().GetBalance(gomock.Any()).Return(zeroBalance).AnyTimes()
	mockState.EXPECT().SetCode(addr1, code, tracing.CodeChangeContractCreation).Return(nil)
	mockState.EXPECT().SetCode(addr2, code, tracing.CodeChangeContractCreation).Return(nil)
	mockState.EXPECT().HasSelfDestructed(gomock.Any()).Return(false).AnyTimes()
	mockState.EXPECT().GetCode(gomock.Any()).Return(code).AnyTimes()

	diffDB := newDebankStateDiffDB(mockState)
	diffDB.SetCode(addr1, code, tracing.CodeChangeContractCreation)
	diffDB.SetCode(addr2, code, tracing.CodeChangeContractCreation)

	_, _, _, codes := diffDB.StateUpdateMaps()
	require.Len(t, codes, 1)
	require.Equal(t, code, codes[codeHash])
}

func TestDebankStateDiffUsesExpectedReplayRoot(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := state.NewMockStateDB(ctrl)

	root := common.HexToHash("0x20")
	diffDB := newDebankStateDiffDB(mockState)
	diffDB.SetExpectedReplayRoot(root)
	diffDB.EndBlock(1)

	require.Equal(t, root, diffDB.GetStateHash())
}
