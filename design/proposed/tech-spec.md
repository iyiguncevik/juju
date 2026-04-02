## Abstract

This document defines the technical implementation for the bootstrap flow of
the controller snap path. It specifies the bootstrap-time contracts between:

- juju client
- cloud init
- controller agent
- charm

The focus is implementation behavior from artifact selection through first
controller start, with controller-agent worker design as the primary concern.

## Rationale

`design/proposed/functional-spec.md` describes sequence. This document defines
the concrete technical contracts needed to implement that sequence without
duplicating logic across layers.

The main driver is the controller agent worker topology in
`cmd/jujud-controller/agent/model/manifolds.go`:

- several workers still call the API server even when running inside the
  controller agent process;
- controller-only workers can read/write state through local services directly;
- machine-agent-only workers must not remain in controller model manifolds;
- workers needed in both contexts require an explicit per-worker decision:
  controller-specific copy, or a shared worker behind a data-access facade.

Without this contract, bootstrap behavior remains harder to reason about,
slower than needed, and more failure-prone.

## Scope

This project handles only the bootstrap path for the first controller machine
(`controller/0`) until control is handed to normal charm-managed operations.

### In scope

- Bootstrap-time responsibilities for controller snap acquisition, delivery,
  install, and first-start readiness.
- Bootstrap command/config/API contracts.
- Controller-agent worker loading and data-access strategy for model manifolds.
- Artifact metadata and distribution contracts needed by bootstrap.

### Out of scope

- Build system design or implementation (`make`, linking strategy, build jobs).
- Snap packaging and release pipeline design.
- CI/CD job definitions.
- Post-bootstrap lifecycle behavior.

## Technical Specification (Bootstrap Flow)

### 1. juju client

Bootstrap contract:

- provision the initial controller machine;
- resolve controller snap artifacts for the selected Juju version;
- pass all controller snap install inputs into instance configuration;
- upload/register required artifacts before bootstrap completion.

Version and artifact resolution:

1. Determine target Juju version:
   - use `--agent-version` if supplied;
   - otherwise resolve from simplestreams (latest patch in configured stream).
2. Resolve controller snap artifact for the same target version:
   - `local` / `local-dangerous`: inspect provided snap metadata and require version match with resolved Juju version.
   - `snapstore`: query snap store/proxy and select a revision whose snap version matches resolved Juju version;
3. Acquire artifacts:
   - `local`: upload provided `.snap` and `.assert` files
   - `local-dangerous`: build and upload `.snap` without assertion validation
   - `snapstore`: `.snap` and `.assert` will be downloaded by cloud-init

Command surface:

- `--agent-version`
- `--build-agent` (machine/unit `jujud` tools only)
- `--controller-snap <file>`
- `--controller-snap-assert <file>`
- controller config `jujud-controller-snap-source` (`legacy`, `snapstore`,
  `local`, `local-dangerous`)

### 2. cloud init

Primary implementation files:

- `internal/cloudconfig/userdatacfg.go`
- `internal/cloudconfig/instancecfg/instancecfg.go`
- `internal/cloudconfig/cloudinit/cloudinit_ubuntu.go`

Current bootstrap behavior (baseline):

- `ConfigureJuju` downloads `jujud` tools (`addDownloadToolsCmds`), runs
  `jujud bootstrap-state` (`configureBootstrap`), and starts the machine agent
  (`addMachineAgentToBoot`).
- Snap logic only configures snap proxy/store assertions
  (`snap ack /etc/snap.assertions`, `snap set core proxy.store=...`).
- There is no `juju-controllerd` snap install/start/version-check step.

Bootstrap responsibilities:

- carry source-mode and artifact inputs from `juju bootstrap`;
- carry snap proxy inputs (`SnapStoreAssertions`, `SnapStoreProxyID`, `SnapStoreProxyURL`);
- execute deterministic `snap ack` / install / service start flow;
- fail explicitly when assertion handling or snap install fails.

Required changes for bootstrap flow:

1. **Bootstrap parameter plumbing**
   - Extend `StateInitializationParams` and `stateInitializationParamsInternal`
     with controller snap fields for source mode, expected version/channel, and
     optional inline snap/assert blobs.
   - Update `Marshal`/`Unmarshal` and propagation from bootstrap CLI/config into
     `InstanceConfig.Bootstrap`.
2. **Controller snap install command generation**
   - Add controller-snap command generation in `ConfigureJuju` with
     mode-specific flow:
     - `snapstore`: `snap download juju-controllerd --channel=<major.minor>/stable`,
       then `snap ack`, then `snap install`;
     - `local`: write embedded `.snap` and `.assert`, then `snap ack`, then
       `snap install`;
     - `local-dangerous`: write embedded `.snap`, then
       `snap install --dangerous`.
   - For all modes, execute `snap refresh --hold juju-controllerd`
     immediately after install.
3. **Bootstrap safety gates**
   - Add version coherence check: installed snap version must match target Juju
     version before bootstrap handoff.
   - Add explicit readiness gate for `juju-controllerd` before cloud-init
     reports bootstrap handoff complete.
4. **Failure semantics**
   - Keep fail-fast behavior (`set -xe`) and do not silently fall back across
     source modes.
   - Emit progress/error logs and remove temporary snap/assert files after use.

### 3. controller agent

This is the primary implementation section.

Primary focus file: `cmd/jujud-controller/agent/model/manifolds.go`.

#### 3.1 Bootstrap artifact contracts

Controller artifacts are stored in object store with explicit metadata:

- target Juju version;
- snap revision/channel;
- artifact kind (`controller-snap`, `controller-assert`, `agent-tools`);
- hashes (SHA256/SHA384), size, and object-store identity;
- source mode (`snapstore`, `local`, `local-dangerous`).

Integrity rules:

- verify hash before metadata registration;
- keep metadata immutable for `(version, revision, kind)`;
- use deterministic write/cleanup behavior on partial failures.

#### 3.2 Worker classification for `model/manifolds.go`

1. **Controller-only worker**
   - Worker exists only for controller responsibilities.
   - Action: keep in controller manifolds.

  1a. **Controller-only worker requiring data-access change**
    - Worker is controller-only but currently uses `APICallerName` for
      model-local data.
    - Action: remove controller-local API round-trips and switch to direct
      state/domain services access.

2. **Machine-agent-only worker**
   - Worker belongs to machine agent responsibilities and must not be loaded
     in controller model manifolds.
   - Action: remove from controller model manifold wiring.

3. **Shared worker (controller + machine)**
   - Worker is needed in both contexts.
   - Does not use `APICallerName`
   - Action: keep in both manifolds

  3a. **Shared worker requiring controller-side data-access change**
    - Worker is shared, and the controller-side path currently uses
      `APICallerName` for model-local data.
    - Action: decide case by case:
      - a controller-specific copy using local state/domain services; or
      - a facade-backed shared worker with controller-local and machine-API
        implementations.

#### 3.3 Manifold inventory from `model/manifolds.go`

Classification by manifold is:

This inventory is scoped to `cmd/jujud-controller/agent/model/manifolds.go`
only (the per-model engine).

It does not include controller machine-level manifolds in
`cmd/jujud-controller/agent/machine/manifolds.go` such as
`object-store`, `watcher-registry`, or `model-worker-manager`.
Those run in the machine engine and can also spawn additional child workers,
which is why observed runtime worker counts can be much higher than this list.

- `commonManifolds`:
  - `agent` → `3. Shared worker`
  - `clock` → `3. Shared worker`
  - `api-config-watcher` → `3. Shared worker`
  - `api-caller` → `3. Shared worker`
  - `provider-service-factories` → `1. Controller-only worker`
  - `domain-services` → `1. Controller-only worker`
  - `lease-manager` → `1. Controller-only worker`
  - `http-client` → `1. Controller-only worker`
  - `api-remote-relation-caller` → `1. Controller-only worker`
  - `log-sink` → `1. Controller-only worker`
  - `logging-config-updater` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `not-dead-flag` → `1. Controller-only worker`
  - `is-responsible-flag` → `1. Controller-only worker`
  - `valid-credential-flag` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `migration-fortress` → `3. Shared worker`
  - `migration-inactive-flag` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `migration-master` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `charm-revisioner` → `1. Controller-only worker`
  - `remote-relation-consumer` → `1. Controller-only worker`
  - `removal` → `1. Controller-only worker`
  - `provider-tracker` → `1. Controller-only worker`
  - `storage-provisioner` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `async-charm-downloader` → `1. Controller-only worker`
  - `secrets-pruner` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `user-secrets-drain-worker` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `operation-pruner` → `1. Controller-only worker`
  - `change-stream-pruner` → `3. Shared worker`

- `IAASManifolds` additions:
  - `compute-provisioner` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `firewaller` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `instance-poller` → `1. Controller-only worker`
  - `agent-binary-fetcher` → `1. Controller-only worker`

- `CAASManifolds` additions:
  - `caas-firewaller` → `1. Controller-only worker`
  - `caas-model-operator` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `caas-model-config-manager` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `caas-application-provisioner` → `1a. Controller-only worker requiring data-access change` ⚠️

No current entries in `model/manifolds.go` are classified as
`2. Machine-agent-only worker`.

Expected direction for bootstrap implementation from this classification:

- workers already driven by `DomainServicesName`, provider services, or local value workers remain on local service paths;
- workers using `APICallerName` are reviewed individually to remove controller-local API round-trips where not required;
- workers that fundamentally require remote calls keep explicit API usage;
- no machine-agent-only responsibilities are introduced into this manifold.

### 4. charm

The charm is responsible for bootstrap-time controller unit convergence:

- consume selected controller artifact metadata;
- ensure the controller snap is installed and services are started;
- expose clear blocked/error states for bootstrap failures;
- keep operations retry-safe and idempotent.

Bootstrap readiness requires successful charm-driven convergence on
`controller/0`.

## Design Decisions

1. **Version mismatch policy**
   - Decide whether version mismatch is warning or fatal.
   - Recommended: mismatch is fatal before bootstrap worker handoff.

2. **Snap storage location**
   - Decide whether snaps/assertions are stored as charm resource or not.