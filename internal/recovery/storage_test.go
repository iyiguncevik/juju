// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package recovery_test

import (
	"math"
	"testing"

	"github.com/juju/tc"

	"github.com/juju/juju/core/constraints"
	domainrecovery "github.com/juju/juju/domain/recovery"
	"github.com/juju/juju/internal/recovery"
)

type recoveryStorageSuite struct{}

func TestRecoveryStorageSuite(t *testing.T) { tc.Run(t, &recoveryStorageSuite{}) }
func (s *recoveryStorageSuite) TestGrowsDiskForArchiveAndImport(c *tc.C) {
	info := &domainrecovery.ArchiveInfo{Size: 10 << 30, ImportSize: 100 << 30}
	result, err := recovery.SizeRecoveryDisk(info, constraints.Value{RootDisk: new(uint64(8192))})
	c.Assert(err, tc.ErrorIsNil)
	c.Check(*result.RootDisk > uint64(135*1024), tc.IsTrue)
	larger := uint64(500 * 1024)
	result, err = recovery.SizeRecoveryDisk(info, constraints.Value{RootDisk: &larger})
	c.Assert(err, tc.ErrorIsNil)
	c.Check(*result.RootDisk, tc.Equals, larger)
}
func (s *recoveryStorageSuite) TestRejectsSizeOverflow(c *tc.C) {
	for _, info := range []*domainrecovery.ArchiveInfo{
		{Size: -1}, {ImportSize: -1}, {Size: math.MaxInt64}, {ImportSize: math.MaxInt64},
	} {
		_, err := recovery.SizeRecoveryDisk(info, constraints.Value{})
		c.Assert(err, tc.ErrorMatches, "archive recovery size exceeds supported range")
	}
}
