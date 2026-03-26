## Abstract

In the controller-as-a-snap initiative, controller responsibilities are separated from machine/unit responsibilities at both worker and binary levels, so controller machines run both `jujud` and `juju-controllerd` instead of a single combined agent process. This removes the current bootstrap role-transition "dance" where one agent mode is stopped and another is started, and it enables controller-agent lifecycle control through the controller charm. During bootstrap, however, we still need an initial API server before the machine agent can create and manage charms, so the startup sequence must explicitly account for that chicken-and-egg constraint.

## Rationale

This document exists to align the team on the three workflows that matter for this initiative: bootstrap, controller HA scale-out, and controller upgrade. Its purpose is to define those technical workflows before implementation starts, without mixing in project planning, organization details, or delivery process.

Compared to the current system, the target state in this document moves controller-specific behavior away from generic agent paths and into controller-charm-managed controller workflows. That gives clearer ownership boundaries between `jujud` (machine/unit agent responsibilities) and `juju-controllerd` (controller responsibilities).

This is intentionally not the end-state architecture for Juju as a whole. It is a scoped target for the current project: agree concrete workflow contracts, reduce cross-component ambiguity, and keep the initiative focused enough to deliver meaningful outcomes in a reasonable timeframe.

## Specification

In this target workflow, controller machines run `jujud` and `juju-controllerd` concurrently. `jujud` keeps machine/unit-agent responsibilities, while `juju-controllerd` runs controller-specific services and workers. This explicit coexistence replaces the current bootstrap role-switch pattern and makes controller lifecycle ownership through the controller charm unambiguous.

### Components

#### `juju-controllerd` (snap payload)

- Installed as the `juju-controllerd` snap on controller machines.
- Runs controller-specific services and workers.
- Does not run machine/unit-agent responsibilities.

#### `jujud` (machine/unit agent binary)

- Continues to be distributed through existing simplestreams mechanisms.
- Runs on controller machines to provide machine-agent responsibilities.
- Does not include controller-specific bootstrap logic, API server, dq-lite dependency.

#### Controller charm

- Declares and manages controller snap lifecycle as part of controller application operations.
- Ensures the correct `juju-controllerd` snap revision is installed and running on controller units.
- Coordinates controller-unit lifecycle behavior via normal charm operations.

#### Cloud-init bootstrap scripts

- Initialize controller machine bootstrap steps.
- Start baseline agent processes required to reach controller charm management.
- Apply environment-specific artifact acquisition flow (store/proxy/local).

### Workflow: bootstrap (initial controller)

1. Juju client provisions the controller machine and renders cloud-init configuration.
2. Cloud-init acquires and starts both `juju-controllerd` and `jujud`:
   - Production:
     - the target version is resolved from simplestreams (existing mechanism, unchanged);
     - the matching `juju-controllerd` snap revision is resolved from the snap store using the
       Juju version as the channel qualifier (e.g. `juju-controllerd --channel=3.6/stable`);
     - cloud-init runs `snap download juju-controllerd --channel=<version>/stable` to fetch the
       snap and its assertion file from the store;
     - immediately after installation, auto-refresh is held:
       `snap refresh --hold juju-controllerd`;
     - `jujud` is downloaded from simplestreams as today.
   - Development:
     - `jujud` is built and uploaded by the Juju client when `--build-agent` is passed
       (existing `BuildAgentTarball` path, unchanged);
     - a new `--controller-snap=<path>` flag accepts the path to a locally built `.snap` file;
       the client embeds the snap blob and its assert in cloud-init data and cloud-init
       sideloads it with `snap install --dangerous <snap-file>`;
     - the snap is **not** built automatically by the Juju client to avoid the cost of a full
       snapcraft build on every bootstrap. The developer must build and provide it separately
       (see snap build cost notes below).
   - Airgapped:
     - `juju-controllerd` is downloaded from the snap store proxy;
     - `jujud` is downloaded through the simplestreams proxy;
     - both a Snap Proxy and a SimpleStreams Proxy must be configured.
3. Cloud-init waits until `juju-controllerd` has started successfully.
4. The `juju-controllerd` bootstrap worker triggers deployment of the controller charm.
5. The `juju-controllerd` bootstrap worker stores the snap and assert in object store.
6. The `juju-controllerd` bootstrap worker stores tools in the object store.
7. The controller charm verifies peer relations and confirms that the `juju-controllerd` snap version matches the charm version.

#### Bootstrap params and cloud-init changes

The following additions to the bootstrap path are required:

- **New `--controller-snap=<path>` CLI flag**: accepts a path to a local `.snap` file for the
  development path. The client also looks for an adjacent `<snap-name>.assert` file; if present,
  it is embedded alongside the snap blob. This flag is separate from `--build-agent` because
  building a snap is significantly heavier than building a binary tarball (see snap build cost
  notes below), and the two artifacts have independent lifecycles.

- **`StateInitializationParams` additions**: two new optional fields carry the controller snap
  blob and its assert blob when using the development path. Production and airgapped paths leave
  these fields empty and rely on cloud-init pulling directly from the store or proxy.

- **Cloud-init template changes**:
  - Production/airgapped: emit a `snap download` + `snap ack` + `snap install` sequence targeting
    the versioned channel, followed by `snap refresh --hold`.
  - Development (snap provided): emit an inline file write for the `.snap` and `.assert` blobs,
    followed by `snap ack <assert-file>` + `snap install --dangerous <snap-file>` +
    `snap refresh --hold`.

- **Version coherence check**: after cloud-init installs the snap, it must verify that the
  installed snap's version matches the target Juju version before proceeding. This prevents a
  mismatch between the simplestreams-resolved version and the actual snap revision from reaching
  the bootstrap worker.

#### Snap build cost and workarounds

Building the `juju-controllerd` snap with `snapcraft` is a heavy operation (it creates an
isolated build environment and recompiles the binary from scratch) and is not suitable as an
on-every-bootstrap step for development. Practical workarounds:

1. **Build once, reuse**: run `snapcraft` once and pass the resulting `.snap` to every subsequent
   `juju bootstrap --controller-snap=./juju-controllerd.snap`. Rebuild only when the binary or
   snap metadata changes.
2. **`snapcraft pack` with pre-built binary**: place the compiled `juju-controllerd` binary into
   the snap prime directory, then run `snapcraft pack prime/` to assemble the `.snap` without
   a full rebuild. This reuses an existing binary (e.g. one compiled with `go build`) and only
   does the snap packaging step, which is fast.
3. **Download from CI**: download a pre-built `.snap` from a CI artifact (e.g. a GitHub Actions
   run) and pass it via `--controller-snap`. This avoids any local build entirely.

#### snapcraft.yaml location

The `juju-controllerd` snap does not require its `snapcraft.yaml` to live in this repository.
Launchpad's current build system supports one snap per repository, and this repo already hosts
the `juju` CLI snap. Keeping the controller snap in a separate repository avoids that
constraint and allows independent Launchpad and store pipelines. The development path with
`--controller-snap` only requires a local `.snap` file, not a build system integration in this
repo. A final decision on repository structure is recorded in `spec.md`.

Notes:

- `juju-controllerd` is snap-managed from first successful bootstrap.
- `jujud` runs as machine-agent along with `juju-controllerd` and is not replaced by the snap.
- The controller charm understands that the bootstrap process for `controller/0` is different
  from subsequent units.
- `--build-agent` and `--controller-snap` are independent flags and can be combined: `--build-agent`
  rebuilds `jujud` from source; `--controller-snap` provides the pre-built controller snap.

### Controller scale process (HA)
1. Juju initiates controller scale-out with `juju add-unit` command.
2. `juju-controllerd` provisions a new machine and configures cloud-init.
3. Cloud-init starts `jujud` on the new machine.
4. `jujud` (via the uniter) determines that it must deploy the controller charm.
5. The charm determines that this unit is **not** `controller/0`, so downloads the controller snap from the object store.
6. The charm installs the snap, writes the agent configuration for `juju-controllerd`, and starts `juju-controllerd`.

### Controller upgrade process
1. Juju initiates the upgrade with `juju upgrade-controller`, which internally triggers a controller charm refresh as part of the workflow.
2. The controller charm downloads the target snap revision.
3. `juju-controllerd` stores the snap and assert in object store.
4. The controller charm installs the snap and restarts `juju-controllerd`.
5. After `juju-controllerd` is upgraded and restarted, the Juju client starts controller model upgrade using the existing model-upgrade flow.

Notes:
- Model upgrade process is not changed in this project, so details are not included here.
- Upgrade of the controller and the controller model are decoupled. Juju client triggers controller upgrade first, then triggers model upgrade after controller is upgraded and restarted. This way, we keep the two upgrade processes independent and avoid coupling model upgrade with controller upgrade. However, this also means Juju client needs to orchestrate the two upgrade processes together to keep them in sync.

## Decisions

**Service ownership split.** `juju-controllerd` carries controller services; `jujud` remains machine-agent-only. Workflow definitions must preserve this boundary.

**Client-Side Orchestration for UX Consistency**: While the controller and model upgrades are technically decoupled to prevent process interference, the Juju client will orchestrate both sequentially. This avoids forcing users to manually trigger two separate upgrades—preserving the existing "single-command" experience while benefiting from the stability of independent execution phases.

**Charm-centered lifecycle control**: After initial cloud-init bootstrap, steady state controller snap lifecycle (convergence and upgrades) is controlled by Juju workflows (charm/worker), not by jujud or juju-controllerd.

**Environment parity principle**: Production, development, and airgapped flows may differ in artifact source, but they must converge to the same runtime topology and upgrade authority model.

**Required Proxy Services**: Standardize on a dual-proxy configuration for air-gapped environments. Deployments in these environments must provide both a Snap Proxy and a SimpleStreams Proxy to facilitate secure, internal access to all necessary resources.

**Separate `--controller-snap` flag**: a new `--controller-snap=<path>` flag is introduced for the development workflow rather than extending `--build-agent`. `--build-agent` continues to handle only the `jujud` tarball. The controller snap is not built automatically by the client because the snapcraft build cost is too high for iterative development.

**snapcraft.yaml in a separate repository**: the controller snap's build configuration lives in a dedicated repository to avoid Launchpad's one-snap-per-repo constraint. This repo only consumes the resulting `.snap` file via the `--controller-snap` flag.

**Snap version resolution for production**: the snap store channel naming convention `juju-controllerd/<major>.<minor>/stable` maps Juju version numbers to snap revisions. The Juju client resolves the target version from simplestreams and constructs the matching channel name to pass to cloud-init.

**Snap auto-refresh held immediately**: after installation (by any path), `snap refresh --hold juju-controllerd` is executed immediately so that the upgrader worker and controller charm control all snap upgrades explicitly.

**Version coherence enforced before bootstrap worker starts**: cloud-init verifies that the installed snap version matches the simplestreams-resolved target version before signalling readiness to the bootstrap worker.

## Open Questions

1. During HA scale-out, what is the exact failure-handling policy when snap installation fails on a candidate controller unit?
2. How is the target snap revision identified during `juju upgrade-controller`? Does the charm query simplestreams for the target version and then resolve the corresponding channel, or does the client pass the resolved revision directly to the charm?
3. When `--controller-snap` is not provided and the feature flag is ON, should bootstrap fail with a clear error, or silently fall back to the production snap-store path even in a development build?

## Further Information
