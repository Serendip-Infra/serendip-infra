# Spec 023 verification (T013, real cluster)

Run 2026-11-14 on `serendip-t018`: kind, `kindest/node:v1.34.0`, with the
spec 017/018/022 stack (OpenBao, kgateway, OpenChoreo 1.3.1, ThunderID
1.0.1, the builds package). Tokens, passwords and keys were never printed.
Times are UTC.

## Setup

- **`serendipd`:** built from the working tree after T012 (`7f1fa1c`),
  with the console rebuilt and embedded, on `127.0.0.1:8088`. It was
  rebuilt twice during the run, as fixes landed (F1–F5 below); each
  criterion says which build it ran on where it matters.
- **Store:** a copy of spec 022 T014's (`t014.db`), so the T014 app, its
  builds and the builds package carried over. On start, `serendipd`
  found the workflow plane's releases missing from the cluster and
  re-installed them on its first resync.
- **API keys:**
  - `verifier`: Owner at `/`;
  - `t014-reader`: Reader at `/`;
  - `t014-contrib`: Contributor at Project `t014-builds`;
  - `t023-logreader`: Log Reader at Project `t014-builds`;
  - `t023-other`: Contributor at a new Project, `t023-other`.
  New keys start as Owner at `/` (spec 017's migration of unscoped keys).
  Those assignments were replaced before anything else was done.
- **A second Project:** `t023-other`, with Environment `t023-dev`,
  Application `t023-app` and Component `t023-echo` (nginx, deployed), for
  the cross-Project checks.
- **The package was removed first.** `TestLiveUninstall` removed the copy
  `TestLiveInstall` had put there outside `serendipd` (T006), in 38 s, so
  every install below started from an empty store.
- **The person:** `admin` in ThunderID. The console was driven in
  headless Chrome over the DevTools protocol. The CLI was signed in as
  `admin` (`serendip login --no-browser`, the callback pasted back), so
  `serendip mcp` acted as `agent:serendip-mcp/<admin>`. Screenshots were
  looked at and aren't committed.

## Evidence, by acceptance criterion

1. **Cost before install** (the Reader, package not installed):
   - **`GET /v1/packages`:** idle 608 MiB, 21 mcores; peak 967 MiB,
     372 mcores; disk 496 MiB a day, 3 days, budget 8192 MiB; measured
     on the simulated T470 on 2026-11-14, with the results folder and the
     fit note; `installable: true`. These are T012's numbers.
   - **`serendip packages list`:** the same, with "Disk: about 496 MiB a
     day, kept 3 days, at most 8192 MiB".
   - **MCP `packages_list`:** the same JSON.
   - **The Packages page:** "Idle: 608 MiB of memory and 0.02 CPU cores.
     At its peak: up to 967 MiB of memory and 0.37 CPU cores", "Disk:
     About 496 MiB a day, kept 3 days, at most 8,192 MiB", where it was
     measured, how it fits and what removing it loses.
3. **Install:**
   - **A Project's Contributor** (`t014-contrib`): `serendip
     provider-instances create` answered "forbidden: missing
     serendip/provider-instances/write at /", exit 3. Audit:
     `t014-contrib`, `cli (declared)`, `deny: missing
     serendip/provider-instances/write at /`.
   - **The Owner, from the console** (14:35:08). The inline confirmation
     restated the cost and the disk line. `t014-pub`'s Observability tab
     read:
     - 3 s: "Not installed. This needs the openchoreo-observability-plane
       package, which isn't installed. See packages";
     - 20 s: "Installing. The openchoreo-observability-plane package is
       installing. This section works once it's ready.";
     - 136 s: available (environment, time range, CPU and memory).
   - **Audited:** `person:admin`, `http`, `allow: Owner (…) at /`, then
     the reconciler's status writes.
   - **On a fresh store, OpenObserve's setup Job completed.** This is
     T012's SSRF fix (RP-062) on the real cluster. A second install
     through the API later took 116 s.
4. **Cheap resync:**
   - **Idle:** from the install (14:35–14:37) to the repair below, all
     four Helm releases stayed at revision 1: no resync re-ran Helm. The
     CPU figure is T012's: +0.87 mcores over 10 minutes.
   - **Repair:** the `logs-adapter-openobserve` Deployment, deleted by
     hand, was back in 11 s and rolled out. Only its release moved, to
     revision 2; the other three stayed at 1.
5. **Log search:**
   - **A deleted pod's lines:** three requests to `t014-pub` in
     `t014-dev` (`/t023known1794667071-1..3`, nginx's access and error
     lines) reached the store by 14:38:17. The pod was then deleted, and
     replaced.
   - **By text** (`q=t023known1794667071`): all 6 lines, from the deleted
     instance. This held through the API and the CLI (as the
     Project-scoped Log Reader), MCP (`deployments_logs_search`, as
     `admin`), and the console's Observability tab.
   - **By time only** (14:37:51–14:37:54, no text): the same 6 lines.
   - **An Application's search** (`t014-app`, `t014-dev`): one marker
     written into both Components' pods came back from `t014-priv` and
     `t014-pub`, with `omitted: 0`. This held through the API (Owner and
     Log Reader), the CLI and the console.
   - **Capped and paginated:** at `limit=2`, three pages with `next`
     gave the same 6 lines as one page of 100. Limits of 0, 1000 and 1001
     are refused: "limit: a whole number from 1 to 500".
   - **Just after the install,** two searches answered 500 with "soon
     after an install this can mean no Component has written a line
     yet". That was true: no line had been stored yet.
6. **Metrics:**
   - **A load:** a busy loop in `t014-pub`'s pod for 6 minutes, against
     its 100m limit.
   - **The console** (Last hour): "CPU, millicores: Latest 70m, highest
     100m, request 100m, limit 100m", the spike drawn against the
     request and limit line; memory 4.9 MiB of 256 MiB.
   - **MCP `deployments_metrics`:** 19 CPU points, summary max 100.08m.
     "Requests, errors and latency: needs request metrics, not installed"
     is listed as not available.
   - **The Reader** read metrics (200).
   - **Read just after the install,** the chart showed 0m. That was right:
     Prometheus had only one point then. The first CPU point needs two
     minutes of data, as T006 found.
7. **Build logs after the pods are gone:**
   - **Expiry** (build `01a1da88…`, 33 lines live): its WorkflowRun was
     deleted, as expiry does. The logs then came from the store,
     `stored: true`. The Reader is refused them (403).
   - **Found: F2 and F3,** both fixed. Re-checked on build `01a1da8e…`:
     with its pods deleted and its run kept, the logs answer `stored:
     true` with 33 lines, the live count, and no executor lines.
   - **After the package is removed,** an expired build's logs are 410 as
     before the package.
8. **Authorization:**
   - **The route table:** every new route has a rule (`go test
     ./api/...`, T008).
   - **A Reader** (`t014-reader`, Reader at `/`): logs 403 ("missing
     serendip/components/logs/read at Deployment/…"), audited as a
     `deny`; metrics 200.
   - **A Log Reader scoped to one Project** (`t023-logreader`): its own
     Project's searches answer, but `t023-other`'s Deployment is 403.
   - **An agent:** MCP calls ran as `agent:serendip-mcp/<admin>`, with
     the admin as sponsor. That an agent's search uses its own roles,
     and is narrowed to them, is `TestSpec023Criterion8_Authorization`
     (agent tokens).
9. **No side door,** from `t023-echo`'s pod (Project `t023-other`):
   - **These timed out:** OpenObserve (Service, and pod IP `:5080`),
     Prometheus (Service `:9091`, and pod IP `:9090`), the Observer
     (`:8080`), `observer-internal` (`:8081`), both adapters (`:9098`,
     `:9099`) and kube-state-metrics.
   - **Fluent Bit's own `:2020` answers.** It's on `hostNetwork`, so no
     policy applies to it. It serves counters, uptime and version, never
     log lines. Closing it would break the DaemonSet's own probes.
     Recorded as **RP-064**. This is the remaining path the criterion
     allows when it's in the risk register.
10. **Retention and disk use:**
    - **Retention as stated:** OpenObserve's ConfigMap has
      `ZO_COMPACT_DATA_RETENTION_DAYS=3`. Prometheus runs with
      `--storage.tsdb.retention.time=3d` and `--storage.tsdb.retention.size=2GB`.
    - **The package reports its use:** `GET /v1/packages` `usage`, 4 MiB
      of 8192 at 14:58 and at 14:59.
    - **Over a measured period:** this cluster's run was short (about 25
      minutes installed). T012's 2-hour disk phase under load is the
      measured period: 41.6 MiB, well inside the budget (RP-063 for how
      it extrapolates).
11. **Removal** (console, 14:59:14):
    - **The confirmation** names the ProviderInstance and states "All
      stored logs and metrics history are deleted. Recent logs and events
      on the Runtime tab keep working. Finished builds' logs can no
      longer be read once their pods are gone."
    - **The Approval:** Executed, decided by policy (`allow: Owner`),
      CleanedUp 44 s later. **F4** (the decision's time) was found here.
    - **The tab:** "Not installed" 48 s after the delete. **F5** was found
      in between.
    - **The cluster afterwards:**
      - no namespace, no Fluent Bit DaemonSet;
      - no store, adapter or collector pod anywhere;
      - no observability releases, no plane object;
      - no Prometheus Operator CRDs.
    - **What still works:**
      - Searches answer 503, naming the package.
      - The Runtime tab's recent logs answer (200).
      - A new build (`01a1da9b…`) succeeded, and its live logs read 33
        lines.
    - **F5 re-checked through the API** (an install, then a delete):
      Ready, then `Removing` 3 s after the delete, then `NotInstalled`
      25 s later.
13. **Nothing else breaks:**
    - `go test ./...` (all packages), `go vet ./...`, gofmt and the
      import check pass.
    - The UI's typecheck, 154 tests and build (104.1 KiB gz) pass.
    - The bench harness ran in T012.
    - This real-cluster run is the last part.

Criterion 2 (the backend chosen by measurement) is ADR 0021 (T003).
Criterion 12 (footprint) is T012's results folder.

## Findings, all fixed in T013

- **F1, the console's log times:** log times were UTC, cut from the
  raw timestamp. They sat beside a chart in local time, 5½ hours apart
  on this machine. `LogView` now shows local time, to the millisecond,
  with the UTC timestamp as its title.
- **F2, a finished build whose pods are gone:**
  - **Seen:** with the build's pods deleted while its WorkflowRun
    remained, its logs answered 200 with no lines, through the API, CLI
    and MCP. Only a gone run fell back to the store.
  - **Fix:** a finished build whose live read finds no lines now
    answers from the store, when the package is ready.
  - **Test:** `TestSpec023Criterion7_BuildPodsGone`.
- **F3, Argo's executor lines in stored build logs:**
  - **Seen:** Argo's executor prints its memory stats as its `init` and
    `wait` containers exit (`time=… level=info msg="Alloc=… Goroutines=…"`).
    That was 4 of 37 stored lines, and the live read shows none.
  - **Fix:** a run's stored lines carry no container (T006), so they're
    dropped by their shape, beside the emissary's lines.
- **F4, the decision time on Approvals decided by policy:**
  - **Seen:** a policy decision was stored with `decision.at` as
    `0001-01-01`. This is spec 019's code, and all 6 in this store had
    it. The console showed "739,934 days ago".
  - **Fix:** the store now stamps the decision with the request's time.
    The console falls back to the request's time for records stored
    before.
- **F5, readiness while the package is being removed:**
  - **Seen:** for the ~45 s uninstall the capability stayed Ready. The
    tab kept offering charts, which failed with "couldn't be read".
  - **Fix:** readiness is now `Removing` while the uninstall runs, and
    the console says the package "is being removed".
  - **Test:** `TestUninstallCleanup_RemovingWhileItRuns`.

## Also seen

- **The admin password once appeared in a screenshot.** The sign-in
  script's first version clicked the password field's "show" button
  instead of Sign In. The screenshot was deleted straight away and the
  script fixed. Rotating the test cluster's admin password is offered.
- **The workflow plane's releases were missing on start,** though the
  store recorded the builds package installed (left from spec 022's
  removal checks). `serendipd`'s first resync re-installed them, as
  designed.
