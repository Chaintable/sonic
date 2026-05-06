package ethapi

import (
	"math/big"
	"sort"
	"strings"

	"github.com/0xsoniclabs/sonic/inter/state"
	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
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
		origExist:    d.StateDB.Exist(addr),
		origNonce:    d.StateDB.GetNonce(addr),
		origCodeHash: d.StateDB.GetCodeHash(addr),
		storage:      make(map[common.Hash]common.Hash),
	}
	change.origBalance.Set(d.StateDB.GetBalance(addr))
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
		change.storage[key] = d.StateDB.GetState(addr, key)
	}
	return d.StateDB.SetState(addr, key, value)
}

func (d *debankStateDiffDB) SetStorage(addr common.Address, storage map[common.Hash]common.Hash) {
	change := d.getChange(addr)
	for key := range storage {
		if _, ok := change.storage[key]; !ok {
			change.storage[key] = d.StateDB.GetState(addr, key)
		}
	}
	d.StateDB.SetStorage(addr, storage)
}

func (d *debankStateDiffDB) SelfDestruct(addr common.Address) uint256.Int {
	change := d.getChange(addr)
	change.deleted = true
	return d.StateDB.SelfDestruct(addr)
}

func (d *debankStateDiffDB) SelfDestruct6780(addr common.Address) (uint256.Int, bool) {
	change := d.getChange(addr)
	prev, deleted := d.StateDB.SelfDestruct6780(addr)
	if deleted {
		change.deleted = true
	}
	return prev, deleted
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

func (d *debankStateDiffDB) BuildStateDiff(originRoot, root common.Hash) *ptypes.BlockStorageDiff {
	diff := &ptypes.BlockStorageDiff{
		Hash:       root,
		ParentHash: originRoot,
	}
	if diff.Hash == (common.Hash{}) {
		diff.Hash = types.EmptyRootHash
	}
	if diff.ParentHash == (common.Hash{}) {
		diff.ParentHash = types.EmptyRootHash
	}

	addrs := make([]common.Address, 0, len(d.changes))
	for addr := range d.changes {
		addrs = append(addrs, addr)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return strings.Compare(addrs[i].Hex(), addrs[j].Hex()) < 0
	})

	for _, addr := range addrs {
		change := d.changes[addr]
		addrHash := debankAddressHash(addr)
		if change.deleted || d.StateDB.HasSelfDestructed(addr) {
			diff.DeletedAccounts = append(diff.DeletedAccounts, addrHash)
			continue
		}
		if d.accountNeedsMetadata(addr, change) {
			diff.NewAccounts = append(diff.NewAccounts, ptypes.NewAccount{
				Address:  addrHash,
				Balance:  new(uint256.Int).Set(d.StateDB.GetBalance(addr)),
				Nonce:    d.StateDB.GetNonce(addr),
				CodeHash: d.StateDB.GetCodeHash(addr),
			})
		}
		if change.codeTouched {
			code := d.StateDB.GetCode(addr)
			if len(code) > 0 {
				diff.NewCodes = append(diff.NewCodes, ptypes.NewCode{
					CodeHash: d.StateDB.GetCodeHash(addr),
					Code:     common.CopyBytes(code),
				})
			}
		}
		if values := d.storageValues(addr, change); len(values) > 0 {
			diff.StorageDiff = append(diff.StorageDiff, ptypes.AccountStorageDiff{
				Address: addrHash,
				Values:  values,
			})
		}
	}
	return diff
}

func (d *debankStateDiffDB) StorageContractAddresses() []string {
	seen := make(map[string]struct{})
	for addr, change := range d.changes {
		if len(d.storageValues(addr, change)) == 0 {
			continue
		}
		seen[strings.ToLower(addr.Hex())] = struct{}{}
	}
	addrs := make([]string, 0, len(seen))
	for addr := range seen {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	return addrs
}

func (d *debankStateDiffDB) accountNeedsMetadata(addr common.Address, change *debankAccountChange) bool {
	if change.origExist != d.StateDB.Exist(addr) {
		return true
	}
	if change.origNonce != d.StateDB.GetNonce(addr) {
		return true
	}
	if change.origCodeHash != d.StateDB.GetCodeHash(addr) {
		return true
	}
	if change.origBalance.Cmp(d.StateDB.GetBalance(addr)) != 0 {
		return true
	}
	return len(d.storageValues(addr, change)) > 0
}

func (d *debankStateDiffDB) storageValues(addr common.Address, change *debankAccountChange) []ptypes.IndexValuePair {
	keys := make([]common.Hash, 0, len(change.storage))
	for key, original := range change.storage {
		if d.StateDB.GetState(addr, key) == original {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return strings.Compare(keys[i].Hex(), keys[j].Hex()) < 0
	})
	values := make([]ptypes.IndexValuePair, 0, len(keys))
	for _, key := range keys {
		value := d.StateDB.GetState(addr, key)
		values = append(values, ptypes.IndexValuePair{
			Index: crypto.Keccak256Hash(key[:]),
			Value: uint256.NewInt(0).SetBytes(value.Bytes()),
		})
	}
	return values
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
	tracer       *ptracer.RPCTracer
	txActive     bool
	txHasTopCall bool
}

func newDebankTraceHooks(tracer *ptracer.RPCTracer) *tracing.Hooks {
	guard := &debankTraceGuard{tracer: tracer}
	return &tracing.Hooks{
		OnTxStart: guard.OnTxStart,
		OnTxEnd:   guard.OnTxEnd,
		OnEnter:   guard.OnEnter,
		OnExit:    guard.OnExit,
		OnOpcode:  guard.OnOpcode,
		OnLog:     guard.OnLog,
	}
}

func (g *debankTraceGuard) OnTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	g.txActive = true
	g.txHasTopCall = false
	g.tracer.OnTxStart(env, tx, from)
}

func (g *debankTraceGuard) OnTxEnd(receipt *types.Receipt, err error) {
	defer func() {
		g.txActive = false
		g.txHasTopCall = false
	}()
	if receipt == nil || err != nil || !g.txHasTopCall {
		return
	}
	g.tracer.OnTxEnd(receipt, err)
}

func (g *debankTraceGuard) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if !g.txActive {
		return
	}
	if depth == 0 {
		g.txHasTopCall = true
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
	g.tracer.OnLog(log)
}
