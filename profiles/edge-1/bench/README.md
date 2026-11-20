# edge-1 benchmarks

Constitution rule 9: benchmarks live here and run on reference hardware (a
ThinkPad T470) before profile defaults change. Rule 4 sets the budget they
check: the control plane under 256 MB RSS and 5% CPU at idle.

| File | What |
|---|---|
| `001-resource-model.md` | Spec 001: 10k-resource CRUD smoke test (dev machine) |
| `002-edge1-footprint.md` | Spec 013: whole edge-1 stack idle footprint (simulated T470) |
| `results/` | Raw `result.json` / `summary.md` per run |

## The footprint harness: `cmd/bench-edge1`

One command benchmarks the whole edge-1 stack as `serendipd` deploys it:

1. Starts `serendipd` against a cluster and times a **cold start** (every
   install, including image pulls).
2. Waits until every pod is Ready, settles, then samples **idle memory and
   CPU per component group** and `serendipd`'s own process.
3. Deploys a **light workload** (3 exposed nginx Components by default),
   records each one's create → `Serving` time, settles, and samples again.
4. Restarts `serendipd` and times a **warm restart**. The earlier run's
   log is kept as `serendipd.1.log` (then `.2`, …), so the restart
   doesn't overwrite it.
5. Writes `result.json` (every sample, machine profile, verdicts) and
   `summary.md`.

**How it measures:** cluster numbers come from the kubelet Summary API
(`/api/v1/nodes/<node>/proxy/stats/summary`, memory = `workingSetBytes`),
read through the API server. Nothing extra is installed in the cluster,
and the same code works on kind and on K3s. `serendipd` is measured as a
host process (`/proc` on Linux, PowerShell on Windows).

**Disk** (spec 023): each PersistentVolumeClaim's use, first and last
in a phase, with its growth put per day.
- **Volume stats:** the kubelet's, where it reports them.
- **host-path volumes** (local-path, edge-1's default): the kubelet
  reports nothing for these. So a short-lived `busybox` pod in `default`
  mounts their parent directories read-only and runs `du`, at the first
  and last sample of each phase only, and is deleted straight after.
  `-disk-probe=false` turns it off.
- **`compare`** also states each volume's growth from run A to run B.

**Groups** come from the namespaces Serendip's providers deploy into.
Anything unrecognised is listed by name, never hidden.

**Verdicts in every result:**
- the rule-4 pass/fail line for `serendipd`;
- the 16 GB headroom verdict: idle platform + 10 light Components against
  16 GB − 2 GB host OS. Up to 50% is comfortable, up to 75% tight,
  otherwise it points to 32 GB.

### Run it on a real T470 (the reference-hardware gate)

On the T470, with K3s installed by `serendip-agent bootstrap`:

```sh
go build -o serendipd ./cmd/serendipd
go build -o bench-edge1 ./cmd/bench-edge1
sudo ./bench-edge1 --serendipd ./serendipd --kubeconfig /etc/rancher/k3s/k3s.yaml --out results/<date>-t470/run1
```

Don't pass `--sim-*` flags. A run without them is a real reference-hardware
result. Run it twice and compare:

```sh
./bench-edge1 compare results/<date>-t470/run1/result.json results/<date>-t470/run2/result.json
```

### Simulate a T470 on a dev machine (kind)

```sh
profiles/edge-1/bench/kind-t470-setup.sh up bench-t470 /tmp/bench-t470.kubeconfig
go run ./cmd/bench-edge1 --serendipd ./serendipd --kubeconfig /tmp/bench-t470.kubeconfig \
  --sim-cpus 2 --sim-cpuset 0-3 \
  --sim-note "kind node limited to 4 threads and 2.0 CPUs of quota to approximate an i5-6300U (2c/4t)" \
  --not-simulated "16-32 GB RAM (only measured, not limited); SATA/NVMe disk speed" \
  --out profiles/edge-1/bench/results/<date>-simulated-t470/run1
profiles/edge-1/bench/kind-t470-setup.sh down bench-t470
```

**Image caches (optional, saves time and data):** `registry-cache.sh up`
starts one local pull-through cache per upstream registry, with layers in
Docker volumes that outlive clusters. `kind-t470-setup.sh up` wires new
clusters to them when they're running; `registry-cache.sh wire <cluster>`
wires an existing one. A cache that's down or can't serve an image falls
back to the real registry. `registry-cache.sh status` shows the disk each
cache uses, and `down --purge` frees it. First used for spec 023's T012
runs (2026-11-14): both cold starts pulled through them, with results in
line with spec 022's runs.

### Measuring an optional package

`--package <provider>` installs that package through the API after the
platform's idle sample, measures it, and writes a package section and
its stated cost (the node's use beyond the platform phase) to the
summary. An unknown provider is refused. The catalogue refuses a package
without `Requirements`, so the first measurement runs a `serendipd`
built with `go build -overlay` and placeholder numbers.

- **Builds** (`openchoreo-workflow-plane`): idle, two builds, auto-build
  and one full Helm run.
- **Observability** (`openchoreo-observability-plane`): idle, then
  `--package-load-script` (default `observability-load/load.sh`) for
  `--package-load-samples` (180) samples every 10 s, then
  `--package-disk-samples` (121) every `--package-disk-interval` (1m)
  for the volumes' growth per day, then the load's query latencies.
- **Alert rules** (`--package-rules N`, observability only, at most 199:
  the install's cap of 200 enabled, less the built-in disk rule; spec 024
  T011). Between the load and the disk phases, still under the load:
  1. deploys 3 nginx Components for the rules to watch;
  2. samples a baseline with no rules;
  3. creates N rules, disabled, and samples them (resync only);
  4. enables them all and samples them evaluating, for
     `--package-rules-samples` (180) samples every 10 s.

  The rules stay on through the disk phase, which is the plateau check
  for evaluation latency. The rule types are a third each of metric (cpu
  or memory), restarts and logMatch (5 or 15 minutes), all at 1 minute.
  Their thresholds can't be reached, so nothing fires.

  The section reports, for each phase:
  - node and `serendipd` CPU and memory;
  - store writes a minute, by kind, and audit rows a minute, read from
    `serendipd`'s SQLite file;
  - the evaluator's 10-minute timing lines (`reconcile: alerts: stats:`
    in `serendipd`'s log): queries, failed, no data, mean, p50, p95,
    max, the slowest tick and the latest pick-up.

Any `--sim-*` flag marks the run as a simulation. Its summary opens with
"SIMULATION — does not satisfy the reference-hardware gate", and no
footprint may be reported as verified on its strength (constitution rule 6).

## Observability load (spec 023)

`observability-load/load.sh` puts a steady log and query load on an
installed observability backend (spec 023 plan decision 9):
- **five loggers** in `serendip-obs-load`, each 10 lines a second of
  about 120 bytes, labelled like OpenChoreo Components with fixed
  made-up UIDs;
- **a driver pod** in the backend's namespace, so its NetworkPolicies
  need no change. It runs a log search every 10 s (one logger by UID in
  turn) and a 1-hour metrics query every 30 s, and prints one line per
  query.

```sh
export KUBECTL="kubectl --context kind-t470"
./observability-load/load.sh start          # driver in openchoreo-observability-plane
./observability-load/load.sh rate           # about 100 lines per logger per 10 s
./observability-load/load.sh latency        # queries, failures, mean and max seconds
./observability-load/load.sh stop
```

The defaults query OpenChoreo's logs and metrics adapters. For another
backend, set `LOGS_URL`, `LOGS_BODY`, `METRICS_URL`, `METRICS_BODY` and
`AUTH_HEADER` before `start`, and pass its namespace (see `driver.sh` for
the placeholders).

## Reading results honestly

- On kind, the "node" is the kind container, not the Docker VM, so the
  totals exclude the VM's own overhead. On K3s, node totals are the host.
- Cold start is dominated by image pulls, so it's network-bound. Warm
  restart is the CPU/API-bound number.
- Create → `Serving` includes the workload image pull, and is quantised by
  the reconcile engine's early re-checks (about 2, 6, 14, 22 and 30 s after
  create, at most 8 s apart; spec 014). A workload ready at 23 s is
  reported at 30 s, the harness's 2 s poll adds up to 2 s more, and the
  application platform's own Ready lag (about 10 s after the pods) comes
  first.
