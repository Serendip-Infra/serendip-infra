# Spec 023 T012: edge-1 footprint on the simulated T470, and the observability package's cost

> **SIMULATION — does not satisfy the reference-hardware gate.**

Every run used a fresh `bench-t470` kind cluster (2.0 CPUs of quota on
cpuset 0-3, 180 s settle, 12 platform samples every 10 s). The host's
gateway port was mapped to 30081 (`KIND_CONFIG`), as in spec 022's runs.
Images came through the local pull-through caches
(`profiles/edge-1/bench/registry-cache.sh`), their first verified use.

| Run | Date | What |
|---|---|---|
| `no-package/` | 2026-11-14 | edge-1 without any package, against spec 022's run (the regression check) |
| `package/` | 2026-11-14 | `bench-edge1 --package openchoreo-observability-plane`. **Its numbers are the package's stated cost** |

The package run used a `serendipd` built with `go build -overlay` and a
placeholder `Requirements`, because the catalogue refuses to install a
package with no measured cost. Everything else was the working tree,
including the SSRF fix below.

## Without the package: no regression (PASS)

`compare-no-package-vs-spec022.md` compares against
`2026-11-06-spec022-simulated-t470/no-package`, and every group is within
tolerance. Kubernetes system was 814.9 → 787.7 MiB.

- **`serendipd`:** 29.5 MiB RSS idle, 0% CPU. Rule 4: **PASS**.
- **Cold start:** 316 s. **Warm restart:** 92 s, against 31 s for spec
  022. The difference is the network: the host's Helm fetch of the
  kgateway-crds chart was reset (`wsarecv: An existing connection was
  forcibly closed by the remote host`) and retried. The package run's
  warm restart, on the same machine later that day, took 32 s.

## With the package

What the package run does, after the platform's idle sample:
1. installs the package through the API (create → Ready): **3m34s**,
   with image pulls;
2. 10 min idle, sampled every 10 s;
3. starts the load (`observability-load/load.sh`, from T002): five
   loggers in `serendip-obs-load` and a driver sending steady log and
   metric queries through the adapters;
4. 31 min under load, sampled every 10 s;
5. 2 hours more under load, sampled every minute, for disk growth;
6. stops the load and reads its query latencies.

| | Idle | Under load | Disk phase (2 h) |
|---|---|---|---|
| Node total, mean (platform: 1466 MiB) | 2074 MiB | 2226 MiB | 2251 MiB |
| Node total, max | 2129 MiB | 2413 MiB | 2433 MiB |
| The package's own pods, mean / max | 273 / 303 MiB | 404 / 577 MiB | 429 / 573 MiB |
| OpenObserve, mean | 124 MiB, 6 mcores | 247 MiB, 38 mcores | 264 MiB, 40 mcores |
| Prometheus, mean | 83 MiB, 6 mcores | 84 MiB, 7 mcores | 90 MiB, 7 mcores |
| Node CPU, mean / max (platform: 258 mcores) | 278 / 397 | 325 / 629 | 317 / 411 |
| `serendipd` | 30.5 MiB, 0.87 mcores | 29.6 MiB, 0.93 mcores | 29.5 MiB, 0.81 mcores |

The load's own pods are a few MiB of these totals: the loggers are 3.6
MiB ("Other: serendip-obs-load"), and the query driver runs in the
plane's namespace, so it counts in the package's group.

Queries during the load: **metrics** 290, all 2xx, mean 12 ms, max 94
ms; **logs** 868, all 2xx, mean 0.47 s, max 1.5 s.

### Stated cost

As for builds (RP-046), the stated figures are **the node's use beyond
the same run's platform phase**, never below the package's own pods.

- **Idle: 608 MiB, 21 mcores.** About 275 MiB is the package's own pods,
  ~40 MiB more is kube-apiserver (523 → 565 MiB), and ~285 MiB is node
  memory outside pods (the container runtime and file cache, 487 → 771
  MiB), with ~1 GiB more of images on disk (2204 → 3209 MiB).
- **Peak: 967 MiB, 372 mcores.** The largest sample over the load and
  disk phases.

### Disk

The three volumes grew **41.6 MiB over the 2-hour disk phase, 496 MiB a
day** extrapolated: the logs store 431 MiB a day, Prometheus 65 MiB, the
Observer's alerts volume nothing. The shorter load phase extrapolated
higher (654 + 73 MiB a day), as the logs store compacts its first data;
the longer phase is the stated figure.

Both stores keep 3 days (`logRetentionDays`; Prometheus' `retention:
3d`), so under this load they level off near **1.5 GiB**, well inside
the stores' 8 GiB of volumes (logs 5 GiB, metrics 3 GiB, which is the
stated budget and what `usage.capacityMiB` reports). The harness's
summary says 8320 MiB because it also counts the Observer's 128 MiB
volume.

`GET /v1/packages`' `usage.usedMiB` read 34 MiB at the end, against ~65
MiB used on the volumes' filesystems: usage reads the stores' own sizes
(OpenObserve's compressed data and index, Prometheus' blocks and WAL),
not filesystem use.

### Resync (criterion 4)

With the package installed and nothing changed, `serendipd` used **+0.87
mcores** over the 10-minute idle phase against the same run without the
package (0.04% of the 2-core quota), so a resync doesn't re-run the
install: it stays on the fingerprint path.

## Fit on a T470

Idle, the observability package costs about 600 MiB of a 16 GB T470;
under steady logging and queries it peaks near 1 GiB on the node, with
OpenObserve taking about a fifth of a core. Its stores grow about 500
MiB a day and keep 3 days. This is `Requirements.Note`, which the
console and `GET /v1/packages` show.

## Verified

`GET /v1/packages` on a `serendipd` built from the working tree (with
`requirements.go`), against the package run's cluster and a copy of its
database, returns `idleMemoryMiB: 608, idleCPUMillicores: 21,
peakMemoryMiB: 967, peakCPUMillicores: 372`, `disk: {growthMiBPerDay:
496, retentionDays: 3, budgetMiB: 8192}`, the measurement place, this
folder and the note, with `installable: true`.

## Notes on the runs

- **A fresh install failed at first** (not kept here). The logs chart's
  setup Job (`openobserve-setup-logs`) hit BackoffLimitExceeded:
  OpenObserve's SSRF guard refused its webhook destination,
  `http://logs-adapter:9098/...`, because the Service is a private
  ClusterIP. `serendip-t018` had hidden this: its destination already
  existed. The package now sets `ZO_SSRF_ALLOW_LOOPBACK=true`, as the
  module's newer releases do; RP-062 records the trade-off.
- **Harness changes from this task:** the package section is generic
  over a package's group; `--package` refuses an unknown package; an
  observability run drives `--package-load-script` through load and
  disk phases (`--package-load-samples`, `--package-disk-samples`,
  `--package-disk-interval`), and the summary has load, disk and
  latency sections.
