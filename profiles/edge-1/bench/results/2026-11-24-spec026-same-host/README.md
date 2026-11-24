# Spec 026 T010: `serendipd` idle footprint, same-host A/B

> **Not the simulated T470.** A same-host A/B on the dev test bed, as
> spec 024 used to separate its code from the host. It does not satisfy
> the reference-hardware gate.

Run 2026-11-24 15:28–15:51 IST on `serendip-t018` (kind, the spec
017–025 stack with OpenBao), from Claude Code's shell (`ab.sh`).

- **A:** `serendipd` built from `main` at `214891f` (spec 025 merged).
- **B:** `serendipd` built from `spec-026` at `395820b` (T001–T009 and
  T012).
- Order A, B, A, B, one at a time on `:8088`. Each run got a fresh copy
  of the T009 store (`t026.db`), which holds Component `t026-reseal`. Its
  Deployment uses a secret variable (`PW`), delivered as
  `cmp-b5a4fea02d-secret-env`.
- Each run was read 4.5 min after start: the Windows working set from
  `tasklist`, 6 reads 10 s apart. Then the cluster's pods were listed.

## Criterion 12: PASS

| Pair | A (`main`) | B (`spec-026`) | B − A |
|---|---|---|---|
| 1 | 83.2 MiB (83.2–83.3) | 83.1 MiB (82.8–83.1) | **−0.1 MiB** |
| 2 | 35.1 MiB (34.8–35.5) | 36.1 MiB (35.7–36.5) | **+1.0 MiB** |

- Both pairs are within the 2 MiB target (plan, "What it costs"). Spec
  026's code adds no measurable idle memory with a Deployment using
  secret variables.
- **The working set was bimodal,** about 83 MiB in the first pair and
  35 MiB in the second, for both binaries. This is the Windows effect
  spec 024 and spec 025 met (their 79 and 82 MiB readings), not
  Serendip's code. That's why the criterion compares pairs taken
  back to back, not one reading against spec 025's 33.2 MiB on another
  stack. The second pair sits near that figure: B is +2.9 MiB over it,
  with a larger store and a different cluster.
- **No new pods** (`pods/`): the same 39 pods in every listing, apart
  from new names for the 4 workload pods that rolled. The only one not
  Running is `openobserve-setup-logs`, a Completed Job, in every
  listing. The feature adds one Secret per (Component with secrets ×
  Environment) and nothing that runs.

## What the switches did

- **`main` (A)** doesn't know `serendipSecretEnv`. At start it set the
  `serendip-service` type back to its spec 025 form, so every Deployment
  following the latest release rolled, and `t026-reseal`'s pods lost
  their secret variable for that run. It left `cmp-b5a4fea02d-secret-env`
  alone, as it doesn't know about it.
- **`spec-026` (B)** put the property back, and those pods rolled again.
  Their ReplicaSet hash came back the same (`b8d9bd7`), with `envFrom`
  and the version annotation restored.
- **The Secret** was never rewritten: its age ran 23 → 40 min across the
  runs. B's resolver found its version current.

That's decision 12's one roll on upgrade, both ways. A downgrade to a
pre-026 build drops secret variables from running pods until the
upgrade comes back, which T011's operations notes should say.

## Files

- `ab.sh`: the script, with this machine's paths.
- `ab.txt`: every working-set read and the secret-env Secret per run.
- `pods/`: the pod list before the run and after each run.

The store copies and `serendipd` logs stayed out of the repo (the store
holds the dev install's principals).
