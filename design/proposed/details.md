# Controller-as-a-snap rollout details

This document captures the updated rollout plan after confirming that running
two independent controller agents in parallel is not viable.

The plan is incremental and PR-friendly, with explicit feature-flag transition
steps before final cleanup.

## Stage summary

- **Stage 1 — Bootstrap (Steps 1–5):** This stage delivers controller bootstrap
on the new path: feature-flagged rollout, controller/machine agent coexistence,
dedicated controller snap adoption, production/dev/airgap source-resolution
behavior, and machine/controller binary split.

- **Stage 2 — Upgrade and HA (Steps 6–8):** This stage establishes HA scale-out
charm-driven for non-`controller/0` units with required checks, and controller
upgrade follows explicit snap-distribution sequencing.

- **Stage 3 — Release & Cleanup (Steps 9–10):** This stage finalizes rollout by
making the new path default, removing feature-flag branching, then performing
post-flag machine-binary cleanup (`jujud` without controller-only API
server/dqlite dependencies).

## Rollout stages

### Stage 1 — Bootstrap (Steps 1–5)

### Step 1 — Feature flag + install Juju snap on controller (do not start)

#### Delivered behavior

With feature flag ON, bootstrap installs the Juju snap package on controller
machines, but does not start an additional controller process yet.

Legacy runtime remains authoritative.

#### Tasks

1. Add/confirm feature flag guard for new bootstrap path.
2. Add bootstrap/cloud-init install logic for Juju snap on controller machine.
3. Ensure no new controller service is started in this step.
4. Preserve existing bootstrap path when flag is OFF.

#### Acceptance criteria

- Flag OFF: behavior unchanged.
- Flag ON: snap is present on controller machine after bootstrap.
- No additional controller process is started yet.

---

### Step 2 — Start machine agent and controller agent together (same binary)

#### Delivered behavior

Controller machine starts machine-agent and controller-agent processes together,
using the same underlying binary with different configuration/command roles.

Controller path becomes active without machine-agent restart dance.

#### Tasks

1. Wire startup and process supervision for concurrent machine/controller agent
   processes on controller machine.
2. Route controller bootstrap responsibilities to the controller-agent process.
3. Ensure controller charm creation is performed from controller-agent flow.
4. Ensure controller agent and machine agent have separate config/log files.
5. Add explicit readiness gate: cloud-init/bootstrap waits until
   `juju-controllerd` has started successfully.

#### Acceptance criteria

- Both agents are running concurrently on `controller/0`.
- Controller bootstrap/charm path is executed by controller-agent process.
- Machine agent does not restart/flip mode to become controller.
- Bootstrap fails clearly if `juju-controllerd` does not reach ready state.

---

### Step 3 — Introduce dedicated controller snap package (same binary)

#### Delivered behavior

Controller uses a new dedicated controller snap package instead of Juju snap,
while payload binary remains the same for now.

#### Tasks

1. Create and wire dedicated controller snap packaging/publishing flow.
2. Switch bootstrap install path from Juju snap to controller snap.
3. Keep runtime behavior from Step 2 unchanged.
4. Validate artifact naming/versioning/channel behavior for controller snap.

#### Acceptance criteria

- Controller machine installs controller snap (not Juju snap) in Stage path.
- Runtime behavior remains functionally identical to Step 2.

---

### Step 4 — Production/airgap source matrix + version resolution

#### Delivered behavior

Bootstrap resolves target version from SimpleStreams and acquires matching
controller snap via the correct source matrix (production/dev/airgap).

#### Tasks

1. Implement target version resolution from SimpleStreams for controller
   bootstrap path.
2. In production path, download corresponding controller snap revision from snap
   store.
3. In airgapped path, enforce/use dual-proxy model:
   - Snap Proxy for controller snap acquisition.
   - SimpleStreams Proxy for version and `jujud` artifacts.
4. Keep development path using locally provided artifacts.
5. Add validation that resolved version and acquired snap revision are coherent.

#### Acceptance criteria

- Bootstrap source selection works for production, development, and airgapped
  modes.
- Airgapped flow requires both Snap Proxy and SimpleStreams Proxy.
- Resolved version from SimpleStreams drives corresponding snap acquisition.

---

### Step 5 — Split binaries by snap role

#### Delivered behavior

Controller snap contains controller binary; Juju snap contains `jujud` machine
binary.

#### Tasks

1. Build and package distinct machine/controller binaries.
2. Ensure controller snap consumes controller binary artifact.
3. Ensure Juju snap/machine-agent path consumes `jujud` binary artifact.
4. Update CI/release wiring to publish and validate both artifacts.

#### Acceptance criteria

- Distinct binaries are produced and packaged in their intended snaps.
- Controller runtime uses controller binary from controller snap.
- Machine runtime uses `jujud` binary from Juju snap/machine path.

---

### Stage 2 — Upgrade and HA (Steps 6–8)

### Step 6 — Upload bootstrap artifacts to object store (snap + assert + tools)

#### Delivered behavior

Bootstrap path stores snap, assert, and tools artifacts in object store with
consistent metadata for later HA/upgrade use.

#### Tasks

1. Implement bootstrap object-store upload for controller snap artifact.
2. Implement bootstrap object-store upload for assertion artifact.
3. Implement bootstrap object-store upload for tools artifact.
4. Persist metadata needed for retrieval and validation of the artifact set.
5. Make storage writes idempotent and retry-safe.

#### Acceptance criteria

- Successful bootstrap stores snap + assert + tools as one coherent artifact
  set.
- Metadata is consistent, retrievable, and retry-safe.

---

### Step 7 — Charm changes for HA scale-out (+ bootstrap checks)

#### Delivered behavior

HA scale-out is supported through charm-driven controller snap lifecycle on new
controller units, with bootstrap and convergence checks aligned to
`workflow-spec.md`.

#### Tasks

1. Update charm workflow for non-`controller/0` units to install/start
   controller snap from object-store-provided artifacts.
2. Integrate object-store artifact retrieval into charm/unit lifecycle.
3. Add explicit `jujud`/uniter-driven decision path for deploying controller
   charm on new units.
4. Add/ensure bootstrap checks:
   - peer relations are verified,
   - installed `juju-controllerd` snap version matches charm version,
   - `controller/0` bootstrap path remains explicitly distinguished from others.
5. Add failure/recovery handling for scale-out convergence.

#### Acceptance criteria

- `juju add-unit` scale-out converges with controller snap lifecycle managed by
  charm.
- Non-`controller/0` path is driven by expected uniter/charm logic.
- Peer-relation and snap/charm version checks are enforced.
- Failed scale-out does not leave units in unrecoverable partial state.

---

### Step 8 — Upgrade flow via snap distribution (explicit sequence)

#### Delivered behavior

Controller upgrade uses snap-distribution workflow end-to-end, coordinated by
Juju/charm with object-store-backed artifact flow and explicit sequencing.

#### Tasks

1. Implement explicit sequence:
   1. `juju upgrade-controller` initiates upgrade and triggers charm refresh.
   2. Charm resolves/downloads target snap revision.
   3. `juju-controllerd` stores snap + assert in object store.
   4. Charm installs snap and restarts `juju-controllerd`.
   5. After controller restart/upgrade, Juju client initiates model upgrade.
2. Preserve decoupled controller-upgrade vs model-upgrade contract.
3. Add integration coverage for successful and failure upgrade paths.

#### Acceptance criteria

- Controller upgrade follows the defined sequence deterministically.
- Model upgrade is triggered only after controller upgrade/restart success.
- Upgrade behavior is observable and recoverable on failure.

---

### Stage 3 — Release & Cleanup (Steps 9–10)

### Step 9 — Switch default to new path and remove feature flag

#### Delivered behavior

The new snap-based controller path becomes default behavior, and feature-flag
branching is removed.

#### Tasks

1. Flip default behavior to the new controller-snap workflow.
2. Remove feature-flag checks and temporary dual-path branching.
3. Keep any required migration guards for existing deployed controllers.
4. Update documentation to reflect new default and migration notes.

#### Acceptance criteria

- New path is default without explicit flag enablement.
- Feature flag and major dual-path branches are removed.
- Existing supported upgrade/migration paths remain functional.

---

### Step 10 — Post-flag cleanup: remove API server and dqlite from `jujud`

#### Delivered behavior

After flag removal and default switch, machine binary (`jujud`) no longer
links/includes controller-only API server and dqlite responsibilities.

#### Tasks

1. Remove controller-only API server and dqlite dependencies from `jujud`.
2. Refactor imports/build wiring so machine path stays functional.
3. Add checks/tests to prevent regression of removed dependencies.

#### Acceptance criteria

- `jujud` builds and runs without API server/dqlite components.
- Controller functionality remains available through controller binary path.

## Notes on ordering

- Steps 1–3 establish packaging/runtime flow first.
- Step 4 establishes source/version parity across production/dev/airgap.
- Steps 5–8 complete binary split, storage, HA, and upgrade behavior while flag
  can still protect rollout.
- Step 9 removes the flag and makes new behavior default.
- Step 10 performs final machine-binary cleanup that is unsafe behind flag
  branching.
