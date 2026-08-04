# KubeVirt Bazel Removal — Design Document

**Status:** Draft
**Author:** Vamsi Krishna Siddu
**Related:** [VEP-392](https://github.com/kubevirt/enhancements/pull/393), [kubevirt#14038](https://github.com/kubevirt/kubevirt/issues/14038)

---

## 1. Overview

This document describes the design for removing Bazel from the KubeVirt build system and replacing it with standard Go tooling (`go build` / `go test`) and Containerfile-based image builds.

The migration follows a **parallel-flow strategy**: the new build pipeline runs alongside the existing Bazel pipeline with no impact on the current workflow. New Prow jobs are introduced as non-blocking, and only promoted to blocking once stabilized.

---

## 2. Design Principles

- **Zero disruption** — The existing Bazel-based flow continues to work unchanged throughout the migration. Both flows coexist independently.
- **Incremental migration** — Each area (images, compilation, tests, caching, cross-compilation) is migrated separately and validated before proceeding.
- **No feature flags** — The two flows are separate codepaths, not conditional branches behind a flag.
- **Makefile interface preserved** — Developers continue using the same Make targets; only the underlying implementation changes.

---

## 3. Parallel Build Flows

The core of the migration strategy is maintaining two independent flows:

```
                         ┌──────────────────────────────────────────────────────┐
                         │                    PR Opened                        │
                         └──────────────┬───────────────────┬───────────────────┘
                                        │                   │
                    ┌───────────────────▼──┐          ┌─────▼─────────────────┐
                    │   Bazel Flow         │          │  Containerfile Flow   │
                    │   (existing)         │          │  (new)                │
                    │   ── BLOCKING ──     │          │  ── NON-BLOCKING ──   │
                    └───────────┬──────────┘          └─────┬─────────────────┘
                                │                           │
                    ┌───────────▼──────────┐  ┌─────────────▼─────────────────┐
                    │  bazel build         │  │  go build                     │
                    │  bazel test          │  │  go test                      │
                    │  bazel images        │  │  containerfile images         │
                    │  e2e tests           │  │  e2e tests                    │
                    └───────────┬──────────┘  └─────────────┬─────────────────┘
                                │                           │
                    ┌───────────▼──────────┐  ┌─────────────▼─────────────────┐
                    │  Merge gate:         │  │  Merge gate:                  │
                    │  BLOCKS merge        │  │  Does NOT block merge         │
                    └──────────────────────┘  └───────────────────────────────┘
```

Once the Containerfile flow is stabilized:
1. Promote Containerfile jobs to **blocking**.
2. Run both as blocking for a validation period.
3. Retire Bazel jobs.

---

## 4. CI Strategy — Prow Job Transition

New Prow jobs for the Containerfile-based pipeline are created and run on every PR alongside the existing Bazel jobs. These new jobs are **non-blocking** — they do not affect mergeability.

### 4.1 Transition Stages

| Stage | Bazel Jobs | Containerfile Jobs | Merge Gate |
|-------|------------|-------------------|------------|
| **1. Introduction** | Blocking (unchanged) | Non-blocking (new) | Bazel only |
| **2. Stabilization** | Blocking | Non-blocking (monitoring) | Bazel only |
| **3. Validation** | Blocking | Blocking (promoted) | Both |
| **4. Cutover** | Removed | Blocking (sole) | Containerfile only |

### 4.2 Promotion Criteria

A Containerfile job is promoted to blocking only when:
- It has been passing consistently over a sufficient observation period.
- The full e2e test suite passes against images built by the new flow.
- Build times are within acceptable range (with caching enabled).
- All supported architectures (amd64, arm64, s390x) build successfully.

---

## 5. RPM Dependency Management

**Current approach:** Using `bazeldnf` as a standalone CLI tool — invoked directly without Bazel.

**Status:** Open for feedback. Not yet fully decided.

### 5.1 Why bazeldnf Standalone

- Provides deterministic RPM resolution with version pinning.
- Supports local mirroring for reproducible builds.
- Using `dnf` / `microdnf` directly in Containerfiles was considered but rejected — RPM versions and transitive dependencies can change between builds, breaking reproducibility.

### 5.2 RPM Dependency Flow

```
  ┌──────────────┐       ┌──────────────────┐       ┌───────────────┐       ┌──────────────────┐
  │  RPM repos   │──────▶│  bazeldnf CLI    │──────▶│  Pinned RPMs  │──────▶│  Containerfile   │
  │  (CentOS     │       │  (standalone,    │       │  (tar archive, │       │  (COPY + install │
  │   Stream)    │       │   no Bazel)      │       │   locked deps) │       │   from archive)  │
  └──────────────┘       └──────────────────┘       └───────────────┘       └──────────────────┘
```

### 5.3 RPM Base Image Layer

RPM dependencies are decoupled from component image builds into separate base images.
Base images are built using standalone `bazeldnf rpm2tar` — no Bazel invocation required:

```
┌──────────────────────────────────────────────────────────┐
│  hack/rpm-deps.sh (standalone bazeldnf)                  │
│  → Resolves RPM dependencies                             │
│  → Updates pinned dependency files                       │
└──────────────────────────┬───────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────┐
│  hack/rpm-base-images/generate-rpm-tars.sh               │
│  → Parses rpmtree rules                                  │
│  → Downloads RPMs using pinned URLs                      │
│  → Runs bazeldnf rpm2tar with --symlinks/--capabilities  │
│  → Outputs tars to _out/rpm-tars/                        │
└──────────────────────────┬───────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────┐
│  hack/rpm-base-images/build-base-images.sh               │
│  → Runs generate-rpm-tars.sh for each rpmtree target     │
│  → Builds Containerfiles (FROM scratch + ADD tar)        │
│  → Tags as quay.io/kubevirt/<name>:<version>             │
├──────────────────────────────────────────────────────────┤
│  launcherbase │ handlerbase │ exportserverbase │ pr-helper│
│  libguestfs-  │ sidecar-   │ testimage        │ libvirt- │
│  tools        │ shim       │                  │ devel    │
└──────────────┬───────────────────────────────────────────┘
               │
               ▼ (used as FROM in component Containerfiles)
┌──────────────────────────────────────────────────────────┐
│         Component Images (Containerfiles)                │
│  virt-operator, virt-api, virt-controller,               │
│  virt-handler, virt-launcher, etc.                       │
└──────────────────────────────────────────────────────────┘
```

**When to rebuild base images:**
- When RPM dependency files or repo configs change
- Manually via `make rpm-base-build && make rpm-base-push`

**Multi-arch support:**
- Base images are built for `amd64`, `arm64`, `s390x`
- Component images are built per target architecture

---

## 6. Unit Test Migration

Unit test migration from Bazel (`go_test` targets) to standard `go test` has started.

### 6.1 Approach

Following the Kubernetes test-infra approach from [kubernetes/test-infra#16623](https://github.com/kubernetes/test-infra/pull/16623/changes):

1. Run `go test` as the standard test invocation in CI.
2. Back up the Go test cache (`GOCACHE`) to a shared storage location (e.g., GCS) after each run.
3. Restore the cache at the start of subsequent CI runs for incremental test execution.

### 6.2 CI Test Caching Flow

```
  ┌─────────────┐      ┌─────────────────┐      ┌──────────────┐      ┌─────────────────┐
  │ CI Job      │─────▶│ Restore Cache   │─────▶│ go test      │─────▶│ Save Cache      │
  │ Start       │      │ (GOCACHE from   │      │ (uses cached │      │ (GOCACHE to     │
  │             │      │  GCS/artifact   │      │  results)    │      │  GCS/artifact   │
  │             │      │  storage)       │      │              │      │  storage)       │
  └─────────────┘      └─────────────────┘      └──────────────┘      └─────────────────┘
```

### 6.3 Reference

The Kubernetes approach runs two periodic jobs:
- **Cache generator** (hourly): Runs `make test`, archives `GOCACHE` to GCS.
- **Cached test runner** (every 15 min): Restores cache from GCS, runs `make test` with warm cache.

This pattern is being adapted for KubeVirt's CI infrastructure.

---

## 7. Caching Strategy

**Status:** Research in progress.

Bazel provides built-in caching for builds and tests. Replacing this capability is critical for keeping CI times acceptable.

### 7.1 Go Build Caching (GCS-based, same as k/k approach)

The same Kubernetes technique used for test caching (Section 6) applies to `go build` as well. The Go compiler's `GOCACHE` stores both build and test artifacts — backing it up to GCS and restoring it gives incremental compilation across CI runs.

```
  ┌─────────────┐      ┌────────────────────┐      ┌──────────────┐      ┌────────────────────┐
  │ CI Build    │─────▶│ Restore Cache      │─────▶│ go build     │─────▶│ Save Cache         │
  │ Job Start   │      │ (GOCACHE +         │      │ (incremental │      │ (GOCACHE +         │
  │             │      │  GOMODCACHE from   │      │  compilation │      │  GOMODCACHE to     │
  │             │      │  GCS bucket)       │      │  using cache)│      │  GCS bucket)       │
  └─────────────┘      └────────────────────┘      └──────────────┘      └────────────────────┘
```

This mirrors the [kubernetes/test-infra#16623](https://github.com/kubernetes/test-infra/pull/16623/changes) pattern:
- A **periodic cache generator job** runs `go build` / `go test` on the main branch and archives `GOCACHE` to a GCS bucket.
- **PR jobs** restore that cache before building, so only changed packages are recompiled.
- `GOMODCACHE` is also cached to avoid re-downloading module dependencies on every run.

### 7.2 All Cache Layers

| Cache Layer | Mechanism | Notes |
|-------------|-----------|-------|
| **Go build cache** | Back up / restore `GOCACHE` to GCS (k/k approach) | Incremental compilation across CI runs |
| **Go module cache** | Back up / restore `GOMODCACHE` to GCS | Avoid re-downloading dependencies |
| **Go test cache** | `go test` result caching via `GOCACHE` (k/k approach) | Skip re-running unchanged tests |
| **Container layer cache** | Registry-based or local layer caching | Reuse unchanged image layers |
| **Base image cache** | Pre-built base images in registry | Versioned independently, pulled as-is |

### 7.3 Current Observation

Without caching, the initial Containerfile-based build takes roughly **2x** the current Bazel build time. The GCS-based caching strategy is aimed at closing this gap. Measurements with caching are pending.

---

## 8. Cross-Compilation

**Status:** Research in progress.

### 8.1 Supported Architectures

| Architecture | Status |
|-------------|--------|
| amd64 | Primary target |
| arm64 | Supported |
| s390x | Supported (Bazel lacks native support — one of the motivations for removal) |

### 8.2 Components Requiring Special Handling

Not all KubeVirt components are pure Go. The following require CGO or C toolchains:

| Component | Language | Requirement |
|-----------|----------|-------------|
| `virt-launcher` | Go + CGO | Links to `libvirt` — requires CGO enabled and cross-compilation toolchain |
| `node-labeller` (kvm-caps-info) | Go + CGO | KVM capability detection — amd64 only |
| `container-disk-v2alpha` | Pure C | Needs a C cross-compiler toolchain for multi-arch builds |

### 8.3 Approaches Under Consideration

- **Pure Go binaries**: `GOOS=linux GOARCH=<target> go build` — straightforward.
- **CGO binaries**: Cross-compilation with appropriate C toolchain (e.g., gcc-aarch64-linux-gnu for arm64).
- **Pure C binaries**: C cross-compiler in the build container.
- **Multi-arch container images**: `podman build --platform` or `buildx` for multi-arch image builds and manifests.

---

## 9. Image Inventory

All images currently produced by the Bazel flow are covered by the Containerfile flow.

### 9.1 Base Images

Built via `hack/rpm-base-images/build-base-images.sh`:

| Image | Containerfile | Architectures |
|-------|--------------|---------------|
| launcherbase | `hack/rpm-base-images/Containerfile.launcherbase` | amd64, arm64, s390x |
| handlerbase | `hack/rpm-base-images/Containerfile.handlerbase` | amd64, arm64, s390x |
| exportserverbase | `hack/rpm-base-images/Containerfile.exportserverbase` | amd64, arm64, s390x |
| libvirt-devel | `hack/rpm-base-images/Containerfile.libvirt-devel` | amd64, arm64, s390x |
| sidecar-shim | `hack/rpm-base-images/Containerfile.sidecar-shim` | amd64, arm64, s390x |
| testimage | `hack/rpm-base-images/Containerfile.testimage` | amd64, arm64, s390x |
| pr-helper | `hack/rpm-base-images/Containerfile.pr-helper` | amd64, arm64 |
| libguestfs-tools | `hack/rpm-base-images/Containerfile.libguestfs-tools` | amd64, s390x |

### 9.2 Component Images

Built via `hack/build-images-container.sh`:

| Image | Containerfile |
|-------|--------------|
| virt-operator | `cmd/virt-operator/Containerfile` |
| virt-api | `cmd/virt-api/Containerfile` |
| virt-controller | `cmd/virt-controller/Containerfile` |
| virt-handler | `cmd/virt-handler/Containerfile` |
| virt-launcher | `cmd/virt-launcher/Containerfile` |
| virt-exportserver | `cmd/virt-exportserver/Containerfile` |
| virt-exportproxy | `cmd/virt-exportproxy/Containerfile` |
| virt-synchronization-controller | `cmd/synchronization-controller/Containerfile` |
| conformance | `tests/conformance/Containerfile` |
| sidecar-shim | `cmd/sidecars/Containerfile` |
| example-hook-sidecar | `cmd/sidecars/smbios/Containerfile` |
| example-disk-mutation-hook-sidecar | `cmd/sidecars/disk-mutation/Containerfile` |
| example-cloudinit-hook-sidecar | `cmd/sidecars/cloudinit/Containerfile` |
| example-node-hook-plugin | `cmd/example-node-hook-plugin/Containerfile` |
| test-domain-hook-sidecar | `cmd/plugin-sidecars/test-domain-hook/Containerfile` |
| test-helpers | `cmd/test-helpers/pod-mutator/Containerfile` |
| network-slirp-binding | `cmd/sidecars/network-slirp-binding/Containerfile` |
| network-passt-binding | `cmd/sidecars/network-passt-binding/Containerfile` |
| network-passt-binding-cni | `cmd/cniplugins/passt-binding/cmd/Containerfile` |
| pr-helper | `cmd/pr-helper/Containerfile` |
| libguestfs-tools | `cmd/libguestfs/Containerfile` |
| vm-killer | `images/vm-killer/Containerfile` |
| disks-images-provider | `images/disks-images-provider/Containerfile` |
| winrmcli | `images/winrmcli/Containerfile` |
| Container disk images | `hack/build-container-disks.sh` |

---

## 10. Implementation Details

### 10.1 RPM Dependency Resolution (`hack/rpm-deps.sh`)

RPM dependencies are managed using standalone `bazeldnf` (installed via `hack/install-bazeldnf.sh`):

```bash
source hack/install-bazeldnf.sh

bazeldnf fetch --repofile rpm/repo-cs9.yaml
bazeldnf rpmtree --name launcherbase_x86_64_cs9 --basesystem centos-stream-release ...
bazeldnf prune ...
bazeldnf verify ...
```

### 10.2 Base Image Generation (`hack/rpm-base-images/generate-rpm-tars.sh`)

Converts rpmtree rules into rootfs tars without Bazel:

```bash
source hack/rpm-base-images/generate-rpm-tars.sh

# Downloads RPMs using pinned URLs,
# runs bazeldnf rpm2tar with symlinks/capabilities,
# outputs to _out/rpm-tars/
generate_rpm_tar launcherbase_x86_64_cs9
```

### 10.3 Container Flow Build Scripts

The Containerfile flow uses the following scripts:

| Script | Purpose |
|--------|---------|
| `hack/multi-arch-container.sh` | Build all component images using Containerfiles |
| `hack/multi-arch-push-container.sh` | Push all component images to the registry |
| `hack/rpm-base-images/build-base-images.sh` | Build RPM base images (generate tars + podman build) |
| `hack/rpm-base-images/push-base-images.sh` | Push RPM base images to registry |
| `hack/build-images-container.sh` | Build individual component images |
| `hack/build-container-disks.sh` | Build container disk images |

### 10.4 Manifest Generation

Manifests are generated using Go tooling directly:

```bash
cd tools/manifest-templator/ && go build && ./manifest-templator ...
```

### 10.5 Alt Tag/Prefix Handling for Operator Tests

The container flow publishes images in two additional ways for sig-operator upgrade tests:

1. **Same prefix with alt tag**: `registry:5000/kubevirt/virt-operator:devel_alt`
2. **Alt prefix with base tag**: `registry:5000/kubevirt/kv-virt-operator:devel`

This matches the Bazel flow's behavior in `hack/bazel-push-images.sh` to ensure sig-operator upgrade tests work identically.

---

## 11. Makefile Targets

### 11.1 Containerfile Flow Targets (new)

| Target | Description |
|--------|-------------|
| `make container-build-images` | Build all component images using Containerfiles |
| `make container-push-images` | Push all component images to registry |
| `make rpm-base-build` | Build RPM base images (generate tars + podman build) |
| `make rpm-base-push` | Push RPM base images to registry |
| `make rpm-deps` | Resolve RPM dependencies using standalone bazeldnf |
| `make verify-rpm-deps` | Verify RPM dependency checksums |
| `make go-build` | Build Go binaries using `go build` |
| `make go-test` | Run unit tests using `go test` |

### 11.2 Bazel Flow Targets (existing, to be retired)

| Target | Description |
|--------|-------------|
| `make bazel-build` | Build using Bazel |
| `make bazel-test` | Run tests using Bazel |
| `make bazel-build-images` | Build images using Bazel |
| `make bazel-push-images` | Push images using Bazel |
| `make bazel-generate` | Regenerate BUILD.bazel files |

The `bazel-*` targets will be removed as part of the final cleanup once the Containerfile flow is the sole pipeline.

---

## 12. Build Architecture Comparison

### 12.1 Current (Bazel)

```
  make <target>
       │
       ▼
  hack/dockerized
       │
       ▼
  Builder Container (with Bazel server)
       │
       ├──▶ bazel build   ──▶ Go binaries
       ├──▶ bazel test    ──▶ Test results
       └──▶ bazel run     ──▶ Container images ──▶ Registry
```

### 12.2 Target (Go + Containerfiles)

```
  make <target>
       │
       ├──▶ go build / go test           ──▶ Go binaries + test results
       │
       ├──▶ bazeldnf CLI (standalone)    ──▶ Pinned RPMs
       │
       └──▶ Containerfile (multi-stage)
                 │
                 ├── FROM base image (pre-built, with RPMs)
                 ├── COPY Go binaries
                 └── podman build --platform  ──▶ Registry
```

---

## 13. Testing Strategy

### 13.1 E2E Test Validation

All existing Prow E2E test lanes must pass against images built by the Containerfile flow:

| CI Lane | Scope |
|---------|-------|
| sig-compute | VM lifecycle, CPU, memory, devices |
| sig-network | Networking, CNI, service mesh |
| sig-operator | Install, upgrade, lifecycle management |
| sig-storage | Disks, volumes, snapshots, export |

### 13.2 Image Parity Validation

Images built by the Containerfile flow are validated for functional equivalence against Bazel-built images:
- Same RPM packages installed
- Same Go binaries present
- Same runtime behavior (verified via full e2e suite)
- All supported architectures (amd64, arm64, s390x) build successfully

Note: Byte-for-byte image comparison is not realistic due to build timestamps and layer ordering differences. The focus is on functional equivalence.

### 13.3 Unit Tests

Unit tests are unaffected by the image build migration — they run via `go test` directly, independent of the image build pipeline.

---

## 14. What Bazel Is Still Used For (During Transition)

During the migration, Bazel continues to be used for:

1. **Production CI builds** — The Bazel flow remains the blocking pipeline in CI until the Containerfile flow is promoted.
2. **Sandbox bootstrap** — Creates the build sandbox with system libraries needed for CGO compilation. The Containerfile flow handles this differently (libraries available in the base image or builder container).

Note: RPM dependency resolution (`hack/rpm-deps.sh`) already uses standalone `bazeldnf` and no longer requires Bazel.

---

## 15. Final Cleanup (Post-Migration)

Once the Containerfile flow is the sole pipeline and all Bazel jobs are retired:

| Artifact | Action |
|----------|--------|
| `BUILD.bazel` files | Delete all |
| `WORKSPACE` file | Delete |
| `bazel-*` Make targets | Remove from Makefile |
| `ci.bazelrc`, `.bazelrc`, `.bazelversion` | Delete |
| `hack/bazel-*.sh` scripts | Delete |
| `hack/dockerized` Bazel server logic | Remove Bazel-specific container setup |
| Bazel builder images (`quay.io/kubevirt/builder`) | Retire |
| Bazel-related Prow job configs | Remove |

---

## 16. Open Questions

| # | Question | Current Thinking |
|---|----------|-----------------|
| 1 | Final RPM dependency management tool? | bazeldnf standalone — open for feedback |
| 2 | Caching storage backend for CI? | GCS bucket (similar to k/k) — under research |
| 3 | How to handle CGO cross-compilation for virt-launcher? | Cross-toolchain in builder image — under research |
| 4 | Build time targets with caching? | Need measurements — currently ~2x without cache |
| 5 | How to validate image parity (Bazel vs Containerfile)? | Functional equivalence: same packages, same binaries, e2e pass |

---

## 17. References

- [VEP-392: Remove Bazel from KubeVirt Build System](https://github.com/kubevirt/enhancements/pull/393)
- [State of Bazel in KubeVirt — kubevirt#14038](https://github.com/kubevirt/kubevirt/issues/14038)
- [Kubernetes test-infra: cached make test jobs — PR #16623](https://github.com/kubernetes/test-infra/pull/16623/changes)
- [Decouple base image build — kubevirt#18286](https://github.com/kubevirt/kubevirt/pull/18286)
- [bazeldnf standalone — kubevirt#18534](https://github.com/kubevirt/kubevirt/pull/18534)
- [Container image build design proposal](https://github.com/vamsikrishna-siddu/kubevirt/blob/rpm-dependency-no-bazel/docs/design-proposals/container-image-build.md)
