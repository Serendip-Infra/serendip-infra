# Spec 025 T013: Cilium, Hubble and tracing on the simulated T470

> **SIMULATION — does not satisfy the reference-hardware gate.**

Two runs on fresh `bench-t470` kind clusters (2.0 CPUs of quota on cpuset
0-3, 180 s settle, images through the local pull-through caches), one
after the other, with every other cluster stopped:

- **`kindnet/`**: the default kind CNI, as every run before this spec.
- **`cilium/`**: `CNI=cilium kind-t470-setup.sh up`, which installs
  Cilium 1.20.5 with the networking provider's values (Hubble metrics
  and flow export, Envoy for L7 visibility, kube-proxy kept).

```
bench-edge1 --package openchoreo-observability-plane \
  --package-load-samples 60 --package-disk-samples 0|61 \
  --package-traffic --package-tracing --package-traffic-samples 120 ...
```

Both runs used the same `serendipd`, built from `spec-025` with a
`go build -overlay` placeholder for the tracing package's requirements
(the catalogue refuses a package without them; see the bench README,
"Measuring an optional package").

## What the runs do

1. Cold start, platform idle; observability package install, 10 min idle;
   spec 023's log and metric load, 10 min (shortened from 30: spec 024's
   run measured the load's cost).
2. **HTTP traffic** (new, `--package-traffic`), still under the log load:
   a caller Component calling an exposed server Component 10 times a
   second, both `t012-otel:2` (`profiles/edge-1/bench/traffic-app`, a
   plain OpenTelemetry Go SDK app). 20 min.
3. **Traced** (`--package-tracing`): the tracing package installed, both
   Components given a new release so they send spans, then the same
   traffic for 20 min more.
4. Cilium only: a 1-hour disk phase with all of it still running, for the
   logs store's growth with traces.
5. Warm restart.

The Cilium run was started twice. The first attempt stopped in its cold
start: Claude Code's memory-pressure reaper killed the shell that had
started the harness. From then on every `helm` that `serendipd` started
failed with `0xc0000142`, a Windows process that can't initialise
without its console. It was rerun on a new cluster, and nothing from the
first attempt is used here. The kindnet run's cold start overlapped a
Docker image build on the host.

## Criterion 12: control-plane idle footprint: PASS

| `serendipd` at platform idle | RSS | CPU |
|---|---|---|
| Spec 024 (the comparison) | 79.0 MiB | 0.01% of 2.0 cores |
| Spec 025, kindnet run | 33.2 MiB | 0.03% |
| Spec 025, Cilium run | 82.1 MiB | 0.03% |
| Spec 025, restarted on the Cilium stack (4.5 min after ready) | 28.6 MiB | – |

Both runs are within rule 4 (a third of 256 MiB at most) and within
10 MiB of spec 024's 79.0 MiB, so the criterion passes on either figure.

The Cilium run's 82 MiB is the Windows working-set effect spec 024 met,
not Serendip's code:
- the same process fell to 59 MiB and then 51 MiB in its later phases,
  while it did more work;
- the same binary, restarted on the same Cilium stack with both
  packages installed, read 28.6 MiB (`timings.txt`);
- the kindnet run read 33.2 MiB and stayed near it.

A working set is what Windows has left mapped to the process. That
depends on the host's memory pressure as much as on the process, and
the host was short of memory during this run (the reaper above). On
Linux (the T470) the harness reads `/proc` RSS instead.

`compare-kindnet-vs-spec024.md`: every group is within tolerance at
platform idle, package idle and under load. Spec 025 adds nothing to the
node on a cluster without Cilium.

## Criterion 13: Cilium's node cost

The Cilium run minus the kindnet run, phase by phase (node means). The
kindnet node runs kindnet and kube-proxy; the Cilium node runs Cilium,
its operator and Envoy, and kube-proxy:

| Phase | kindnet node | Cilium node | Difference | Cilium's own pods |
|---|---|---|---|---|
| Platform idle | 1538 MiB, 235 mcores | 1981 MiB, 327 mcores | **+443 MiB, +92 mcores** | 236 MiB, 58 mcores |
| Observability idle | 2131 MiB, 270 mcores | 2562 MiB, 347 mcores | +431 MiB, +77 mcores | 258 MiB, 63 mcores |
| Under the log load | 2270 MiB, 329 mcores | 2778 MiB, 422 mcores | +508 MiB, +93 mcores | 270 MiB, 66 mcores |
| HTTP traffic (10 req/s) | 2413 MiB, 355 mcores | 2874 MiB, 483 mcores | **+461 MiB, +128 mcores** | 301 MiB, 89 mcores |
| HTTP traffic, traced | 2558 MiB, 365 mcores | 3054 MiB, 483 mcores | +496 MiB, +118 mcores | 302 MiB, 87 mcores |
| Disk phase (1 h, all on) | – | 3099 MiB, 496 mcores | – | 302 MiB, 95 mcores |

- **Cilium costs about +450 to +500 MiB and +80 to +130 mcores on the
  node.**
  - Cilium's own pods (agent, operator, Envoy) are about half the memory,
    236–302 MiB.
  - The rest is outside pods: node overhead is +139 MiB at idle (BPF
    maps and the runtime), and kube-apiserver is +64 MiB holding
    Cilium's CRDs and watches.
- **Under traffic,** Cilium's pods grow by ~65 MiB and ~30 mcores (Envoy
  and Hubble parsing each request). The observability group's Hubble
  scrape is counted in that group's own figures.
- **The heaviest sample of either run** was 3244 MiB (Cilium, disk
  phase). Against a 16 GB T470 less 2 GB for the host OS, that's 23%,
  well inside the 50% "comfortable" line. CPU at platform idle is 16% of
  2 cores with Cilium and 12% without.

### The edge-1 default: Cilium stays

Plan decision 16 sets the default from these numbers, and Cilium fits:
- it costs about half a GiB and a tenth of a core on a 16 GB, 2-core
  machine;
- the whole stack, observability and tracing included and under load,
  stays under a quarter of the memory.

What it buys is what spec 025 is for: request metrics, flows with drop
reasons, and NetworkPolicies enforced by Cilium, with drops visible in
the flows (criterion 3).

So `serendip-agent bootstrap --cni cilium` stays the default. `--cni
default` stays available for machines where half a GiB matters, such as
an 8 GB one. kube-proxy-free (plan decision 6) isn't needed to fit. ADR
0023 (T015) records the decision. RP-089.

## Criterion 13: the tracing pipeline's cost

Traced minus untraced, with the same traffic in the same run:

| | kindnet | Cilium |
|---|---|---|
| Node, mean | +145 MiB, +10 mcores | +180 MiB, +0 mcores |
| Node, traced max over untraced mean | +310 MiB, +195 mcores | +366 MiB, +99 mcores |
| Collector pod | 49 MiB, 3 mcores | 49 MiB, 3 mcores |
| Observability group | +17 MiB, +5 mcores | +31 MiB, +4 mcores |
| Tracing install (with pulls) | 1m24s | 1m50s |
| The server's traces at the end (last 5 min, one API page) | 50 | 50 |

**Disk.**
- **With traces,** the logs store (which keeps them) grew 46.9 MiB in
  the Cilium run's hour and Prometheus 4.7 MiB: 1226 MiB a day.
- **Without traces,** spec 023's 2-hour log-only phase, under the same
  log load, grew 496 MiB a day.
- **So traces add about 730 MiB a day** at 10 calls a second (three
  spans each). They're kept 3 days, so they take ~2.2 GiB of the logs
  store's 5 GiB.
- It overstates traces alone: the Cilium run's stores also took Hubble's
  connection records and request series, which spec 023's run didn't
  have. That's the safe direction for a stated cost.
- The 20-minute phases' slopes are too noisy to use: kindnet's untraced
  phase even shrank as the store compacted. RP-088.

**The tracing package's `Requirements`**
(`providers/observability/openchoreo/requirements.go`,
`TracingRequirements`), the larger of the two runs, rounded up:
- idle: 180 MiB, 10 mcores;
- peak: 370 MiB, 200 mcores;
- disk: 730 MiB a day, kept 3 days, in the logs store's 5 GiB.

**Reads** (`timings.txt`, on the Cilium stack with ~1.5 h of traces):
- a 1-hour trace list: 119 ms median;
- opening a trace: 163 ms;
- a 15-minute flows read: 64 ms.

RP-086 says opening a trace gets slower as the trace store grows; that
is measured only at this size. The store's minute-by-minute retention
job is inside the observability group's figures above, not measured
separately.

## Files

- `kindnet/`, `cilium/`: `result.json` and `summary.md` from
  `bench-edge1`.
- `compare-kindnet-vs-spec024.md`: criterion 12's no-regression check.
- `timings.txt`: the read timings and the restarted `serendipd`'s memory.
