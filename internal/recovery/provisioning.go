// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package recovery

import (
	"github.com/juju/juju/core/constraints"
	"github.com/juju/juju/core/network/ipfamily"
	model "github.com/juju/juju/domain/export/types/v4_1_0"
	domainrecovery "github.com/juju/juju/domain/recovery"
	"github.com/juju/juju/internal/errors"
)

// sourceConstraints projects the source's recorded requirements and observed
// hardware. Missing requirements remain absent for shared bootstrap defaults.
func sourceConstraints(info *domainrecovery.ArchiveInfo, machines []model.Machine,
	rows []model.Constraint, spaces []model.ConstraintSpace, tags []model.ConstraintTag,
	zones []model.ConstraintZone, machineConstraints []model.MachineConstraint,
	hardware []model.MachineCloudInstance,
) (constraints.Value, error) {
	var machineUUID, constraintUUID string
	for _, row := range machines {
		if row.Name == info.MachineName {
			machineUUID = row.UUID
		}
	}
	for _, row := range machineConstraints {
		if row.MachineUUID == machineUUID {
			constraintUUID = row.ConstraintUUID
		}
	}
	var value constraints.Value
	for _, row := range rows {
		if row.UUID != constraintUUID {
			continue
		}
		if row.ContainerTypeID != nil && *row.ContainerTypeID != 0 {
			return value, errors.New("container controller provisioning is unsupported")
		}
		if row.IpFamily != nil {
			family, err := ipfamily.ParseIPFamily(*row.IpFamily)
			if err != nil {
				return value, err
			}
			value.IPFamily = &family
		}
		value.Arch, value.RootDiskSource = row.Arch, row.RootDiskSource
		value.InstanceRole, value.InstanceType = row.InstanceRole, row.InstanceType
		value.VirtType, value.ImageID = row.VirtType, row.ImageID
		for _, pair := range []struct {
			source *int64
			target **uint64
		}{
			{row.CpuCores, &value.CpuCores}, {row.CpuPower, &value.CpuPower},
			{row.Mem, &value.Mem}, {row.RootDisk, &value.RootDisk},
		} {
			if pair.source == nil {
				continue
			}
			if *pair.source < 0 {
				return value, errors.New("negative source provisioning constraint")
			}
			n := uint64(*pair.source)
			*pair.target = &n
		}
		if row.AllocatePublicIp != nil {
			if *row.AllocatePublicIp != 0 && *row.AllocatePublicIp != 1 {
				return value, errors.New("invalid source public IP constraint")
			}
			allocate := *row.AllocatePublicIp == 1
			value.AllocatePublicIP = &allocate
		}
	}
	var sourceSpaces, sourceTags, sourceZones []string
	for _, row := range spaces {
		if row.ConstraintUUID != constraintUUID {
			continue
		}
		space := row.Space
		if row.Exclude {
			space = "^" + space
		}
		sourceSpaces = append(sourceSpaces, space)
	}
	for _, row := range tags {
		if row.ConstraintUUID == constraintUUID {
			sourceTags = append(sourceTags, row.Tag)
		}
	}
	for _, row := range zones {
		if row.ConstraintUUID == constraintUUID {
			sourceZones = append(sourceZones, row.Zone)
		}
	}
	if len(sourceSpaces) > 0 {
		value.Spaces = &sourceSpaces
	}
	if len(sourceTags) > 0 {
		value.Tags = &sourceTags
	}
	if len(sourceZones) > 0 {
		value.Zones = &sourceZones
	}
	for _, row := range hardware {
		if row.MachineUUID != machineUUID {
			continue
		}
		if value.Arch == nil {
			value.Arch = row.Arch
		}
		for _, pair := range []struct {
			source *int64
			target **uint64
		}{
			{row.CpuCores, &value.CpuCores}, {row.CpuPower, &value.CpuPower},
			{row.Mem, &value.Mem}, {row.RootDisk, &value.RootDisk},
		} {
			if pair.source == nil {
				continue
			}
			if *pair.source == 0 {
				continue
			}
			if *pair.source < 0 {
				return value, errors.New("negative source hardware observation")
			}
			n := uint64(*pair.source)
			if *pair.target == nil || **pair.target < n {
				*pair.target = &n
			}
		}
		if value.RootDiskSource == nil {
			value.RootDiskSource = row.RootDiskSource
		}
		if value.VirtType == nil {
			value.VirtType = row.VirtType
		}
	}
	return value, nil
}
