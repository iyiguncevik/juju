// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package backup

import (
	"context"

	"github.com/juju/clock"
	"github.com/juju/errors"
	"github.com/juju/worker/v5"
	"github.com/juju/worker/v5/dependency"

	corebackups "github.com/juju/juju/core/backups"
	"github.com/juju/juju/core/database"
	"github.com/juju/juju/core/lease"
	"github.com/juju/juju/core/logger"
	coremodel "github.com/juju/juju/core/model"
	"github.com/juju/juju/core/objectstore"
	"github.com/juju/juju/internal/services"
)

// LocalValues contains the current controller filesystem paths.
type LocalValues struct{ DataDir, LogDir string }

// LocalConfigReader reads paths each time the worker starts.
type LocalConfigReader interface{ BackupLocalValues() (LocalValues, error) }

// ManifoldConfig configures the archive creator and its lease service.
type ManifoldConfig struct {
	LeaseManagerName        string
	DomainServicesName      string
	ObjectStoreName         string
	ControllerUUID          string
	ControllerModelUUID     string
	ControllerNodeID        string
	MachineInstanceIDGetter GetMachineInstanceIDFunc
	LocalConfigReader       LocalConfigReader
	Clock                   clock.Clock
	Logger                  logger.Logger
	NewWorker               func(Config) (worker.Worker, error)
	NewArchiveGenerator     ArchiveGeneratorConstructor
}

// Validate checks dependencies before the manifold starts.
func (c ManifoldConfig) Validate() error {
	if c.LeaseManagerName == "" {
		return errors.NotValidf("empty LeaseManagerName")
	}
	if c.DomainServicesName == "" {
		return errors.NotValidf("empty DomainServicesName")
	}
	if c.ObjectStoreName == "" {
		return errors.NotValidf("empty ObjectStoreName")
	}
	if c.MachineInstanceIDGetter == nil {
		return errors.NotValidf("nil MachineInstanceIDGetter")
	}
	if c.ControllerNodeID == "" {
		return errors.NotValidf("empty ControllerNodeID")
	}
	if c.LocalConfigReader == nil {
		return errors.NotValidf("nil LocalConfigReader")
	}
	if c.Clock == nil {
		return errors.NotValidf("nil Clock")
	}
	if c.Logger == nil {
		return errors.NotValidf("nil Logger")
	}
	if c.NewWorker == nil {
		return errors.NotValidf("nil NewWorker")
	}
	if c.NewArchiveGenerator == nil {
		return errors.NotValidf("nil NewArchiveGenerator")
	}
	return nil
}

// Manifold outputs the creation function consumed by the API server.
func Manifold(c ManifoldConfig) dependency.Manifold {
	return dependency.Manifold{
		Inputs: []string{
			c.LeaseManagerName,
			c.DomainServicesName,
			c.ObjectStoreName,
		},
		Start: func(_ context.Context, getter dependency.Getter) (worker.Worker, error) {
			if err := c.Validate(); err != nil {
				return nil, err
			}
			paths, err := c.LocalConfigReader.BackupLocalValues()
			if err != nil {
				return nil, errors.Annotate(err, "reading backup paths")
			}
			if paths.DataDir == "" || paths.LogDir == "" {
				return nil, errors.NotValidf("empty backup paths")
			}
			var leaseManager lease.Manager
			if err := getter.Get(c.LeaseManagerName, &leaseManager); err != nil {
				return nil, err
			}
			var domainServices services.DomainServicesGetter
			if err := getter.Get(c.DomainServicesName, &domainServices); err != nil {
				return nil, err
			}
			var objectStores objectstore.ObjectStoreGetter
			if err := getter.Get(c.ObjectStoreName, &objectStores); err != nil {
				return nil, err
			}

			generator, err := c.NewArchiveGenerator(ArchiveGeneratorArgs{
				Config:         c,
				Paths:          paths,
				DomainServices: domainServices,
				ObjectStores:   objectStores,
			})
			if err != nil {
				return nil, err
			}

			return c.NewWorker(Config{
				LeaseManager:        leaseManager,
				ControllerUUID:      c.ControllerUUID,
				ControllerModelUUID: c.ControllerModelUUID,
				ControllerNodeID:    c.ControllerNodeID,
				CreateArchive:       generator,
				Clock:               c.Clock,
				Logger:              c.Logger,
			})
		},
		Output: output,
	}
}

// ArchiveGeneratorConstructor creates a substrate-specific archive generator
// from dependencies the backup manifold has resolved.
type ArchiveGeneratorConstructor func(ArchiveGeneratorArgs) (corebackups.BackupCreator, error)

// ArchiveGeneratorArgs supplies a generator constructor with the manifold
// configuration and its resolved dependencies.
type ArchiveGeneratorArgs struct {
	Config         ManifoldConfig
	Paths          LocalValues
	DomainServices services.DomainServicesGetter
	ObjectStores   objectstore.ObjectStoreGetter
}

// MachineArchiveGenerator creates the archive generator used by machine
// controllers. It records the controller machine base in archive metadata.
func MachineArchiveGenerator(args ArchiveGeneratorArgs) (corebackups.BackupCreator, error) {
	return machineArchiveGenerator{newArchiveGenerator(args)}, nil
}

// K8sArchiveGenerator creates the archive generator used by Kubernetes
// controllers. Kubernetes source provisioning facts are added with the
// Kubernetes recovery work; ordinary backups remain available meanwhile.
func K8sArchiveGenerator(args ArchiveGeneratorArgs) (corebackups.BackupCreator, error) {
	return k8sArchiveGenerator{newArchiveGenerator(args)}, nil
}

func output(in worker.Worker, out any) error {
	w, ok := in.(*Worker)
	if !ok {
		return errors.Errorf("expected backup creation worker, got %T", in)
	}
	target, ok := out.(*corebackups.BackupCreator)
	if !ok {
		return errors.Errorf("expected backup creation function, got %T", out)
	}
	*target = w
	return nil
}

// archiveGenerator resolves request-scoped services after acquiring the lease.
// This keeps worker startup independent of model and object-store readiness.
type archiveGenerator struct {
	config         ManifoldConfig
	paths          LocalValues
	domainServices services.DomainServicesGetter
	objectStores   objectstore.ObjectStoreGetter
}

func newArchiveGenerator(args ArchiveGeneratorArgs) archiveGenerator {
	return archiveGenerator{
		config:         args.Config,
		paths:          args.Paths,
		domainServices: args.DomainServices,
		objectStores:   args.ObjectStores,
	}
}

// create resolves request-scoped services and writes an archive using the
// source recorder selected for the running controller substrate.
func (g archiveGenerator) create(ctx context.Context, notes string, recorder func(string, MachineService) RecordSourceFunc) (*corebackups.Metadata, string, func(), error) {
	controllerServices, err := g.domainServices.ServicesForModel(ctx, coremodel.UUID(g.config.ControllerModelUUID))
	if err != nil {
		return nil, "", nil, err
	}

	modelServicesFor := ModelServicesGetterFunc(func(ctx context.Context, modelUUID coremodel.UUID) (ModelExportDomainServices, error) {
		modelServices, err := g.domainServices.ServicesForModel(ctx, modelUUID)
		if err != nil {
			return nil, err
		}
		return modelExportDomainServices{modelServices}, nil
	})

	controllerStore, err := g.objectStores.GetObjectStore(ctx, database.ControllerNS)
	if err != nil {
		return nil, "", nil, err
	}

	modelObjectStoreFor := ObjectStoreForModelFunc(func(ctx context.Context, modelUUID string) (ReadObjectStore, error) {
		return g.objectStores.GetObjectStore(ctx, modelUUID)
	})

	creator, err := newCreator(CreatorConfig{
		ControllerUUID:                g.config.ControllerUUID,
		ControllerModelUUID:           coremodel.UUID(g.config.ControllerModelUUID),
		ControllerID:                  g.config.ControllerNodeID,
		DataDir:                       g.paths.DataDir,
		LogDir:                        g.paths.LogDir,
		MachineInstanceIDGetter:       g.config.MachineInstanceIDGetter,
		ControllerExport:              controllerServices.ControllerExport(),
		ModelServicesGetter:           modelServicesFor,
		ControllerModelMachineService: controllerServices.Machine(),
		ModelConfig:                   controllerServices.Config(),
		ControllerModelLister:         controllerServices.Controller(),
		ControllerNodes:               controllerServices.ControllerNode(),
		ControllerObjectStore:         controllerStore,
		ObjectStoreForModel:           modelObjectStoreFor,
		RecordSource:                  recorder(g.config.ControllerNodeID, controllerServices.Machine()),
		Clock:                         g.config.Clock,
		Logger:                        g.config.Logger,
	})
	if err != nil {
		return nil, "", nil, err
	}
	return creator.Create(ctx, notes)
}

type machineArchiveGenerator struct{ archiveGenerator }

func (g machineArchiveGenerator) Create(ctx context.Context, notes string) (*corebackups.Metadata, string, func(), error) {
	return g.create(ctx, notes, machineSourceRecorder)
}

type k8sArchiveGenerator struct{ archiveGenerator }

func (g k8sArchiveGenerator) Create(ctx context.Context, notes string) (*corebackups.Metadata, string, func(), error) {
	return g.create(ctx, notes, noSourceRecorder)
}

// modelExportDomainServices narrows the model services factory for the creator.
type modelExportDomainServices struct{ domainServices services.DomainServices }

func (s modelExportDomainServices) Export() ModelExportService { return s.domainServices.Export() }
