# Details of controllers as a snap

## Current state
ey files involved:

**Current binary situation:**
- `jujud-controller` is built from `cmd/jujud-controller/`, **renamed to `jujud`** by Makefile (lines 318, 326, 391)
- Both `cmd/jujud` and `cmd/jujud-controller` link against the same packages (including dqlite, domain services)
- Binary is uploaded to S3 (`s3://juju-qa-data/{STAGING_ROOT}/build-{COMMIT}/agents/`)
- Snap build downloads binary from S3, packages it
- OCI build copies binary from `_build/` directory
- **Result: Same binary everywhere today** (S3 → Snap → OCI have identical hashes)

**Current HA flow:**
1. `juju add-unit` adds a new machine to controller cluster
2. Machine starts as machine agent (runs `jujud`)
3. Machine agent detects it should be a controller (checks dqlite cluster config)
4. Agent restarts itself — **same binary, different command** (`jujud` vs `jujud-controller`)
5. Controller charm is deployed

| File                                              | Current role                                                                                         |
| ------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `cmd/jujud-controller/`                           | Controller binary entry point — already separate                                                     |
| `cmd/jujud/`                                      | Machine/unit agent binary — currently includes controller code paths                                 |
| `snap/snapcraft.yaml`                             | Builds `jujud-controller` snap (core24, musl, static dqlite libs from S3)                            |
| `caas/Dockerfile`                                 | Builds OCI image by copying pre-built binaries from `_build/` directory                              |
| `Makefile`                                        | Uses `musl-gcc`, `CGO_ENABLED=1`, `-extldflags '-static'`; downloads static libs from S3             |
| `internal/cloudconfig/userdatacfg.go`             | `installSnap()` calls `sudo snap install --dangerous`; `addDownloadToolsCmds()` curls `tools.tar.gz` |
| `internal/bootstrap/agentbinary.go`               | `PopulateAgentBinary()` reads `tools.tar.gz` from disk, uploads to object store                      |
| `internal/worker/upgrader/upgrader.go`            | Downloads `tools.tar.gz` via HTTP from controller API                                                |
| `domain/agentbinary/service/simplestreamstore.go` | Falls back to simplestreams for binary discovery                                                     |
| `core/snap/assertions.go`                         | `LookupAssertions()` already exists — fetches assertions from snap store proxy                       |
| `internal/packaging/manager/snap.go`              | `Snap.Install()`, `Snap.ChangeChannel()` — snap manager exists                                       |
| `apiserver/tools.go`                              | HTTP handler serving agent binaries from object store or simplestreams                               |


## Stage Details

### Stage 1 — Snap Infrastructure & Dev Workflow (Single Controller)

#### Outcome

The dedicated repository `github.com/juju/jujud-controller-snap` is set up with CI that
builds and publishes the `jujud-controller` snap to the snap store. The `ControllerSnap`
feature flag exists and developers can bootstrap/upgrade **single-node controllers** using
locally built `.snap` files without needing snap store access. This enables the team to
iterate on the snap install/upgrade flow. **HA is explicitly not supported yet** — that's Stage 4.
The existing build system (musl, S3, Dockerfile, single binary with rename) is unchanged.

#### Tasks

##### 1.1 — Create `github.com/juju/jujud-controller-snap`
- Launchpad already publishes the `juju` CLI snap from this repository and cannot publish
  a second snap from the same repo due to infrastructure constraints
- Create **`github.com/juju/jujud-controller-snap`** as a dedicated repository:
  - Contains `snapcraft.yaml` (migrated from `snap/snapcraft.yaml` in this repo)
  - Contains GH Actions CI for building and publishing the snap to the snap store
  - Downloads `jujud` binary from S3 during snap build (renamed `jujud-controller` as today)
- Remove `snap/snapcraft.yaml` from this repository once the new repo is set up
  (the `snap/` directory stays only for the CLI snap built on LP)
- All snap build and publish runs originate from `github.com/juju/jujud-controller-snap`

##### 1.2 — Feature flag
- Add `ControllerSnap` constant in `internal/featureflag/`
- All snap-specific runtime behaviour is gated on `featureflag.ControllerSnap`
- Flag is `false` by default in Stage 1–Stage 4 releases
- Document the flag in `docs/reference/`

##### 1.3 — `juju bootstrap` accepts a local snap file (dev workflow)
- Add `--controller-snap=<path>` option to the bootstrap command (feature-flagged)
- In `internal/cloudconfig/userdatacfg.go`, add `addLocalSnapInstallCmds()` (flag ON):
  1. Upload the local `.snap` file to the instance via `AddRunBinaryFile`
  2. `snap install --dangerous ./<snap>`
  3. `snap refresh --hold jujud-controller`
- Keep existing `tools.tar.gz` path when flag is OFF
- **Explicitly reject bootstrap with `--to` or HA flags when using controller snap** — HA not yet supported

##### 1.4 — Store snap in dqlite object store from local file
- In `internal/bootstrap/agentbinary.go`, add `PopulateSnapAgentBinary()` (flag ON):
  1. Read `.snap` (and optional `.assert`) files from disk after install
  2. Store both blobs in the object store:
     `agent-binaries/<version>-<arch>.snap` and `agent-binaries/<version>-<arch>.assert`
  3. Record metadata (version, arch, snap revision, assert SHA256) in `agent_binary` table
  4. Return cleanup func that removes files from disk after upload
- Extend `domain/agentbinary/` DDL: add `snap_revision` and `assert_object_path` columns
- Extend `AgentBinaryStore` interface with `AddSnapAgentBinary()` / `GetSnapAgentBinary()`

##### 1.5 — Bootstrap worker dispatch (IAAS path)
- `internal/worker/bootstrap/worker.go`: `seedAgentBinary()` dispatches to
  `PopulateSnapAgentBinary` when flag is ON

##### 1.6 — `juju upgrade-controller` accepts a local snap file (dev workflow)
- Add `--controller-snap=<path>` and `--controller-assert=<path>` options
- Client uploads the local snap+assert pair via API to the controller's object store
- Add API facade method to accept snap+assert blobs and store them
- Upgrader worker on each machine (flag ON):
  1. Fetches `.snap` + `.assert` from object store via controller API
  2. `snap ack <assert-file>`
  3. `snap install ./<snap-file>`
  4. `snap refresh --hold jujud-controller`
  5. Exits with `UpgradeReadyError` (existing restart mechanism)

##### 1.7 — Single-controller validation
- Integration test: bootstrap single LXD controller with `--controller-snap`, verify works
- Integration test: upgrade single controller with `--controller-snap`, verify works
- Explicitly test that `juju enable-ha` is rejected when controller snap is in use

---

### Stage 2 — IAAS Production: Snap Store (Single Controller)

#### Outcome

IAAS controllers bootstrap and upgrade from the snap store using the fully correct install
flow: `snap download` → `snap ack` → `snap install ./` → `snap refresh --hold`. Airgap
deployment works via the existing snap store proxy config. Simplestreams is bypassed for
agent binary discovery. **HA is still not supported** — that's Stage 4. All behind the
`ControllerSnap` feature flag.

#### Tasks

##### 2.1 — `snap download` flow in cloud-init (flag ON, production path)
In `internal/cloudconfig/userdatacfg.go`:
- Add `addSnapStoreCmds()` (flag ON, no local snap file provided):
  1. `snap download jujud-controller --channel=<channel> --target-directory=$bin`
  2. `snap ack $bin/jujud-controller_<rev>.assert`
  3. `snap install $bin/jujud-controller_<rev>.snap`
  4. `snap refresh --hold jujud-controller`
- When `SnapStoreProxyURL` / `SnapStoreProxyID` are set, pass `--store` to `snap download`
- Remove `sudo snap install --dangerous` from the production path entirely

##### 2.2 — Airgap bootstrap (snap proxy or pre-seeded object store)
- When `SnapStoreProxyURL` is set: `snap download` routes through the proxy
- When fully airgapped: snap+assert files provided via `--controller-snap` (from Stage 1)
- The existing `SnapStoreAssertions` / `SnapStoreProxyURL` fields in `instancecfg.go`
  (lines 165–178) are already wired

##### 2.3 — Connected upgrade: controller downloads new snap from snap store
- When `juju upgrade-controller` is run without a local snap file (flag ON):
  1. Controller runs `snap download jujud-controller --channel=<channel>`
  2. Stores snap+assert in object store
  3. Signals upgrader workers
- Reuses `core/snap/assertions.go` `LookupAssertions()` for assertion verification

##### 2.4 — Serve snap+assert blobs from API
- Extend `apiserver/tools.go` to serve:
  `GET /model/{uuid}/tools/{version}/snap` and `.../assert`
- Reads from `AgentBinaryStore.GetSnapAgentBinary()`

##### 2.5 — Bypass simplestreams for agent binary discovery (flag ON)
- In `domain/agentbinary/service/service.go`: when flag is ON, skip
  `SimpleStreamsAgentBinaryStore` fallback entirely
- `simplestreamstore.go` is not deleted yet — just not invoked
- Simplestreams stays active for cloud image metadata (LXD image discovery)

##### 2.6 — Single-controller production validation
- Integration test: bootstrap single LXD controller from snap store, verify works
- Integration test: upgrade single controller from snap store, verify works
- Integration test: airgap bootstrap with snap proxy, verify works
- Explicitly verify that `juju enable-ha` is still rejected

---

### Stage 3 — Binary Separation + HA

#### Outcome

The `jujud-controller` and `jujud` binaries are now truly separate — `jujud` no longer
links dqlite or domain services. **HA is now supported** with the snap path: when
`juju enable-ha` is run, the new machine agent detects it should be a controller, installs
the `jujud-controller` snap, and transitions to controller role. This milestone couples
binary separation with HA snap installation logic because they are tightly related.

#### Tasks

##### 3.1 — Binary separation: Audit controller-only dependencies
- Identify all packages that are controller-specific:
  - `internal/dqlite`, `github.com/canonical/go-dqlite`, `raft`, etc.
  - `domain/*` packages (domain services are controller-only)
  - Controller-only workers (e.g., `bootstrap`, `dbaccessor`, `dblogpruner`)
- Document which packages should be linked into `jujud-controller` only vs. shared

##### 3.2 — Binary separation: Refactor `cmd/jujud/` to exclude controller code
- Ensure `cmd/jujud/main.go` does not import any controller-only packages
- Move shared agent logic to a common package if needed
- `cmd/jujud/` should only import:
  - Core agent framework
  - API client packages
  - Workers that run on machine/unit agents (not controller-only workers)
- Note: `cmd/jujud/run.go` already registers different commands than `cmd/jujud-controller/run.go`

##### 3.3 — Binary separation: Stop renaming binaries in Makefile
- **Remove** the rename logic (Makefile lines 318, 326, 391)
- `jujud-controller` keeps its name
- `jujud` keeps its name
- Both binaries are produced in `_build/`
- Update agent tarball creation to include both binaries (or separate tarballs)

##### 3.4 — Binary separation: Update CI to handle two binaries
- Update `juju-release-jenkins` jobs to upload both binaries to S3:
  - `juju-{version}-{os}-{arch}.tgz` contains `jujud` (for machine/unit agents)
  - `jujud-controller-{version}-{os}-{arch}.tgz` contains `jujud-controller` (for controllers)
- Update S3 paths and tarball structure

##### 3.5 — Binary separation: Update simplestreams
- Add `jujud-controller` binary to simplestreams metadata generation
- When flag is OFF, controllers still download via simplestreams
- When flag is ON, controllers use snap (simplestreams bypassed)

##### 3.6 — Binary separation: Update snap to package `jujud-controller`
- Update `github.com/juju/jujud-controller-snap` to download `jujud-controller-{version}.tgz` from S3
- Snap now packages the `jujud-controller` binary (not renamed `jujud`)

##### 3.7 — Binary separation: Verify
- Run `ldd jujud` and `ldd jujud-controller` to verify:
  - `jujud-controller` links `libdqlite`, `libraft`, `libuv`
  - `jujud` does **not** link these libraries
- Verify binary sizes: `jujud` should be significantly smaller

##### 3.8 — HA: Machine agent snap installation logic
**Problem:** When `juju enable-ha` adds a machine, it starts as a machine agent (`jujud`).
How does it transition to controller (`jujud-controller` snap)?

**Solution:** Machine agent detects controller role and installs snap
- In machine agent startup (`cmd/jujud/agent/machine.go` or worker manifold):
  1. Check if this machine should be a controller (query API or check local config)
  2. If yes and `ControllerSnap` flag is ON:
     - Download snap+assert from controller object store (via API)
     - `snap ack <assert-file>`
     - `snap install ./<snap-file>`
     - `snap refresh --hold jujud-controller`
     - Exit with a new `TransitionToControllerError`
  3. Systemd or upstart restarts the agent, which now runs from snap
- Controller charm is deployed as before

**Implementation:**
- Add new worker or modify existing `machineagent` startup to detect controller role
- Add API endpoint to download snap+assert for HA transition
- Add `TransitionToControllerError` to signal restart needed
- Update systemd/upstart scripts to handle the transition

##### 3.9 — HA: Object store replication
- Snap+assert blobs stored in dqlite object store → automatically replicated via raft
- When HA unit 2/3 starts, it fetches snap+assert from object store (already replicated)

##### 3.10 — HA: Integration tests
- **Bootstrap HA test**: `juju bootstrap --to lxd/0,lxd/1,lxd/2` with snap, verify all units
  install snap and become controllers
- **Enable HA test**: bootstrap single controller with snap, then `juju enable-ha 3`, verify
  new units install snap and transition correctly
- **HA upgrade test**: bootstrap 3-unit HA, upgrade controller, verify all units upgrade snap
- **Binary separation test**: verify machine agents run `jujud`, controllers run `jujud-controller`

---

### Stage 4 — CAAS + Binary Distribution Strategy

#### Outcome

The CAAS controller OCI image is updated to use the renamed `jujud-controller` binary
(following the binary separation in Stage 3). **Critical: The snap and OCI image use the same
binary with identical hash.** This is achieved by establishing a centralized binary
distribution mechanism where both the snap build and OCI build download the same pre-built
binary from S3. The existing `caas/Dockerfile` is updated rather than replaced.

#### Tasks

##### 4.1 — Establish binary distribution strategy
**Problem:** After moving snap to a separate repo, how do we ensure snap and OCI use the
**same** binary with identical hash?

**Solution:** Centralized binary build + distribution via S3
1. Main repo CI builds `jujud-controller` binary (as today, now with its proper name from Stage 3)
2. Binary uploaded to S3: `s3://juju-qa-data/{STAGING_ROOT}/build-{COMMIT}/agents/juju-{version}-{os}-{arch}.tgz`
3. `github.com/juju/jujud-controller-snap` CI downloads binary from S3, packages into snap
4. `caas/Dockerfile` downloads binary from S3, packages into OCI image
5. Both snap and OCI contain the **same binary with identical SHA256 hash**

**Implementation:**
- Verify `juju-release-jenkins` already uploads binary to S3 (it does)
- Update `github.com/juju/jujud-controller-snap` `snapcraft.yaml`:
  - Add part that downloads binary from S3 during snap build
  - Use `plugin: dump` + `source: https://...` or custom build script
- Document S3 URL structure and access requirements

##### 4.2 — Update `caas/Dockerfile`
- Update `caas/Dockerfile` to reference the renamed `jujud-controller` binary from Stage 3
- `jujud-controller` binary is downloaded from S3 (same source as snap)
- Both OCI image and snap are built from the same Git ref and **contain the same binary**

##### 4.3 — Verify binary hash identity
- Add CI check: compare SHA256 of `jujud-controller` binary in snap vs. OCI image
- Fail the build if hashes differ
- Add integration test: extract binary from snap and OCI, compare hashes

##### 4.4 — CAAS integration tests
- Verify CAAS bootstrap continues to work with the updated OCI image
- Verify `ControllerSnap` flag has no effect on CAAS code paths
- Verify binary hash matches snap binary

---

### Stage 5 — Flag Default ON & Full Integration Tests

#### Outcome

All new IAAS installations use the snap path by default. Existing installations continue
on the legacy binary path until they upgrade. A comprehensive integration test suite
validates snap bootstrap, upgrade, HA, airgap, binary separation, and binary hash identity.

#### Tasks

##### 5.1 — Flip `ControllerSnap` flag default to `true`
- Change the default value of `featureflag.ControllerSnap` to `true`
- The legacy `tools.tar.gz` path is now behind `!featureflag.ControllerSnap`
- Document migration: existing controllers keep binary-based upgrades until they run
  `juju upgrade-controller` to this version

##### 5.2 — Integration test suite: `tests/suites/controller_snap/`
- **Bootstrap test**: bootstrap LXD controller with snap; verify `snap list` shows
  `jujud-controller`; verify snap is held (`snap refresh --list`)
- **Upgrade test**: bootstrap at N-1 with snap, upgrade to N, verify snap revision changed
- **HA bootstrap test**: bootstrap 3-unit HA; verify snap on all units
- **HA enable test**: bootstrap single, enable HA, verify new units install snap
- **HA upgrade test**: bootstrap 3-unit HA, upgrade, verify all units upgrade snap
- **Airgap test**: bootstrap with `SnapStoreProxyURL`, verify no direct snap store access
- **Binary separation test**: verify machine agents run `jujud` (no dqlite), controllers
  run `jujud-controller` (from snap, with dqlite)
- **Binary hash identity test**: extract `jujud-controller` from snap and OCI, compare hashes

##### 5.3 — CI validation
- Snap build in `github.com/juju/jujud-controller-snap` produces artifact for integration tests
- OCI image build passes and produces multi-arch image
- Verify both builds download the same binary from S3 (hash check in CI)

##### 5.4 — Upgrade path documentation
- Update `docs/howto/` for snap-based controller operations
- Update `docs/reference/` for `--controller-snap` and `upload-agent-binary --snap`
- Document binary separation: controller agents use snap, machine/unit agents use `jujud`
- Document binary hash identity: snap and OCI use the same binary
- Document HA transition: machine agents install snap to become controllers

---

### Stage 6 — Legacy Removal

#### Outcome

The `ControllerSnap` feature flag is removed. Only the snap path remains for IAAS controllers.
All `tools.tar.gz` distribution code for **controllers**, simplestreams **controller** binary
infrastructure, musl build references, and `_deps/` S3 static library downloads
are deleted. S3 release artifacts (`jujud-controller`, `jujud`, `jujuc`, etc.) are
**not** removed — they are used by `juju-release-jenkins` and remain in S3.
Machine/unit agents still use `tools.tar.gz` and simplestreams (not affected by this project).

#### Tasks

##### 6.1 — Remove musl-gcc and `_deps/` static library downloads
- Remove `musl-install-if-missing` prerequisite from all `Makefile` targets
- Remove S3 download logic from `scripts/dqlite/scripts/dqlite/dqlite-install.sh` that
  populates the `_deps/` folder with pre-built static C libraries (dqlite, raft, libuv)
- Use `apt`-provided `libdqlite-dev` / `libraft-dev` / `libuv1-dev` everywhere
- Remove `musl-compat` part from any remaining snap build references
- Update `Makefile`: replace `CC="musl-gcc"` with system gcc; drop `-extldflags '-static'`
  from controller build
- The controller binary becomes dynamically linked
- **Note:** S3 release artifacts (final built binaries: `jujud-controller`, `jujud`, `jujuc`,
  etc.) are **not** affected — they remain uploaded to S3 by `juju-release-jenkins` as today

##### 6.2 — Delete legacy controller tools.tar.gz distribution code
- `internal/cloudconfig/userdatacfg.go`: remove `addDownloadToolsCmds()` for controllers
  (keep for machine agents), remove legacy `installSnap()`
- `internal/bootstrap/agentbinary.go`: remove the `tools.tar.gz` variant of
  `PopulateAgentBinary()`; rename `PopulateSnapAgentBinary()` to `PopulateAgentBinary()`
- `internal/worker/upgrader/upgrader.go`: remove tools.tar.gz download branch for controllers
- Note: Machine/unit agents still use tools.tar.gz — only remove controller-specific paths

##### 6.3 — Remove or replace `caas/Dockerfile`
- If Rockcraft migration has been completed (see Open Questions), remove `caas/Dockerfile`
- Otherwise, `caas/Dockerfile` remains as the sole CAAS image build path — no action needed

##### 6.4 — Remove simplestreams from controller agent binary service
- Delete `domain/agentbinary/service/simplestreamstore.go`
- Delete associated mock and test files
- Remove simplestreams lookups for **controller** agent binaries
- Note: `environs/simplestreams` package itself stays — still used for:
  - Cloud image metadata (LXD image discovery)
  - Machine/unit agent binaries (still use simplestreams — out of scope for this project)

##### 6.5 — Remove `ControllerSnap` feature flag
- Delete `featureflag.ControllerSnap`
- Remove all `if featureflag.ControllerSnap` / `if !featureflag.ControllerSnap` branches
- Delete flag documentation

##### 6.6 — Snap hook cleanup
- Remove `snap/local/wrappers/fetch-oci` if no longer needed

##### 6.7 — Final documentation pass
- Update `internal/bootstrap/` `doc.go` to reflect snap-only bootstrap for controllers
- Update CONTRIBUTING.md: no more musl; use `apt install libdqlite-dev` for development
- Update `docs/explanation/` architecture docs
- Document final binary distribution model:
  - IAAS controller agents: `jujud-controller` snap (downloads binary from S3)
  - Machine/unit agents: `jujud` binary (via tools.tar.gz or charm payload)
  - CAAS: OCI image via Dockerfile (or Rockcraft if migrated; see Open Questions)
  - Snap and OCI have identical `jujud-controller` binary hash


