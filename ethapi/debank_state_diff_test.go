package ethapi

import (
	"testing"

	"github.com/0xsoniclabs/sonic/inter/state"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
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

	diff := diffDB.BuildStateDiff(common.HexToHash("0x10"), common.HexToHash("0x20"))
	require.Len(t, diff.NewCodes, 1)
	require.Equal(t, codeHash, diff.NewCodes[0].CodeHash)
	require.Equal(t, code, diff.NewCodes[0].Code)
}

func TestValidateDebankReplayPostStateValuesAcceptsMatchingState(t *testing.T) {
	ctrl := gomock.NewController(t)
	replayState := state.NewMockStateDB(ctrl)
	postState := state.NewMockStateDB(ctrl)

	addr := common.HexToAddress("0x1001")
	slot := common.HexToHash("0x01")
	value := common.HexToHash("0x02")
	code := []byte{0x60, 0x00}
	codeHash := crypto.Keccak256Hash(code)
	balance := uint256.NewInt(9)

	replayState.EXPECT().Exist(addr).Return(true).AnyTimes()
	replayState.EXPECT().GetNonce(addr).Return(uint64(4)).AnyTimes()
	replayState.EXPECT().GetCodeHash(addr).Return(codeHash).AnyTimes()
	replayState.EXPECT().GetBalance(addr).Return(balance).AnyTimes()
	replayState.EXPECT().GetCode(addr).Return(code).AnyTimes()
	replayState.EXPECT().GetState(addr, slot).Return(value).AnyTimes()

	postState.EXPECT().Exist(addr).Return(true).AnyTimes()
	postState.EXPECT().GetNonce(addr).Return(uint64(4)).AnyTimes()
	postState.EXPECT().GetCodeHash(addr).Return(codeHash).AnyTimes()
	postState.EXPECT().GetBalance(addr).Return(balance).AnyTimes()
	postState.EXPECT().GetCode(addr).Return(code).AnyTimes()
	postState.EXPECT().GetState(addr, slot).Return(value).AnyTimes()

	diffDB := &debankStateDiffDB{
		StateDB: replayState,
		changes: map[common.Address]*debankAccountChange{
			addr: {
				codeTouched: true,
				storage: map[common.Hash]common.Hash{
					slot: common.Hash{},
				},
			},
		},
	}

	require.NoError(t, validateDebankReplayPostStateValues(1, diffDB, postState))
}

func TestValidateDebankReplayPostStateValuesRejectsStorageMismatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	replayState := state.NewMockStateDB(ctrl)
	postState := state.NewMockStateDB(ctrl)

	addr := common.HexToAddress("0x1001")
	slot := common.HexToHash("0x01")
	codeHash := common.HexToHash("0x03")
	balance := uint256.NewInt(9)

	replayState.EXPECT().Exist(addr).Return(true).AnyTimes()
	replayState.EXPECT().GetNonce(addr).Return(uint64(4)).AnyTimes()
	replayState.EXPECT().GetCodeHash(addr).Return(codeHash).AnyTimes()
	replayState.EXPECT().GetBalance(addr).Return(balance).AnyTimes()
	replayState.EXPECT().GetState(addr, slot).Return(common.HexToHash("0x02")).AnyTimes()

	postState.EXPECT().Exist(addr).Return(true).AnyTimes()
	postState.EXPECT().GetNonce(addr).Return(uint64(4)).AnyTimes()
	postState.EXPECT().GetCodeHash(addr).Return(codeHash).AnyTimes()
	postState.EXPECT().GetBalance(addr).Return(balance).AnyTimes()
	postState.EXPECT().GetState(addr, slot).Return(common.HexToHash("0x03")).AnyTimes()

	diffDB := &debankStateDiffDB{
		StateDB: replayState,
		changes: map[common.Address]*debankAccountChange{
			addr: {
				storage: map[common.Hash]common.Hash{
					slot: common.Hash{},
				},
			},
		},
	}

	err := validateDebankReplayPostStateValues(1, diffDB, postState)
	require.Error(t, err)
	require.Contains(t, err.Error(), "storage mismatch")
}
