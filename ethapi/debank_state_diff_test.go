package ethapi

import (
	"testing"

	cc "github.com/0xsoniclabs/carmen/go/common"
	"github.com/0xsoniclabs/carmen/go/common/amount"
	"github.com/0xsoniclabs/carmen/go/database/mpt"
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

func TestDebankStateDiffUsesWrappedStateHash(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockState := state.NewMockStateDB(ctrl)

	root := common.HexToHash("0x20")
	mockState.EXPECT().EndBlock(uint64(1))
	mockState.EXPECT().GetStateHash().Return(root)

	diffDB := newDebankStateDiffDB(mockState)
	diffDB.EndBlock(1)

	require.Equal(t, root, diffDB.GetStateHash())
}

func TestDebankStateDiffUsesCanonicalPostStateValues(t *testing.T) {
	ctrl := gomock.NewController(t)
	replayState := state.NewMockStateDB(ctrl)
	postState := state.NewMockStateDB(ctrl)

	addr := common.HexToAddress("0x1234")
	slot := common.HexToHash("0x01")
	origValue := common.HexToHash("0x02")
	postValue := common.HexToHash("0x03")
	postCode := []byte{0x60, 0x01}
	origCodeHash := types.EmptyCodeHash
	postCodeHash := crypto.Keccak256Hash(postCode)
	origBalance := uint256.NewInt(100)
	postBalance := uint256.NewInt(80)

	postState.EXPECT().Exist(addr).Return(true).AnyTimes()
	postState.EXPECT().GetNonce(addr).Return(uint64(3)).AnyTimes()
	postState.EXPECT().GetCodeHash(addr).Return(postCodeHash).AnyTimes()
	postState.EXPECT().GetBalance(addr).DoAndReturn(func(common.Address) *uint256.Int {
		return new(uint256.Int).Set(postBalance)
	}).AnyTimes()
	postState.EXPECT().GetCode(addr).Return(postCode).AnyTimes()
	postState.EXPECT().GetState(addr, slot).Return(postValue).AnyTimes()

	diffDB := &debankStateDiffDB{
		StateDB: replayState,
		changes: map[common.Address]*debankAccountChange{
			addr: {
				origExist:    true,
				origBalance:  *origBalance,
				origNonce:    1,
				origCodeHash: origCodeHash,
				codeTouched:  true,
				storage: map[common.Hash]common.Hash{
					slot: origValue,
				},
			},
		},
	}

	_, accounts, storages, codes := diffDB.stateUpdateMapsFromPostState(postState)
	account, err := types.FullAccount(accounts[debankAddressHash(addr)])
	require.NoError(t, err)
	require.Equal(t, uint64(3), account.Nonce)
	require.Equal(t, 0, account.Balance.Cmp(postBalance))
	require.Equal(t, postCodeHash, common.BytesToHash(account.CodeHash))
	require.Equal(t, postCode, codes[postCodeHash])
	_, content, _, err := rlp.Split(storages[debankAddressHash(addr)][crypto.Keccak256Hash(slot[:])])
	require.NoError(t, err)
	require.Equal(t, postValue.Bytes(), content)
}

func TestDebankStateDiffUsesCanonicalCarmenDiff(t *testing.T) {
	ctrl := gomock.NewController(t)
	postState := state.NewMockStateDB(ctrl)

	addr := common.HexToAddress("0x1234")
	slot := common.HexToHash("0x01")
	value := common.HexToHash("0x02")
	code := []byte{0x60, 0x01}
	codeHash := crypto.Keccak256Hash(code)
	balance := uint256.NewInt(80)

	carmenBalance := amount.NewFromUint256(balance)
	carmenNonce := cc.ToNonce(3)
	carmenCodeHash := cc.Hash(codeHash)
	diff := mpt.Diff{
		cc.Address(addr): {
			Balance: &carmenBalance,
			Nonce:   &carmenNonce,
			Code:    &carmenCodeHash,
			Storage: map[cc.Key]cc.Value{
				cc.Key(slot): cc.Value(value),
			},
		},
	}

	postState.EXPECT().Exist(addr).Return(true).AnyTimes()
	postState.EXPECT().GetNonce(addr).Return(uint64(3)).AnyTimes()
	postState.EXPECT().GetBalance(addr).DoAndReturn(func(common.Address) *uint256.Int {
		return new(uint256.Int).Set(balance)
	}).AnyTimes()
	postState.EXPECT().GetCodeHash(addr).Return(codeHash).AnyTimes()
	postState.EXPECT().GetCode(addr).Return(code).AnyTimes()

	destructs, accounts, storages, codes, err := stateUpdateMapsFromCarmenDiff(diff, postState)
	require.NoError(t, err)
	require.Empty(t, destructs)
	account, err := types.FullAccount(accounts[debankAddressHash(addr)])
	require.NoError(t, err)
	require.Equal(t, uint64(3), account.Nonce)
	require.Equal(t, 0, account.Balance.Cmp(balance))
	require.Equal(t, codeHash, common.BytesToHash(account.CodeHash))
	require.Equal(t, code, codes[codeHash])
	_, content, _, err := rlp.Split(storages[debankAddressHash(addr)][crypto.Keccak256Hash(slot[:])])
	require.NoError(t, err)
	require.Equal(t, value.Bytes(), content)
}

func TestDebankStateDiffUsesCarmenResetAndFinalUpdates(t *testing.T) {
	ctrl := gomock.NewController(t)
	postState := state.NewMockStateDB(ctrl)

	addr := common.HexToAddress("0x1234")
	slot := common.HexToHash("0x01")
	balance := uint256.NewInt(1)
	carmenBalance := amount.NewFromUint256(balance)
	diff := mpt.Diff{
		cc.Address(addr): {
			Reset:   true,
			Balance: &carmenBalance,
			Storage: map[cc.Key]cc.Value{
				cc.Key(slot): {},
			},
		},
	}

	postState.EXPECT().Exist(addr).Return(true).AnyTimes()
	postState.EXPECT().GetNonce(addr).Return(uint64(0)).AnyTimes()
	postState.EXPECT().GetBalance(addr).DoAndReturn(func(common.Address) *uint256.Int {
		return new(uint256.Int).Set(balance)
	}).AnyTimes()
	postState.EXPECT().GetCodeHash(addr).Return(types.EmptyCodeHash).AnyTimes()

	destructs, accounts, storages, _, err := stateUpdateMapsFromCarmenDiff(diff, postState)
	require.NoError(t, err)
	require.Contains(t, destructs, debankAddressHash(addr))
	require.Contains(t, accounts, debankAddressHash(addr))
	_, content, _, err := rlp.Split(storages[debankAddressHash(addr)][crypto.Keccak256Hash(slot[:])])
	require.NoError(t, err)
	require.Equal(t, common.Hash{}.Bytes(), content)
}

func TestDebankStateDiffFailsWhenCarmenCodeHashDoesNotMatchPostState(t *testing.T) {
	ctrl := gomock.NewController(t)
	postState := state.NewMockStateDB(ctrl)

	addr := common.HexToAddress("0x1234")
	diffCodeHash := cc.Hash(common.HexToHash("0x01"))
	diff := mpt.Diff{
		cc.Address(addr): {
			Code: &diffCodeHash,
		},
	}

	postState.EXPECT().Exist(addr).Return(true).AnyTimes()
	postState.EXPECT().GetNonce(addr).Return(uint64(0)).AnyTimes()
	postState.EXPECT().GetBalance(addr).Return(uint256.NewInt(0)).AnyTimes()
	postState.EXPECT().GetCodeHash(addr).Return(common.HexToHash("0x02")).AnyTimes()
	postState.EXPECT().GetCode(addr).Return([]byte{0x60}).AnyTimes()

	_, _, _, _, err := stateUpdateMapsFromCarmenDiff(diff, postState)
	require.ErrorContains(t, err, "code hash mismatch")
}
