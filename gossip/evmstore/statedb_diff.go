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
	"fmt"
	"reflect"
	"unsafe"

	"github.com/0xsoniclabs/carmen/go/database/mpt"
	carmen "github.com/0xsoniclabs/carmen/go/state"
	"github.com/0xsoniclabs/sonic/utils/caution"
)

type carmenArchiveDiffProvider interface {
	GetDiffForBlock(block uint64) (mpt.Diff, error)
}

// ArchiveStateDiffByNumber returns the canonical block-level Carmen archive
// diff introduced by number. Strict Debank tracing depends on this rather than
// replay touched-set approximations.
func (s *Store) ArchiveStateDiffByNumber(number uint64) (diff mpt.Diff, err error) {
	if s.carmenState == nil {
		return nil, fmt.Errorf("unable to get archive state diff - EvmStore is not open")
	}
	if s.parameters.Archive != carmen.S5Archive {
		return nil, fmt.Errorf("canonical Carmen archive diff requires S5 archive, configured archive is %q", s.parameters.Archive)
	}

	archiveState, err := s.carmenState.GetArchiveState(number)
	if err != nil {
		return nil, fmt.Errorf("unable to get archive state for block %d: %w", number, err)
	}
	defer caution.CloseAndReportError(&err, archiveState, "failed to close archive state")

	provider, err := carmenArchiveDiffProviderFromArchiveState(archiveState)
	if err != nil {
		return nil, err
	}
	diff, err = provider.GetDiffForBlock(number)
	if err != nil {
		return nil, fmt.Errorf("unable to get archive state diff for block %d: %w", number, err)
	}
	return diff, nil
}

func carmenArchiveDiffProviderFromArchiveState(archiveState carmen.State) (carmenArchiveDiffProvider, error) {
	value := reflect.ValueOf(archiveState)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil, fmt.Errorf("Carmen archive state %T cannot provide block diffs", archiveState)
	}
	elem := value.Elem()
	if elem.Kind() != reflect.Struct {
		return nil, fmt.Errorf("Carmen archive state %T cannot provide block diffs", archiveState)
	}
	field := elem.FieldByName("archive")
	if !field.IsValid() {
		return nil, fmt.Errorf("Carmen archive state %T does not expose an archive handle", archiveState)
	}
	if !field.CanAddr() {
		return nil, fmt.Errorf("Carmen archive state %T archive handle is not addressable", archiveState)
	}
	if field.Kind() == reflect.Interface && field.IsNil() {
		return nil, fmt.Errorf("Carmen archive state %T has no archive handle", archiveState)
	}

	archive := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface()
	provider, ok := archive.(carmenArchiveDiffProvider)
	if !ok {
		return nil, fmt.Errorf("Carmen archive %T does not expose block diffs", archive)
	}
	return provider, nil
}
