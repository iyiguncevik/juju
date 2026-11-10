// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package recovery

import (
	"math"

	"github.com/juju/juju/core/constraints"
	domainrecovery "github.com/juju/juju/domain/recovery"
	"github.com/juju/juju/internal/errors"
)

// SizeRecoveryDisk retains the source disk requirement and grows it when the
// archive and import workspace need more room. The target repeats a free-space
// check on its actual data filesystem before loading any database.
func SizeRecoveryDisk(info *domainrecovery.ArchiveInfo, cons constraints.Value) (constraints.Value, error) {
	const margin = int64(5 << 30)
	if info.Size < 0 || info.ImportSize < 0 || info.Size > math.MaxInt64-margin ||
		info.ImportSize > (math.MaxInt64-info.Size-margin)/5*4 {
		return cons, errors.New("archive recovery size exceeds supported range")
	}
	// 25% import headroom in addition to the shared 5 GiB disk reserve.
	importSize, err := recoveryImportSize(info)
	if err != nil {
		return cons, errors.Capture(err)
	}
	if importSize > (math.MaxInt64-info.Size-margin)/5*4 {
		return cons, errors.New("source database recovery size exceeds supported range")
	}
	needed := info.Size + importSize + importSize/4 + margin
	diskMiB := uint64(needed/(1<<20) + 1)
	diskMiB = max(diskMiB, 20*1024)
	if cons.RootDisk == nil || *cons.RootDisk < diskMiB {
		cons.RootDisk = &diskMiB
	}
	return cons, nil
}

// recoveryImportSize applies the same database capacity floor on the client
// and on the target's data filesystem before import.
func recoveryImportSize(info *domainrecovery.ArchiveInfo) (int64, error) {
	if info.ImportSize < 0 || info.DatabaseSizeBytes < 0 ||
		info.DatabaseSizeBytes > math.MaxInt64-info.ImportSize/3 {
		return 0, errors.New("source database recovery size exceeds supported range")
	}
	return max(info.ImportSize, info.DatabaseSizeBytes+info.ImportSize/3), nil
}
