## Abstract

This document defines the end-state technical solution for distributing and
running Juju controller agents as a dedicated snap. It describes what each
component does once implementation is complete.

It complements `design/proposed/workflow-spec.md`, which describes workflow
behavior. This document focuses on component responsibilities, interfaces, and
implementation contracts.

## Rationale

The workflow specification defines sequence and behavior. Implementation still
needs a precise technical contract for:

- command/config surfaces;
- cloud-init and bootstrap internals;
- worker/charm ownership boundaries;
- artifact storage and API distribution contracts;
- upgrade target decision ownership.

Without this, logic can be duplicated across CLI, workers, and charm code.

## Scope

### In scope

- End-state component responsibilities for controller snap distribution.
- End-state command/config/API contracts.
- Domain metadata/storage model for controller snap and assertion artifacts.
- Required decisions and unresolved decisions tied to implementation.

### Out of scope

- Build system design or implementation (`make`, linking strategy, build jobs).
- Snap packaging/release pipeline design.
- CI/CD job definitions.

## Technical Specification (End State)

### 1. juju CLI changes

#### 2.1 `juju bootstrap`

End-state bootstrap contract:

- bootstrap provisions controller machine(s);
- bootstrap supplies controller snap acquisition/install inputs to cloud-init;
- bootstrap ensures controller artifacts are registered for controller use.

Bootstrap version and artifact resolution is explicit:

1. Determine target Juju version:
   - use `--agent-version` if supplied;
   - otherwise resolve from simplestreams (latest patch in configured stream).
2. Resolve controller snap artifact for the same target version:
   - `snapstore`: query snap store/proxy and select a revision whose snap
     version matches the resolved Juju version;
   - `local` / `local-dangerous`: inspect provided snap metadata and require
     version match with the resolved Juju version.
3. Acquire artifacts:
   - `snapstore`: download `.snap` and `.assert`;
   - `local`: use provided `.snap` and `.assert`;
   - `local-dangerous`: use provided `.snap` without assertion validation.

Command surface includes:

- `--agent-version` for Juju version intent.
- `--build-agent` for machine/unit `jujud` tools packaging only.
- `--controller-snap <file>` and `--controller-snap-assert <file>` for explicit
  local controller artifact input.

Controller snap upload behavior:

- no extra "upload controller snap" flag is required;
- when controller snap artifacts are selected (from store or local), bootstrap
  uploads/registers them as part of normal bootstrap completion.

Controller snap build behavior:

- bootstrap does not define an in-command "build controller snap" step;
- this avoids heavy snap rebuild work on each bootstrap run;
- developer workflow is to reuse a prebuilt local snap (same artifact reused
  across multiple bootstraps) or use snap store/proxy sourced artifacts.
- bootstrap is artifact-consumer only; it does not require a controller
  `snapcraft.yaml` to exist in this repository.
- whether the snap is built from this repo or another repo is outside bootstrap
  contract; bootstrap consumes the resulting `.snap`/`.assert` artifacts.

Validation rules:

- `--controller-snap` is required when source mode is `local` or
  `local-dangerous`.
- `--controller-snap-assert` is required when source mode is `local`.
- local artifact version must match resolved Juju version.
- architecture/base compatibility must be validated before install.
- if no matching revision exists in snap store for resolved version, bootstrap
  fails with an explicit version-resolution error.

Additional bootstrap details that must be specified:

- cloud-init receives snap proxy/assertion settings from instance config.
- cloud-init performs `snap ack`/install flow for assertion-backed installs.
- bootstrap records selected controller snap revision/channel in persisted
  metadata.
- bootstrap readiness requires successful controller runtime start on
  `controller/0`.
- artifact upload/registration failures abort bootstrap; no silent fallback path.

#### 2.3 `controller-config jujud-controller-snap-source`

This config key is the source-mode contract for controller snap acquisition.
Supported values are:

- `snapstore`
- `local`
- `local-dangerous`

Validation remains enforced in `controller/config.go`.

### 2. Cloud-init and instance configuration

Primary component: `internal/cloudconfig/instancecfg`.

Cloud-init/instancecfg responsibilities:

- carry controller snap source-mode inputs;
- carry snap proxy inputs (`SnapStoreAssertions`, `SnapStoreProxyID`,
  `SnapStoreProxyURL`);
- install and start required controller runtime services deterministically;
- fail explicitly when controller snap install/assert processing fails.

Scripts and service setup must be idempotent.

### 3. Controller Agent changes

Controller artifacts are stored in object store and represented with explicit
metadata. Required metadata fields:

- Juju version target.
- Snap revision/channel.
- Artifact kind (`controller-snap`, `controller-assert`, `agent-tools`).
- Hashes (SHA256/SHA384), size, and object-store identity.
- Source mode (`snapstore`, `local`, `local-dangerous`).

Data integrity contracts:

- hash verified before metadata registration;
- artifact metadata immutable for a fixed version+revision+kind tuple;
- object-store and metadata writes follow deterministic cleanup/error behavior.

#### 3.1. Bootstrap worker responsibilities

Primary components:

- `internal/bootstrap/*`
- `internal/worker/bootstrap/deployer.go`

Bootstrap worker responsibilities in end state:

- upload machine/unit tools artifacts as required for `jujud`;
- upload controller snap and assertion artifacts;
- register controller artifact metadata;
- ensure artifacts are available to controller units via replicated object store.

#### 3.2. API server artifact distribution contracts

Primary component: `apiserver/tools.go` plus associated routing.

API responsibilities:

- continue serving tools artifact endpoints for machine/unit agent binaries;
- expose controller artifact download endpoints for snap/assert retrieval;
- enforce existing request authentication/authorization and model/controller
  scoping;
- return deterministic HTTP errors for missing/invalid artifact requests.

### 4. Controller charm/operator responsibilities

Controller charm/operator logic is the steady-state owner of controller snap
lifecycle:

- install/refresh/restart controller snap;
- enforce controller-unit convergence to the selected target revision;
- apply peer/readiness checks before declaring a unit converged;
- surface actionable blocked/error unit state on failures;
- execute retry-safe, idempotent operations.

## Component Change Matrix

| Component | End-state responsibility |
| --- | --- |
| `cmd/juju/commands/bootstrap.go` | Accept and pass controller artifact inputs required by bootstrap. |
| `cmd/juju/commands/upgradecontroller.go` | Orchestrate controller upgrade command flow and post-controller model-upgrade sequencing. |
| `controller/config.go` | Validate and expose `jujud-controller-snap-source` source-mode contract. |
| `internal/cloudconfig/instancecfg/instancecfg.go` | Render cloud-init/service inputs for controller snap install and snap proxy assertion handling. |
| `internal/bootstrap/*` | Ingest/upload/register controller artifacts during bootstrap. |
| `internal/worker/bootstrap/deployer.go` | Orchestrate bootstrap uploader/deployer responsibilities. |
| `domain/agentbinary/service/*` and state | Persist/query immutable controller artifact metadata and object-store references with hash checks. |
| `apiserver/tools.go` (+ routing) | Serve tools and controller artifact endpoints with auth/scoping and deterministic error mapping. |
| Controller charm/operator code | Own controller snap lifecycle convergence for bootstrap steady-state, HA, and upgrades. |
| `internal/worker/upgrader/upgrader.go` | Handle `jujud` tools upgrades only. |

## Decisions
