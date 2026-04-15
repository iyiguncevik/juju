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

1. Introduce controller-snap-bootstrap feature flag
2. Upload snap to controller during bootstrap
3. Local Snap Installation
4. Bootstrap with latest controller snap 
5. Bootstrap with a snap channel
6. Bootstrap with a snap revision

#### Acceptance criteria

- Flag OFF: behavior unchanged.
- Flag ON: snap is present on controller machine after bootstrap.
- No additional controller process is started yet.

---

### Step 2 — Create controller snap
#### Delivered behavior
Controller snap is created in juju repo Makefile. 
cmd/jujud and cmd/jujud-controller are built separately, with the latter included in the controller snap → two binaries
--build-agent flag builds the controller snap with the controller binary

### Step 3 — Controller snap runs in parallel with legacy controller
jujud-controller only have controller workers
Machine agent workers talks to apiserver in the controller snap

### Step 4 — Remove controller workers from jujud
Put controller workers behind feature flag

### Step 5 — Publish controller snap
Controller snap is published to the snap store, and made available in the beta channel.
Bootstrap can be done with snaps from beta channel