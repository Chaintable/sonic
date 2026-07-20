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

	"github.com/0xsoniclabs/carmen/go/database/mpt"
	"github.com/0xsoniclabs/sonic/inter/state"
	"github.com/0xsoniclabs/sonic/utils/signers/internaltx"
	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

type debankStateDiffDB struct {
	state.StateDB
	changes   map[common.Address]*debankAccountChange
	snapshots []debankDiffSnapshot
}

type debankAccountChange struct {
	origExist    bool
	origBalance  uint256.Int
	origNonce    uint64
	origCodeHash common.Hash
	codeTouched  bool
	deleted      bool
	storage      map[common.Hash]common.Hash
}

type debankDiffSnapshot struct {
	id      int
	changes map[common.Address]*debankAccountChange
}

func newDebankStateDiffDB(db state.StateDB) *debankStateDiffDB {
	return &debankStateDiffDB{
		StateDB: db,
		changes: make(map[common.Address]*debankAccountChange),
	}
}

func (d *debankStateDiffDB) getChange(addr common.Address) *debankAccountChange {
	if change, ok := d.changes[addr]; ok {
		return change
	}
	change := &debankAccountChange{
		origExist:    d.Exist(addr),
		origNonce:    d.GetNonce(addr),
		origCodeHash: d.GetCodeHash(addr),
		storage:      make(map[common.Hash]common.Hash),
	}
	change.origBalance.Set(d.GetBalance(addr))
	d.changes[addr] = change
	return change
}

func (d *debankStateDiffDB) AddBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) uint256.Int {
	d.getChange(addr)
	return d.StateDB.AddBalance(addr, amount, reason)
}

func (d *debankStateDiffDB) SubBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) uint256.Int {
	d.getChange(addr)
	return d.StateDB.SubBalance(addr, amount, reason)
}

func (d *debankStateDiffDB) SetBalance(addr common.Address, amount *uint256.Int) {
	d.getChange(addr)
	d.StateDB.SetBalance(addr, amount)
}

func (d *debankStateDiffDB) SetNonce(addr common.Address, nonce uint64, reason tracing.NonceChangeReason) {
	d.getChange(addr)
	d.StateDB.SetNonce(addr, nonce, reason)
}

func (d *debankStateDiffDB) SetCode(addr common.Address, code []byte, reason tracing.CodeChangeReason) []byte {
	change := d.getChange(addr)
	change.codeTouched = true
	return d.StateDB.SetCode(addr, code, reason)
}

func (d *debankStateDiffDB) SetState(addr common.Address, key, value common.Hash) common.Hash {
	change := d.getChange(addr)
	if _, ok := change.storage[key]; !ok {
		change.storage[key] = d.GetState(addr, key)
	}
	return d.StateDB.SetState(addr, key, value)
}

func (d *debankStateDiffDB) SetStorage(addr common.Address, storage map[common.Hash]common.Hash) {
	change := d.getChange(addr)
	for key := range storage {
		if _, ok := change.storage[key]; !ok {
			change.storage[key] = d.GetState(addr, key)
		}
	}
	d.StateDB.SetStorage(addr, storage)
}

func (d *debankStateDiffDB) SelfDestruct(addr common.Address) {
	change := d.getChange(addr)
	change.deleted = true
	d.StateDB.SelfDestruct(addr)
}

func (d *debankStateDiffDB) CreateAccount(addr common.Address) {
	d.getChange(addr)
	d.StateDB.CreateAccount(addr)
}

func (d *debankStateDiffDB) CreateContract(addr common.Address) {
	d.getChange(addr)
	d.StateDB.CreateContract(addr)
}

func (d *debankStateDiffDB) Snapshot() int {
	id := d.StateDB.Snapshot()
	d.snapshots = append(d.snapshots, debankDiffSnapshot{
		id:      id,
		changes: cloneDebankChanges(d.changes),
	})
	return id
}

func (d *debankStateDiffDB) RevertToSnapshot(revid int) {
	d.StateDB.RevertToSnapshot(revid)
	for i := len(d.snapshots) - 1; i >= 0; i-- {
		if d.snapshots[i].id == revid {
			d.changes = cloneDebankChanges(d.snapshots[i].changes)
			d.snapshots = d.snapshots[:i]
			return
		}
	}
}

func (d *debankStateDiffDB) Copy() state.StateDB {
	return newDebankStateDiffDB(d.StateDB.Copy())
}

func (d *debankStateDiffDB) StateUpdateMaps() (map[common.Hash]struct{}, map[common.Hash][]byte, map[common.Hash]map[common.Hash][]byte, map[common.Hash][]byte) {
	destructs := make(map[common.Hash]struct{})
	accounts := make(map[common.Hash][]byte)
	storages := make(map[common.Hash]map[common.Hash][]byte)
	codes := make(map[common.Hash][]byte)

	for addr, change := range d.changes {
		addrHash := debankAddressHash(addr)
		if change.deleted || d.HasSelfDestructed(addr) {
			destructs[addrHash] = struct{}{}
			continue
		}
		if d.accountNeedsMetadata(addr, change) {
			accounts[addrHash] = debankSlimAccountRLP(
				d.GetNonce(addr),
				d.GetBalance(addr),
				d.GetCodeHash(addr),
			)
		}
		if change.codeTouched {
			code := d.GetCode(addr)
			if len(code) > 0 {
				codes[d.GetCodeHash(addr)] = common.CopyBytes(code)
			}
		}
		storage := d.storageUpdateMap(addr, change)
		if len(storage) > 0 {
			storages[addrHash] = storage
		}
	}
	return destructs, accounts, storages, codes
}

func (d *debankStateDiffDB) accountNeedsMetadata(addr common.Address, change *debankAccountChange) bool {
	if change.origExist != d.Exist(addr) {
		return true
	}
	if change.origNonce != d.GetNonce(addr) {
		return true
	}
	if change.origCodeHash != d.GetCodeHash(addr) {
		return true
	}
	if change.origBalance.Cmp(d.GetBalance(addr)) != 0 {
		return true
	}
	return d.hasStorageUpdate(addr, change)
}

func (d *debankStateDiffDB) hasStorageUpdate(addr common.Address, change *debankAccountChange) bool {
	for key, original := range change.storage {
		if d.GetState(addr, key) == original {
			continue
		}
		return true
	}
	return false
}

func (d *debankStateDiffDB) storageUpdateMap(addr common.Address, change *debankAccountChange) map[common.Hash][]byte {
	storage := make(map[common.Hash][]byte)
	for key, original := range change.storage {
		value := d.GetState(addr, key)
		if value == original {
			continue
		}
		storage[crypto.Keccak256Hash(key[:])] = debankStorageRLP(value)
	}
	return storage
}

func (d *debankStateDiffDB) stateUpdateMapsFromPostState(postState state.StateDB) (map[common.Hash]struct{}, map[common.Hash][]byte, map[common.Hash]map[common.Hash][]byte, map[common.Hash][]byte) {
	destructs := make(map[common.Hash]struct{})
	accounts := make(map[common.Hash][]byte)
	storages := make(map[common.Hash]map[common.Hash][]byte)
	codes := make(map[common.Hash][]byte)

	for addr, change := range d.changes {
		addrHash := debankAddressHash(addr)
		if !postState.Exist(addr) {
			if change.origExist || change.deleted || d.HasSelfDestructed(addr) {
				destructs[addrHash] = struct{}{}
			}
			continue
		}
		if d.accountNeedsMetadataFromPostState(addr, change, postState) {
			accounts[addrHash] = debankSlimAccountRLP(
				postState.GetNonce(addr),
				postState.GetBalance(addr),
				postState.GetCodeHash(addr),
			)
		}
		if change.codeTouched || change.origCodeHash != postState.GetCodeHash(addr) {
			code := postState.GetCode(addr)
			if len(code) > 0 {
				codes[postState.GetCodeHash(addr)] = common.CopyBytes(code)
			}
		}
		storage := d.storageUpdateMapFromPostState(addr, change, postState)
		if len(storage) > 0 {
			storages[addrHash] = storage
		}
	}
	return destructs, accounts, storages, codes
}

func (d *debankStateDiffDB) accountNeedsMetadataFromPostState(addr common.Address, change *debankAccountChange, postState state.StateDB) bool {
	if change.origExist != postState.Exist(addr) {
		return true
	}
	if change.origNonce != postState.GetNonce(addr) {
		return true
	}
	if change.origCodeHash != postState.GetCodeHash(addr) {
		return true
	}
	if change.origBalance.Cmp(postState.GetBalance(addr)) != 0 {
		return true
	}
	return d.hasStorageUpdateFromPostState(addr, change, postState)
}

func (d *debankStateDiffDB) hasStorageUpdateFromPostState(addr common.Address, change *debankAccountChange, postState state.StateDB) bool {
	for key, original := range change.storage {
		if postState.GetState(addr, key) == original {
			continue
		}
		return true
	}
	return false
}

func (d *debankStateDiffDB) storageUpdateMapFromPostState(addr common.Address, change *debankAccountChange, postState state.StateDB) map[common.Hash][]byte {
	storage := make(map[common.Hash][]byte)
	for key, original := range change.storage {
		value := postState.GetState(addr, key)
		if value == original {
			continue
		}
		storage[crypto.Keccak256Hash(key[:])] = debankStorageRLP(value)
	}
	return storage
}

func stateUpdateMapsFromCarmenDiff(diff mpt.Diff, postState state.StateDB) (map[common.Hash]struct{}, map[common.Hash][]byte, map[common.Hash]map[common.Hash][]byte, map[common.Hash][]byte, error) {
	destructs := make(map[common.Hash]struct{})
	accounts := make(map[common.Hash][]byte)
	storages := make(map[common.Hash]map[common.Hash][]byte)
	codes := make(map[common.Hash][]byte)

	for carmenAddr, accountDiff := range diff {
		if accountDiff == nil || accountDiff.Empty() {
			continue
		}
		addr := common.Address(carmenAddr)
		addrHash := debankAddressHash(addr)
		exists := postState.Exist(addr)

		if accountDiff.Reset {
			destructs[addrHash] = struct{}{}
		}

		if carmenDiffNeedsFinalAccount(accountDiff) {
			if exists {
				accounts[addrHash] = debankSlimAccountRLP(
					postState.GetNonce(addr),
					postState.GetBalance(addr),
					postState.GetCodeHash(addr),
				)
			} else {
				destructs[addrHash] = struct{}{}
			}
		}

		if accountDiff.Code != nil && exists {
			wantCodeHash := common.Hash(*accountDiff.Code)
			gotCodeHash := postState.GetCodeHash(addr)
			code := postState.GetCode(addr)
			if len(code) == 0 {
				if wantCodeHash != (common.Hash{}) && gotCodeHash != wantCodeHash {
					return nil, nil, nil, nil, fmt.Errorf("canonical Carmen diff code hash mismatch for %s: diff has %s, post-state has %s", addr, wantCodeHash, gotCodeHash)
				}
				continue
			}
			if gotCodeHash != wantCodeHash {
				return nil, nil, nil, nil, fmt.Errorf("canonical Carmen diff code hash mismatch for %s: diff has %s, post-state has %s", addr, wantCodeHash, gotCodeHash)
			}
			if len(code) > 0 {
				if hash := crypto.Keccak256Hash(code); hash != gotCodeHash {
					return nil, nil, nil, nil, fmt.Errorf("canonical post-state code bytes mismatch for %s: hash(code)=%s, codeHash=%s", addr, hash, gotCodeHash)
				}
				codes[gotCodeHash] = common.CopyBytes(code)
			}
		}

		if len(accountDiff.Storage) > 0 {
			storage := make(map[common.Hash][]byte, len(accountDiff.Storage))
			for key, value := range accountDiff.Storage {
				storage[crypto.Keccak256Hash(key[:])] = debankStorageRLP(common.Hash(value))
			}
			storages[addrHash] = storage
		}
	}
	return destructs, accounts, storages, codes, nil
}

func carmenDiffNeedsFinalAccount(diff *mpt.AccountDiff) bool {
	return diff.Balance != nil || diff.Nonce != nil || diff.Code != nil || len(diff.Storage) > 0
}

func debankStateUpdateMapSizes(destructs map[common.Hash]struct{}, accounts map[common.Hash][]byte, storages map[common.Hash]map[common.Hash][]byte, codes map[common.Hash][]byte) (int, int, int, int) {
	slots := 0
	for _, storage := range storages {
		slots += len(storage)
	}
	return len(destructs), len(accounts), slots, len(codes)
}

func debankSlimAccountRLP(nonce uint64, balance *uint256.Int, codeHash common.Hash) []byte {
	account := types.StateAccount{
		Nonce:    nonce,
		Balance:  new(uint256.Int).Set(balance),
		Root:     types.EmptyRootHash,
		CodeHash: codeHash.Bytes(),
	}
	return types.SlimAccountRLP(account)
}

func debankStorageRLP(value common.Hash) []byte {
	encoded, err := rlp.EncodeToBytes(value.Bytes())
	if err != nil {
		panic(err)
	}
	return encoded
}

func cloneDebankChanges(src map[common.Address]*debankAccountChange) map[common.Address]*debankAccountChange {
	dst := make(map[common.Address]*debankAccountChange, len(src))
	for addr, change := range src {
		cloned := &debankAccountChange{
			origExist:    change.origExist,
			origNonce:    change.origNonce,
			origCodeHash: change.origCodeHash,
			codeTouched:  change.codeTouched,
			deleted:      change.deleted,
			storage:      make(map[common.Hash]common.Hash, len(change.storage)),
		}
		cloned.origBalance.Set(&change.origBalance)
		for key, value := range change.storage {
			cloned.storage[key] = value
		}
		dst[addr] = cloned
	}
	return dst
}

func debankAddressHash(addr common.Address) common.Hash {
	return crypto.HashData(crypto.NewKeccakState(), addr.Bytes())
}

type debankTraceGuard struct {
	tracer           *ptracer.RPCTracer
	chainConfig      *params.ChainConfig
	txActive         bool
	txHasTopCall     bool
	currentTx        *types.Transaction
	pendingLogs      []*types.Log
	intrinsicGasByTx map[string]uint64
	internalTxByID   map[string]struct{}
}

func newDebankTraceGuard(tracer *ptracer.RPCTracer, chainConfig *params.ChainConfig) *debankTraceGuard {
	return &debankTraceGuard{
		tracer:           tracer,
		chainConfig:      chainConfig,
		intrinsicGasByTx: make(map[string]uint64),
		internalTxByID:   make(map[string]struct{}),
	}
}

func (g *debankTraceGuard) Hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnTxStart: g.OnTxStart,
		OnTxEnd:   g.OnTxEnd,
		OnEnter:   g.OnEnter,
		OnExit:    g.OnExit,
		OnOpcode:  g.OnOpcode,
		OnLog:     g.OnLog,
	}
}

func (g *debankTraceGuard) OnTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	g.txActive = true
	g.txHasTopCall = false
	g.currentTx = tx
	g.pendingLogs = g.pendingLogs[:0]
	txID := tx.Hash().Hex()
	if intrinsicGas, err := g.intrinsicGas(env, tx); err == nil {
		g.intrinsicGasByTx[txID] = intrinsicGas
	}
	if internaltx.IsInternal(tx) {
		g.internalTxByID[txID] = struct{}{}
	}
	g.tracer.OnTxStart(env, tx, from)
}

func (g *debankTraceGuard) OnTxEnd(receipt *types.Receipt, err error) {
	defer func() {
		g.txActive = false
		g.txHasTopCall = false
		g.currentTx = nil
		g.pendingLogs = g.pendingLogs[:0]
	}()
	if receipt == nil || err != nil || !g.txHasTopCall {
		return
	}
	g.flushPendingLogs()
	g.tracer.OnTxEnd(g.receiptForPipeline(receipt), err)
}

func (g *debankTraceGuard) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if !g.txActive {
		return
	}
	if depth == 0 {
		g.txHasTopCall = true
		g.tracer.OnEnter(depth, typ, from, to, input, gas, value)
		g.flushPendingLogs()
		return
	}
	if !g.txHasTopCall {
		return
	}
	g.tracer.OnEnter(depth, typ, from, to, input, gas, value)
}

func (g *debankTraceGuard) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	if !g.txActive || !g.txHasTopCall {
		return
	}
	g.tracer.OnExit(depth, output, gasUsed, err, reverted)
}

func (g *debankTraceGuard) OnOpcode(pc uint64, opcode byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	if !g.txActive || !g.txHasTopCall {
		return
	}
	g.tracer.OnOpcode(pc, opcode, gas, cost, scope, rData, depth, err)
}

func (g *debankTraceGuard) OnLog(log *types.Log) {
	if !g.txActive {
		return
	}
	if !g.txHasTopCall {
		g.pendingLogs = append(g.pendingLogs, copyLog(log))
		return
	}
	g.tracer.OnLog(log)
}

func (g *debankTraceGuard) AdjustBlockFile(blockFile *ptypes.BlockFile) {
	adjustTopTraceGasUsed(blockFile, g.intrinsicGasByTx)
}

func (g *debankTraceGuard) flushPendingLogs() {
	for _, log := range g.pendingLogs {
		if log == nil {
			continue
		}
		g.tracer.OnLog(log)
	}
	g.pendingLogs = g.pendingLogs[:0]
}

func (g *debankTraceGuard) receiptForPipeline(receipt *types.Receipt) *types.Receipt {
	if g.currentTx == nil {
		return receipt
	}
	if _, ok := g.internalTxByID[g.currentTx.Hash().Hex()]; !ok {
		return receipt
	}
	cpy := *receipt
	cpy.EffectiveGasPrice = new(big.Int)
	return &cpy
}

func (g *debankTraceGuard) intrinsicGas(env *tracing.VMContext, tx *types.Transaction) (uint64, error) {
	if g.chainConfig == nil || env == nil {
		return 0, nil
	}
	rules := g.chainConfig.Rules(env.BlockNumber, env.Random != nil, env.Time)
	return core.IntrinsicGas(
		tx.Data(),
		tx.AccessList(),
		tx.SetCodeAuthorizations(),
		tx.To() == nil,
		rules.IsHomestead,
		rules.IsIstanbul,
		rules.IsShanghai,
	)
}

func copyLog(log *types.Log) *types.Log {
	if log == nil {
		return nil
	}
	cpy := *log
	cpy.Topics = append([]common.Hash(nil), log.Topics...)
	cpy.Data = common.CopyBytes(log.Data)
	return &cpy
}
