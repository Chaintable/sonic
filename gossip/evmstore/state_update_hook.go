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
	cc "github.com/0xsoniclabs/carmen/go/common"
	carmen "github.com/0xsoniclabs/carmen/go/state"
)

// StateUpdateHook receives the canonical block-level Carmen update after it has
// been applied successfully to live state.
type StateUpdateHook func(block uint64, parentRoot cc.Hash, newRoot cc.Hash, update cc.Update)

// SetStateUpdateHook registers a live/import commit-time state update hook.
func (s *Store) SetStateUpdateHook(hook StateUpdateHook) {
	s.stateUpdateHookMu.Lock()
	defer s.stateUpdateHookMu.Unlock()
	s.stateUpdateHook = hook
}

func (s *Store) getStateUpdateHook() StateUpdateHook {
	s.stateUpdateHookMu.RLock()
	defer s.stateUpdateHookMu.RUnlock()
	return s.stateUpdateHook
}

type carmenStateUpdateRecorder struct {
	carmen.State
	hook func() StateUpdateHook
}

func newCarmenStateUpdateRecorder(state carmen.State, hook func() StateUpdateHook) carmen.State {
	return &carmenStateUpdateRecorder{
		State: state,
		hook:  hook,
	}
}

func (r *carmenStateUpdateRecorder) Apply(block uint64, update cc.Update) (<-chan error, error) {
	var hook StateUpdateHook
	if r.hook != nil {
		hook = r.hook()
	}
	if hook == nil {
		return r.State.Apply(block, update)
	}

	parentRoot, err := r.GetHash()
	if err != nil {
		return nil, err
	}
	canonicalUpdate := cloneCarmenUpdate(update)
	archiveDone, err := r.State.Apply(block, update)
	if err != nil {
		return archiveDone, err
	}
	newRoot, err := r.GetHash()
	if err != nil {
		return archiveDone, err
	}
	hook(block, parentRoot, newRoot, canonicalUpdate)
	return archiveDone, nil
}

func cloneCarmenUpdate(update cc.Update) cc.Update {
	clone := cc.Update{
		DeletedAccounts: append([]cc.Address(nil), update.DeletedAccounts...),
		CreatedAccounts: append([]cc.Address(nil), update.CreatedAccounts...),
		Balances:        append([]cc.BalanceUpdate(nil), update.Balances...),
		Nonces:          append([]cc.NonceUpdate(nil), update.Nonces...),
		Codes:           append([]cc.CodeUpdate(nil), update.Codes...),
		Slots:           append([]cc.SlotUpdate(nil), update.Slots...),
	}
	for i := range clone.Codes {
		clone.Codes[i].Code = append([]byte(nil), clone.Codes[i].Code...)
	}
	return clone
}
