# Spec 024 T011: the alert evaluator's cost on the simulated T470

> **SIMULATION — does not satisfy the reference-hardware gate.**

One run, `rules/`, on a fresh `bench-t470` kind cluster (2.0 CPUs of
quota on cpuset 0-3, 180 s settle, 12 platform samples every 10 s), with
the host's gateway port on 30081 (`KIND_CONFIG`) and images through the
local pull-through caches, as in spec 023's runs. `serendip-t018` was
stopped for the whole run (spec 023's ran beside it).

```
bench-edge1 --package openchoreo-observability-plane --package-rules 199 \
  --out profiles/edge-1/bench/results/2026-11-15-spec024-simulated-t470/rules ...
```

The run started 2026-11-16 11:09 and ended 15:09 (IST). Three earlier
attempts didn't finish: Claude Code's memory-pressure reaper killed the
first two, and the third stopped at its first rule (a restarts threshold
of 1000, over the API's 999 cap). The harness now checks every bench rule
against the API's own validator (`TestBenchRule_PassesValidation`).

## What the run does

1. Cold start, platform idle; package install, 10 min idle; spec 023's
   log and metric load, 31 min (spec 023's phases, unchanged).
2. **Rules** (`--package-rules`), still under the load:
   - deploys 3 nginx Components for the rules to watch;
   - **baseline:** 10 min with the Components and no rules;
   - creates 199 rules, disabled, then **rules disabled:** 10 min
     (resync only);
   - enables all 199, then **rules evaluating:** 30 min.
3. **Disk phase:** 2 hours more, the load and the rules still on, sampled
   every minute. This is the plateau check for evaluation latency (below).
4. Stops the load and reads the driver's query latencies; warm restart.

199 rules, because the install's cap is 200 enabled and the package's
built-in disk rule is one. Every rule runs at 1 minute, over 3
Components:

| Type | Rules | Condition |
|---|---|---|
| `metric` cpu | 34 | value above 1,000,000 mcores for 5 min |
| `metric` memory | 33 | value above 1,000,000 MiB for 5 min |
| `restarts` | 66 | above 999 in 10 min |
| `logMatch` "error" | 33 + 33 | above 999 in 5 min / 15 min |

The thresholds can't be reached, so nothing fires: the run measures
evaluation, not delivery (T010 covered delivery live).

## Constitution rule 4: PASS

| `serendipd` | RSS | CPU |
|---|---|---|
| Platform idle (rule 4's measure) | 79.0 MiB | 0.01% of 2.0 cores |
| Package idle, no rules | 82.1 MiB | 1.13 mcores |
| Rules evaluating (30 min) | 88.4 MiB mean, 90.3 max | 6.79 mcores mean, 40.2 max |
| Disk phase (2 h, rules on) | 68.6 MiB | 6.78 mcores |

Even with 200 rules evaluating every minute, `serendipd` is a third of
rule 4's 256 MiB and 0.3% of the CPU budget (5% at idle).

**Why 79 MiB, not spec 023's 29.5 MiB.** Every run since spec 017 read
27–36 MiB. A same-host A/B showed this isn't spec 024's code: spec 023's
`serendipd` (main) and this branch's, each started against the installed
`bench-t470` stack with a fresh store and read 4.5 min later, settled at
24 MiB and 25 MiB. The difference is how this run was launched: from the
user's own terminal, where Claude Code's background jobs kept being
reaped, not from Claude Code's shell as before. The cause of the higher
Windows working set wasn't found. This run's deltas (evaluation +5.5 MiB,
below) compare phases of the same process, so they stand.

## Without rules: no regression (criterion 12): PASS

`compare-package-vs-spec023.md` compares this run's platform idle,
package idle and package-under-load phases (all before any bench rule
exists) with spec 023's package run. Every group is within tolerance in
all three.

| | Spec 023 | Spec 024 |
|---|---|---|
| Platform idle, node | 1466 MiB, 258 mcores | 1422 MiB, 268 mcores |
| Package idle, node | 2074 MiB, 278 mcores | 2060 MiB, 310 mcores |
| Under load, node | 2226 MiB, 325 mcores | 2206 MiB, 375 mcores |
| Package install (with pulls) | 3m34s | 4m38s |
| Cold start | 316 s | 372 s |
| Warm restart | 32 s | 35 s |

The cold start and install were slower because the image caches had just
restarted with Docker Desktop. Resync with the package installed (spec
022's fingerprint path): +1.00 mcores.

## The rules' cost

| Phase (under the load) | Node memory mean / max | Node CPU mean / max | `serendipd` RSS | `serendipd` CPU | Store writes / min | Audit rows / min |
|---|---|---|---|---|---|---|
| Baseline (no rules) | 2322 / 2450 MiB | 397 / 512 mcores | 82.8 MiB | 2.57 mcores | 0.2 | 0.2 |
| Rules disabled | 2340 / 2539 MiB | 399 / 574 mcores | 84.8 MiB | 2.95 mcores | 0.1 | 0.1 |
| Rules evaluating | 2499 / 2852 MiB | 631 / 1201 mcores | 88.4 MiB | 6.79 mcores | 25.9 | 25.9 |
| Disk phase (2 h) | 2579 / 2927 MiB | 546 / 1111 mcores | 68.6 MiB | 6.78 mcores | 26.4 | 26.4 |

- **Resync** (199 disabled rules − baseline): **+0.38 mcores**.
- **`serendipd`'s evaluation** (evaluating − baseline): **+4.22 mcores,
  +5.5 MiB**.
- **The node pays far more than `serendipd`:** +234 mcores mean (+177 MiB)
  while evaluating, +149 mcores over the 2-hour phase. It's the backends
  answering: OpenObserve averaged **375 MiB and 179 mcores** in the disk
  phase, against 264 MiB and 40 mcores in spec 023's disk phase with the
  same load and no rules. On a 2-core T470, 200 one-minute rules are
  about 7–12% of the machine. RP-080.
- **Store writes:** all AlertRule status flushes, 814 in the 30-minute
  phase and 3197 in the 2-hour one. Each is an audit row of about 2 KiB
  (it snapshots the rule), so about **37,000 rows and 75 MiB a day** for
  200 rules. Up to 20 of the 26 a minute are the 10-minute `lastEvaluated`
  refresh (`alertStaleEvaluated`) that every rule gets even when nothing
  changed (199 rules / 10 min). The rest are rules whose value changed,
  flushed once a minute. RP-080,
  open: this needs a decision (plan, "Findings from T011").
- **At the end:** 173 rules `Evaluating=True/Evaluated`, 27
  `False/NoData`, none `QueryFailed`; no Alert firing.

## Evaluation latency, and the plateau

The evaluator logs one timing line per 10 minutes (`reconcile: alerts:
stats:`, new in T011). Window 1 starts with the 199 rules enabled part
way through it; windows 2–15 are the steady state, and windows 5–15 fall
in the disk phase.

| Windows | Queries / 10 min | "Failed" | Mean | p50 | p95 | Max | Slowest tick |
|---|---|---|---|---|---|---|---|
| 1 (partial) | 804 | 39 | 264 ms | 20 ms | 959 ms | 2.2 s | 33.9 s |
| 2–5 | 1800 | 81–141 | 242–258 ms | 18–19 ms | 1.08–1.16 s | 1.9–3.3 s | 37.7–38.2 s |
| 6–10 | 1800 | 76–150 | 198–276 ms | 14–18 ms | 0.86–1.16 s | 1.9–4.9 s | 31.8–47.8 s |
| 11–15 | 1600–2000 | 56–145 | 195–211 ms | 14–15 ms | 0.78–0.86 s | 1.8–4.1 s | 29.4–36.7 s |

All 15 lines are in `rules/summary.md`.

- **The plateau holds.** Over 2.5 hours, while the logs volume grew 117
  MiB, the mean fell from about 255 ms to 205 ms and p95 from 1.1 s to
  0.8 s. Nothing climbs, so by plan decision 2's replacement method (the
  user's choice: a 2-hour plateau instead of 3 days of data), this stands
  for the 3-day figure. RP-055's slowdown (spec 023: 0.24 s to 0.53 s for
  the load driver's searches) is not visible in the evaluator's own
  queries, which are `limit = 1` counts over 5 or 15 minutes.
- **The load driver's searches were slower than in spec 023** with the
  rules competing: mean 0.90 s, max 5.1 s over 1149 searches (one not
  2xx). Metrics: mean 18 ms, max 120 ms over 383 queries.
- **The p50 of 14–20 ms against a p95 near 1 s** is the mix: metric and
  restart queries are fast, and the logMatch third is the slow one.
- **"Failed" counted "no data" answers too.** That's ~6% of queries, all
  of them `NoData`: none of the 200 rules was `QueryFailed` at the end,
  and the log had no query-failure lines when checked at 13:35. The rest
  of the log was lost (the warm restart overwrote it; fixed, below). The
  timing line now counts `noData=` separately.
- **Rules ran every ~66 s, not 60 s.** That's 1800 queries per 10 minutes
  instead of 2000. The evaluator counted each rule's next run from the
  tick that picked it up. A tick a minute later then fires a few ms
  before that, and the rule waited another 15 s tick. **Fixed after the
  run, not re-measured:** the next run now counts from the due time
  (`TestAlerts_CadenceHoldsWhenTicksDrift`: 8 runs in 10 minutes before,
  10 after). Expect about 11% more queries than measured, so about +165
  to +260 mcores on the node.
- **One pass takes 30–48 s.** All 200 rules come due in the same tick
  (they were enabled together) and run 2 at a time. That fits inside a
  minute, with room for about 25–60% more query time before rules start
  falling behind.
- **Window 1's 690 s "latest pick-up"** was the rules' disabled time
  counted as lateness. Fixed: a disabled rule is due as soon as it's
  enabled, and its disabled time isn't lateness.

## Fixed after the run (harness and evaluator, not re-measured)

- `serendipd`'s log is kept across restarts (`serendipd.1.log`, …).
  Before, the warm restart overwrote the cold run's log, and with it the
  evaluator's lines.
- The timing line counts `noData` apart from `failed`, and the summary has
  a "No data" column.
- The cadence and lateness fixes above (`core/reconcile/alerts.go`).

## Files

- `rules/result.json`, `rules/summary.md`: the run, as written by
  `bench-edge1`.
- `compare-package-vs-spec023.md`: criterion 12.
