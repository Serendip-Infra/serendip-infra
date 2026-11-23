# Tasks: Spec 025, Networking: Cilium, Hubble and traces

Status: Approved, 2026-11-19. T001–T006 done.

[P] = can run in parallel with the task before it. [A] = subagent-eligible.

## T001: Live checks of the research ✅

**Done as** (2026-11-20): every item checked on `t025` (kind, Cilium
1.20.5, Serendip's platform and the observability package installed by
`serendipd`) and in a `rancher/k3s:v1.36.4-k3s1` container. Findings
in the two `VERSIONS.md` files, answers in `testdata/t001-*`, and
`plan.md`'s "Checked live in T001". Changed decisions: 4 (labels, Envoy
retries off), 7 (p95), 9 (export filters and aggregation), 10 and 12
(traces from OpenObserve, adapter off). **Left to T006:** Fluent Bit
tailing the flow file live (T001 found the chart's `extraVolumes` keys;
the logs module has to be re-installed with Serendip's config to prove
it).

- **Do:** on a scratch cluster `t025` (kind, `disableDefaultCNI`, Cilium
  1.20.5 with the draft values), check what the plan relies on:
  - Node `spec.podCIDRs` with IPAM `kubernetes`; `cilium-config` keys the
    provider reads.
  - The `httpV2` labels OpenChoreo's adapter joins on (`metric=http`
    answers for a test Component), and whether it has a p95.
  - Mirroring a Component's NetworkPolicy into a CiliumNetworkPolicy
    with `http: [{}]` and `enableDefaultDeny` off: a peer refused before
    is still refused, allowed traffic is proxied and counted.
  - The static exporter's JSON shape, the allowList filters for drops
    and new connections, and Fluent Bit tailing it (the chart's volume
    values).
  - The tracing module 0.3.2 on the existing OpenObserve: the adapter's
    query scope (UIDs or names), the response shapes, and the collector
    config override with `pod_association` from the connection (a span
    claiming another pod's IP lands under its real pod).
  - K3s's `HelmChart` with `bootstrap: true` installing Cilium on a
    `rancher/k3s` container started with `--flannel-backend=none
    --disable-network-policy`.
- **Files:** `providers/networking/cilium/VERSIONS.md`,
  `providers/observability/openchoreo/VERSIONS.md`, `testdata/` answers,
  `plan.md` (decisions changed by what's found).
- **Verify:** each item above recorded as checked live, with the answer
  files; `plan.md` updated where a decision changed.

## T002: Core types ✅

**Done as** (2026-11-20): `core/reconcile/networking.go` (facts,
`FlowSource`, flows, `RequestSeries`, `TrafficEdge`, and `FlowReader` /
`TrafficReader` as optional interfaces on the observability provider, like
`PackageUsage`, so providers build unchanged until T006);
`core/reconcile/tracing.go` (`TracingProvider`, `ValidTraceID`,
`ErrTraceNotFound`); `Metrics.Requests`; the three conditions with
validation (`requestErrorRate` with an optional `minRequests`,
`requestLatency` p95/p99, `traceErrors`); the evaluator's routing
(`AlertEvaluatorOptions.Tracing` and `RequestMetrics`, both failing closed
when unwired); `serendip/components/traces/read` out of Reader and the two
access roles, in Log Reader. contract-guardian passed; its cheap notes
fixed (fail closed, a wording test).

- **Do:** `NetworkingProvider`, `NetworkFacts`, `FlowSource`
  (`core/reconcile/networking.go`); `Requests`, `TrafficEdge`, `Flow`,
  `FlowQuery`, `FlowPage` and the two `ObservabilityProvider` methods;
  `TracingProvider` and its types (`tracing.go`); the
  `requestErrorRate`, `requestLatency` and `traceErrors` conditions and
  their validation; `serendip/components/traces/read` (out of Reader,
  like logs); the alert evaluator routing the new conditions and
  refusing them without a source.
- **Files:** `core/reconcile/{networking,tracing,observability,alerts}.go`,
  `core/resource/alerting.go`, `core/policy/builtins.go`, tests.
- **Verify:** `go test ./core/...`; `bash .claude/scripts/check-imports.sh`;
  `go build ./core/... ./api/...` with `providers/` absent from the
  import graph.

## T003 [P]: The `external` networking provider ✅

**Done as** (2026-11-20): `providers/networking/external` (`New(Reader)`,
`Facts`). kindnet by its DaemonSet (version from the image tag;
`enforced`, or `unknown` for a kindnetd image older than `v20240813`,
before kube-network-policies); Flannel by a `kube-flannel*` DaemonSet or,
on K3s, by the nodes' `flannel.alpha.coreos.com/*` annotations (`unknown`
with the plan's reason; `notEnforced` when `k3s.io/node-args` holds
`--disable-network-policy`, a true positive only); anything else
`unknown`. Pod ranges from the nodes' `podCIDRs`. A failed read returns
the facts it has with the error and names what's missing in
`Unavailable`. Live on `serendip-t018`: kindnet `v20250512-df8de77b`,
`enforced`, `10.244.0.0/24`; a deny-all ingress policy made a probe that
had answered time out. `VERSIONS.md` records what's unit-tested only.
contract-guardian passed; its notes fixed (an unparseable kindnetd tag is
`unknown`, not `enforced`; `notEnforced` on K3s needs the DaemonSet list
read and no other policy controller in it; a skipped pod range is named
in `Unavailable`; for a CNI it can't name, the flows and requests
reasons say Serendip can't read them, not that the CNI lacks them).

- **Do:** detection from kube-system DaemonSets and node annotations,
  the facts plan decision 3 lists.
- **Files:** `providers/networking/external/`.
- **Verify:** table-driven tests (kindnet, Flannel on K3s, unknown, read
  failure); live on `serendip-t018`: kindnet reported, policies
  enforced.

## T004: The `cilium` provider ✅

**Done as** (2026-11-20): `providers/networking/cilium`:
- **`Facts`:** version from the agent's tag; enforcement from
  `enable-policy` and `enable-k8s-networkpolicy`; flows and requests
  readable only with the pinned export mask and aggregation (1.20 and
  later) and the `httpV2` metric with pod labels, each drift named in
  `Unavailable`; pod ranges from the nodes (IPAM `kubernetes`) or the
  CiliumNodes.
- **The embedded `values.yaml`** (T001's final) with `ChartVersion`
  1.20.5, and `FlowSource`.
- **`EnsureVisibility`:** copies each target's NetworkPolicy into
  `serendip-l7-<np>`, owned by it, with `http: [{}]` on its HTTP ports
  only and `k8s:` label keys.
  - Shapes it can't copy are skipped with a reason (RP-082).
  - It writes only what differs, and remakes a policy someone else added
    fields to.
  - A target it can't read keeps its policy; the others go on.
  - On proxy drift (retries, path rewriting) it removes every policy it
    owns (RP-081).
  - `Report` names what was applied and deleted, for T008's audit record.
- **Which ports are HTTP:** only OpenChoreo's `Workload` says, so
  `openchoreo.HTTPWorkloads` (a file beyond this task's list) lists
  exposed endpoints and their NetworkPolicy per Environment, keyed by
  Project and Component. T008 adapts it to `cilium.Target`.
- **`kubernetes.ObjectMeta`** gained `OwnerReferences`.
- **Live on `t025`:**
  - srv's policy was made and `Valid`, and a second pass made no writes.
  - A pod in `default` was still dropped (`Policy denied`); same-namespace
    and system-component pods answered 200 through the proxy (`to-proxy`,
    `http-request`).
  - A new exposed Component got its policy, and deleting the Component
    removed it (garbage collector).
- **Reviews:** contract-guardian passed, and its notes are fixed:
  matching by Project as well as Component; `Facts` says when no pass
  has run or a pass failed; the audit record carried to T008. go-reviewer's
  findings are fixed: a slice aliased and compacted in place, a stale
  last report, per-target failures, ctx checks, duplicate targets, long
  names, deletes counted only when made, and fields added by others.

- **Do:** facts (version, Hubble settings, IPAM, ranges, drift from the
  pinned values); the embedded `values.yaml`; the L7 visibility ensurer
  (mirror, owner reference, skip-with-reason, interval); `FlowSource`.
- **Files:** `providers/networking/cilium/`.
- **Verify:** unit tests (mirroring keeps peers and ports, never adds
  one; untranslatable policies skipped); live on `t025`: a Component's
  policy created, deleted with the Component, and a refused peer still
  refused.

## T005: Install path ✅

**Done as** (2026-11-20):
- **`serendip-agent bootstrap --cni cilium|default`** (`cilium` the
  default): `agent/bootstrap/cni.go`. With `cilium`, `INSTALL_K3S_EXEC`
  is `server --flannel-backend=none --disable-network-policy`, and
  `Install` first writes the HelmChart (`bootstrap: true`, chart 1.20.5
  from `helm.cilium.io`, the values as `valuesContent`) to
  `/var/lib/rancher/k3s/server/manifests/serendip/cilium.yaml`.
  `--dry-run` prints both. `--timeout` defaults to 10 minutes (was 5).
  A kernel older than 5.10 (Cilium's documented minimum) is a warning,
  not a refusal.
- **The values copy:** `agent/bootstrap/cilium-values.yaml`, kept
  identical to the provider's `values.yaml`, `ChartRepo` and
  `ChartVersion` by `TestCiliumCopyMatchesProvider` (it reads the
  provider's files; agent/ can't import providers/).
- **Offline install (the plan left it to T005):** not supported; the
  chart and images come from the internet at first boot, like K3s
  itself (RP-083, with the static-charts route for later).
- **`kind-t470-setup.sh` `CNI=cilium`:** adds `disableDefaultCNI` to the
  kind config, installs the provider's chart version and values with
  Helm (an empty repository config, so the user's own Helm repos can't
  break it), then waits for the node.
- **Dev cluster `serendip-t025`** (Cilium; state `~/.serendip-t025-dev`,
  gateway on host port 30083, `serendipd` on `:8089` in its
  `dev-up.env`); `dev-up.ps1`'s help names both dev clusters.
  `serendip-t018` stays kindnet.
- **Live:** see `providers/networking/cilium/VERSIONS.md`, "Checked live
  in T005": the K3s container went Ready with Cilium in about 7 minutes
  (cold pulls); `kind-t470-setup.sh up` with `CNI=cilium` gave a Ready
  node in 521 s.
- **`dev-up.ps1` against `serendip-t025`** (run by the user, the
  session's permission check having refused the launch): built and
  started `serendipd` on `:8089`, the console answered 200, the log
  clean. Its two warnings are expected on a bare cluster (no OpenBao
  unseal file, ThunderID not installed).
  - **Found after (2026-11-21):** serendipd then installed OpenBao,
    ThunderID, the gateway and OpenChoreo itself, but sign-in failed
    ("The request is invalid"). serendipd advertises the gateway's
    NodePort as the host port (issuer `http://identity.localhost:30080`),
    and the cluster mapped NodePort 30080 to host port 30083, so the
    browser reached `serendip-t018`'s ThunderID. Fixed: `dev-up.ps1`
    gained `GatewayNodePort` (`SERENDIP_GATEWAY_NODE_PORT`), and the state
    dir's `kind.yaml` maps 30083 to 30083. The cluster has to be
    recreated for the mapping (kind can't change it in place): deferred
    to when the console is needed (the user, 2026-11-21).
- build-vs-adopt: Helm in the agent rejected for K3s's HelmChart.
- **go-reviewer:** fixed: the manifest is written after the script
  downloads (a failed download changes nothing), `--cni default` removes
  a Cilium manifest an earlier attempt left, `valuesContent: |2-` (stated
  indentation; parses the same as T001's `|-`, checked), the dry run
  shows the kernel warning, `--timeout` is 10 minutes with Cilium and 5
  without, `--cni` is ignored on `--uninstall`, the version regex is
  anchored, writes are sorted. Left as is, by design: the library's zero
  `CNI` is Flannel (existing callers keep their behaviour; only the CLI
  defaults to Cilium). Not checked: what `k3s-uninstall.sh` leaves of
  Cilium on a real host (RP-083).

- **Do:** `serendip-agent bootstrap --cni cilium|default` (K3s flags, the
  HelmChart manifest, wait for Ready, `--dry-run`); the values copy and
  its equality test; `kind-t470-setup.sh` `CNI=cilium`; the dev cluster
  `serendip-t025` with `dev-up.ps1` pointed at it.
- **Files:** `agent/bootstrap/`, `cmd/serendip-agent/`,
  `profiles/edge-1/bench/`, `scripts/dev-up.ps1`.
- **Verify:** `go test ./agent/...`; a `rancher/k3s` container
  bootstrapped with the agent's flags and manifest reaches Ready with
  Cilium; `kind-t470-setup.sh up` with `CNI=cilium` gives a Ready node.

## T006: Request metrics, traffic and flows in the observability provider ✅

**Done as** (2026-11-23):
- **Prometheus:** a `hubble` scrape job (DNS on `hubble-metrics`, keep
  list of six series) in `prometheus-values.yaml` and the bench copy;
  the client gained labelled vectors and range queries.
- **Requests** (`requests.go`): all five series from Serendip's own
  PromQL, not the adapter's `metric=http` (plan decision 7 amended: one
  5xx definition for the chart and the alert). `WithRequestMetrics`
  takes the networking facts' answer (T008 wires it); unavailable
  reasons: not provided, "no HTTP endpoint" (no exposed port), store
  unreadable (CPU and memory still returned), none in range. The two
  conditions read over at least 2 minutes (a 1-minute rule would never
  have two samples).
- **Traffic** (`traffic.go`): the Environment's pods with their UIDs, then
  the rates in their namespaces, joined in Go; both ends must be
  targets; `ErrRequestsUnavailable` when not provided.
- **Flows:** `Installer.SetFlowSource` (validated; in the fingerprint)
  adds a read-only hostPath volume, a tail input, a Lua filter
  (`fluent-bit/serendip-flows.lua`) that flattens each record, and an
  output to `serendip_flows`. `flows.go` reads it with SQL from the
  targets' Deployments (found by UID labels), with time bounds and a
  full sort order in the SQL, the logs' cursor, and EGRESS/INGRESS
  pairs de-duplicated one to one. The other end is named only for a
  target or a platform namespace (an allow-list); anything else is
  Outside.
- **Found live:** Helm replaces `extraVolumes`, so the chart's `db`
  entry is restated; the chart's `--workdir` makes `parsers.conf` the
  image's file, so the flow parser is a second `Parsers_File`; only the
  source records a connection through the L7 proxy, and the proxy's own
  leg (no namespace) is dropped. RP-084, RP-085; RP-054 extended.
- **Live on `t025`** (`TestLiveInstall` with `OBS_LIVE_FLOW_SOURCE`,
  `TestLiveNetworking`): Fluent Bit tails `events.log` into
  `serendip_flows` (carried from T001); srv 5.6 req/s, 5xx about 9%,
  p50 17.7 ms, p95 24.5 ms, p99 39 ms; caller and spoofer "no HTTP
  endpoint"; edges caller→srv 5.2/s and spoofer→srv 1.0/s; a probe from
  `default` dropped, "refused by a network policy", 24 attempts.
  Recorded answers in `testdata/t006-*`.
- **Reviews:** contract-guardian passed; its allow-list point, the zero
  `From` (clamped to the retention) and the window minimum are fixed;
  T009 gates flows on the facts. go-reviewer's findings fixed: tie
  order, bounds in SQL, the over-limit cursor, one-to-one pairing,
  header copy, decoder limits, cache pruning, two queries at a time, a
  sentinel for an unreachable store, the output's host from the
  constants. Left as is, by design: a cursor newer than `to` restarts
  (as logs do); `SELECT *` (naming a field the stream hasn't had yet is
  an error); `Installer.applying` read without the lock (only Install
  reaches it, one package job at a time).

- **Do:** the Hubble scrape job and allowlist; `Requests` in `Metrics`;
  `Traffic`; the request conditions; the Fluent Bit flow input from
  `FlowSource` and the `serendip_flows` stream; `Flows` reads with
  attested-label SQL and the other end's redaction.
- **Files:** `providers/observability/openchoreo/` (`prometheus-values.yaml`,
  `conditions.go`, `observer.go`, `fluentbit.go`, new `flows.go`,
  `traffic.go`), the bench copy of the Prometheus values, `testdata/`.
- **Verify:** unit tests against T001's recorded answers; live on
  `t025`: Fluent Bit tails the flow file into `serendip_flows` (carried
  from T001), a test Component's requests, a traffic edge, a dropped
  flow with its reason.

## T007: The tracing package ✅

**Done as** (2026-11-23):
- **Installer** (`tracing.go`, `TracingInstaller`): installs nothing
  until the observability package's store and admin Secret are there;
  Serendip's collector config (ConfigMap `serendip-trace-collector`,
  its hash in the pod annotations), then the module 0.3.2 with its
  store, adapter and tail sampling off and no Helm `--wait`; readiness
  from the collector's pods, with the observability package's settle
  and doubling repair; a changed config is re-applied and the pods
  restarted. `TracingFingerprint`, `OTLPEndpoint` exported for T008.
- **Collector config** (`tracing-collector.yaml`): an allow-list of
  resource attributes, `service.*` span attributes deleted, attribution
  by connection, and a `filter` dropping unattributed spans. T001's
  strip rule let other spellings onto the attested columns (found live,
  plan decision 10 amended, RP-086).
- **Removal:** Helm uninstall, the ConfigMap, then the `traces` stream
  (type only, so the logs stream of the same name stays), waiting 90 s:
  the store queues a stream's deletion for its retention job, which the
  logs store now runs every minute (`ZO_COMPACT_DATA_RETENTION_INTERVAL=60`,
  `values.go`); spans written before it runs are lost (found live, the
  oss-researcher read it from source). An install refuses while the
  store says a deletion is pending.
- **Reads** (`tracing_read.go`, `Tracing`): SQL on the traces stream by
  the three attested UID columns (`search.go`, shared with `flows.go`);
  summaries from the target's spans only, paged with the logs' cursor;
  `ErrorsOnly`, `MinDuration` and the limit in the SQL; `Trace` reads
  the target's spans (cap 1000) and counts the others as `Hidden`; span
  attributes named like identity are never shown; `traceErrors` is one
  `count(DISTINCT trace_id)`. Nothing stored, a deletion pending or
  missing columns read as nothing, not as an error.
- **Application provider** (`tracing.go`): `SetTraceEndpoint`; the
  OTLP variables join a Component's next **new** release (plan decision
  11 amended), unless it sets them itself.
- **Verified:** unit tests (span filtering, caps, spoofed attributes,
  paging with ties and straddling traces, the installer's paths, the
  release rule). Live on `t025`: the hand-installed failed release
  upgraded; Components' traces listed and opened, filters and
  `traceErrors`; a caller trace opened as the spoofer is not found and
  the spoofer's spans are under its own UID; a spoof span from an
  unlabelled pod stored with no identity, then dropped
  (`testdata/t007-spoof-check.json`); removal left logs working (store
  and `serendipd`'s logs API); remove-then-reinstall lost no spans.
- **Reviews:** contract-guardian passed; its and go-reviewer's findings
  fixed (the root query on a stream without a parent column, missing
  columns read as nothing, page windows ending at `to`, the repair's
  settle time, number and kind decoding, error wording). Left as is, by
  design: a cursor newer than `to` restarts (as logs and flows do);
  `Hidden` counts other Components' spans (plan decision 12); internal
  Service names in transport errors (as flows); `time.After` (Go 1.26
  collects unfired timers). **Carried to T008:** the endpoint follows
  the ProviderInstance's existence, not Ready (plan decision 11); the
  package job's timeout must exceed the 90 s removal wait; the
  observability package's removal also removes the collector (RP-086).
  **To T009:** merge the spans the caller can read and answer
  `partial`, never `Hidden`'s count (plan decision 12, decided after
  T007).

- **Do:** `openchoreo-tracing` installer (module, Serendip's collector
  config, tail sampling off, memory limiter, the module's adapter off,
  readiness checked by the installer); `TracingProvider` (SQL search on
  OpenObserve with the filters, trace scoped to the target's UIDs,
  `traceErrors`); uninstall deleting the
  stream; the OTLP environment variables in the application provider at
  next deploy.
- **Files:** `providers/observability/openchoreo/tracing*.go`,
  collector config, `providers/application/openchoreo/`, `VERSIONS.md`.
- **Verify:** unit tests (span filtering, cap, spoofed attributes
  removed); live on `t025`: a test Component's trace listed and opened,
  a spoofing span not under the claimed Component, removal leaves logs
  working.

## T008: `serendipd` wiring ✅

**Done as** (2026-11-23):
- **Networking loop** (`cmd/serendipd/networking.go`): `cilium.Detect`
  (the agent's DaemonSet; a new provider function) chooses `cilium` or
  `external`, retried with the setup back-off until it can tell, then
  registered as capability `networking` with its readiness. One pass on
  the re-ensure interval (sooner when the application platform comes
  up): the visibility pass, then the flow source, then `Facts`, cached
  and served by `networkState.Facts` (T009's `GET /v1/networking`). Each
  step has its own timeout. Errors are logged only when they change.
  The CNI is detected once per process, by design: plan decision 1 has
  it chosen at cluster creation.
- **Visibility pass:** `HTTPWorkloads` adapted to `cilium.Target`. A pass
  that applies or deletes writes one audit row (`NetworkVisibility` /
  `request-visibility`, after `{"applied":…, "deleted":…}`) as
  `system:reconciler` via the new `sqlite.RecordExternalChange` (core's
  audit row for a change made outside the store, constitution 8). A row
  that fails to write is kept and retried the next pass.
- **Cooperation values:** `Installer.SetFlowSource` from the cilium
  provider's `FlowSource` (zero for `external`, and after 2 failed reads
  with none known). The observability package's install and check wait
  for it, so the log collector isn't applied twice at start.
  `WithRequestMetrics` from the cached facts (the `requests:` reason).
  The notifier's ranges get `PodRanges` merged in (RP-076 updated).
  `providers/kubernetes/ranges.go` was left unchanged: the merge sits in
  cmd. A test checks that `obsplane.FlowFormat` equals `cilium.FlowFormat`.
- **Tracing:** catalogue entry `openchoreo-tracing` (capability
  `tracing`, Requirements nil until T013, so it's refused; its `Removal`
  names the traces' deletion and the one restart). It has a lazy
  `TracingProvider` handle (for T009). The trace endpoint is set from
  the store at start when a `tracing` ProviderInstance exists, and then
  by the package's Install. It's cleared after a successful Uninstall.
  Removing the observability package first removes the trace collector
  and holds tracing's installs until observability installs again (its
  `Removal` says so). The 30-minute package job covers the 90 s wait.
- **Live:** on `t025`, `networking (cilium): cilium 1.20.5; network
  policies enforced; connection records yes; request metrics yes`. On
  `serendip-t018` (started for the check with a store copy, then
  stopped), `networking (external): kindnet v20250512-df8de77b; network
  policies enforced (kindnet's own policy controller); connection
  records no; request metrics no`. On `t025`, with a 1-minute interval,
  srv's deleted L7 policy was re-created at start and again by an
  interval pass. That left exactly two audit rows naming it, and the
  no-write passes left none. Fluent Bit kept its flow input and
  observability read Ready with no second apply. Evidence is in
  `~/.serendip-t025/t008/`.
- **Reviews:** contract-guardian passed. Its findings (a flow source that
  is never readable, the tracing ID) and go-reviewer's are fixed:
  per-step timeouts, audit retry, change-only logs, the flag and
  endpoint cleared only after success, the loop waited for before the
  store closes, and the startup read bounded.

- **Do:** detect the CNI and register the provider; serve the cached
  facts; pass `FlowSource` and the OTLP endpoint between providers; the
  tracing catalogue entry (measured requirements left nil until T013,
  so it can't be installed before); the notifier's ranges merge (RP-076);
  the L7 visibility pass on the re-ensure interval (`EnsureVisibility`
  with `openchoreo.HTTPWorkloads` adapted to `cilium.Target`), recording
  an audit event naming `Report.Applied` and `Report.Deleted` when a pass
  writes (T004's contract-guardian note).
- **Files:** `cmd/serendipd/`, `providers/kubernetes/ranges.go`.
- **Verify:** `go test ./cmd/... ./providers/kubernetes/...`;
  `serendipd` started on `t025` and on `serendip-t018` reports each
  CNI in its log; on `t025` a pass that writes leaves an audit event
  naming the policies, and one that doesn't leaves none.

## T009: API ✅

**Done as** (2026-11-23):
- **Routes** (rules in `authz.go`, plan decision 14):
  `GET /v1/networking` (`NetworkingHandler`, `platform/read` at `/`):
  the cached facts, 503 until first read. `GET /v1/deployments/{id}/flows`
  and `GET /v1/applications/{id}/traffic` (`networking_handler.go`,
  `observability/read`): gated on the facts (`networkGate`: 409 "not
  available: <the facts' reason>"), then the provider. Traffic passes
  only the Application's Deployments in the Environment the caller may
  read (`envTargets`), counts the rest in `omitted`, 422 past 20, and
  keeps only edges with both ends readable. `GET /v1/deployments/{id}/traces`
  and `…/traces/{traceId}` (`traces_handler.go`, `components/traces/read`;
  503 naming the tracing package, with "can't be installed in this
  build" for `ReasonUnavailable`). Metrics' request series are made
  finite, summarised (`requests.*`) and downsampled like CPU and memory.
- **Flows' other end** (plan decision 9 amended): `FlowQuery.Peers`
  (core): the Environment's other Deployments the caller may read,
  named and never searched (`flowRefs` in the provider); the API clears
  any other named end (`redactEnd`).
- **One trace read** (plan decision 12 amended): `TracingProvider.Trace`
  takes every readable target at once (two queries, an OR of the
  scopes); `partial` is `Hidden > 0`, which is never returned.
- **`reconcile.ErrNotCollected`**: the provider's
  `ErrRequestsUnavailable` wraps it; `lazyObservability` forwards
  `Flows`/`Traffic` and answers it for a package without them. Read
  failures name their data (`readSource`): 409, 503 with Retry-After,
  504, or 502 without the error's text.
- **Catalogue:** `networking_get`, `deployments_flows`,
  `applications_traffic`, `deployments_traces_list`,
  `deployments_traces_get`, on the CLI and MCP (traces on MCP like stored
  logs; T010 may revisit); rows in `docs/cli-and-mcp.md`.
- **Tests:** `spec025_acceptance_test.go` over fakes that name every end
  and edge (so the API's own scoping is what passes): criterion 6 (A→B
  with B outside, no B→B, peers only A's), 5, 9 (Reader 403, guessed ID
  404, merge and partial), 2's 409s, failure mapping, the traffic cap.
  Checked by mutation: with `redactEnd` off, criterion 6 fails.
- **Live on `t025`** (`serendipd-t009.exe`, `~/.serendip-t025/t009/`):
  facts, srv's flows with caller and spoofer named as peers and the
  collector as a platform service, two traffic edges, request series in
  metrics, traces 503 (the package isn't installable until T013).
  `TestLiveTraces` opened traces as all targets: 2 spans, 0 hidden,
  against 1 and 1 as one target.
- **Reviews:** contract-guardian passed; its doc note fixed. go-reviewer's
  findings fixed (labelled read failures, stop at the cap, tests for
  every failure path, comments); its trace-loop findings went away with
  the one-call `Trace`. Left as is: the five-series loop in Metrics (a
  small allocation per request).


- **Do:** the six routes of the plan's table, authorization, scoping
  before the backend, the other end's redaction, audit of every new
  mutation, the catalogue entries.
- **Files:** `api/networking_handler.go`, `api/observability_handler.go`,
  new `api/traces_handler.go`, `api/catalog/`, tests,
  `api/spec025_acceptance_test.go`.
- **Verify:** `go test ./api/...`, including: a reader of Project A sees
  A→B flows with B unnamed and no B→B flow; a guessed trace ID of an
  unreadable Component answers 404; Reader without the traces action
  gets 403.

## T010 [P] [A]: CLI and MCP

- **Do:** the commands and tools from the catalogue.
- **Files:** `cmd/serendip/`, `api/mcp_handler.go` (catalogue-driven).
- **Verify:** `go test ./cmd/serendip/... ./api/...` with the parity
  test passing.

**Done as** (2026-11-23):

- **No CLI code needed:** the commands are T009's catalogue entries,
  and the CLI's generic parser already takes a second path parameter by
  position (`traces show <deployment-id> <trace-id>`, or `--traceId`).
  Answers print as indented JSON, like stored logs.
- **Traces stay on MCP** (the plan's "unless that rule says otherwise"):
  stored logs are MCP tools under their own permission, and spec 021's
  rule is the permission, not the interface; an assistant acting for a
  person reads traces only where the person may. No MCP cap below the
  API's: a list is at most 200 short rows, and one trace's 1,000 spans
  can pass `mcp.MaxResult`, which truncates with a note.
- **Tests:** `cmd/serendip/networking_test.go`, against the real API
  handler: each command's request (both ways of passing the trace ID),
  usage errors sent nowhere (required `--environment`, the caps, the
  verdict enum, a missing or extra ID), each answer printed, a trace the
  Deployment has none of exits 5 and a malformed ID 1; on MCP the five
  read-only tools, their answers, `traceId` required, and every request
  declared `mcp`.
- **Live on `t025`** (`bin/serendip-t010.exe` against the T009
  `serendipd`, `~/.serendip-t025/t010/live.txt`): networking facts,
  srv's flows (caller, spoofer, the collector as a platform service),
  two traffic edges; traces 503 with the tracing package's reason, exit
  7, until T013.

## T011: Console

- **Do:** the Requests, Flows and Traces sections; the topology overlay;
  Settings → Platform networking; the three condition types in the rule
  form; "not available" reasons everywhere.
- **Files:** `ui/src/screens/`, `ui/src/components/TopologyDiagram.tsx`,
  `ui/src/lib/`.
- **Verify:** `npm test` and `npm run build` in `ui/`; the gzipped JS size
  recorded.

**Done as** (2026-11-23):

- **Observability tab** (`ui/src/screens/Networking.tsx`): an "HTTP
  requests" block in the metrics section (requests per second, 5xx per
  second, and one latency percentile at a time, p50/p95/p99 picked, so
  each chart stays one series); "Connections" (all or refused only,
  refused listed first with the policy's reason, an end the caller can't
  read said "Not visible to you", paged by `next`); "Traces" (greyed out
  until the `tracing` capability is ready, its own permission like logs,
  duration and errors-only filters, the "redeploy to send traces" hint)
  and one trace as a waterfall table with SVG bars (no inline styles,
  CSP), a partial trace and a truncated one said, a chosen span's
  attributes listed. Spec 023's "Traces: Not available yet" is gone.
- **Topology overlay:** off until turned on (no traffic read before),
  then an Environment and range; `TopologyDiagram` labels each observed
  call "5.4/s, 9.9% failed" and strokes it in a status colour (warn above
  0%, error at 5%); an observed call nobody declared is dashed. A live
  traffic table carries the same facts, and `omitted` is said.
- **Platform networking** went on the Platform page ("Network layer"):
  the console's Settings is notifications only, and the facts are
  platform state beside where workloads run. Plugin and version, policy
  enforcement with its reason, flows and request metrics each available
  or the reason, pod ranges, when read.
- **Rule form:** the three condition types, each with what it needs; a
  refused rule's list entry now shows the evaluator's own message
  ("trace conditions need the tracing package installed") rather than the
  generic words.
- **"Not available" reasons everywhere:** a 409 or 503 from a networking
  or traces read shows the server's detail (`notAvailableReason`), never
  an error box.
- **Checks:** `pnpm typecheck`, 191 tests (19 new:
  `networking.test.tsx`, two rule-form tests and wording tests in
  `alerts.test.tsx`), `check-contrast.mjs`, `pnpm build`. **Gzipped JS:
  118.7 → 124.9 KiB (+6.2; budget 160).** `ui/DESIGN.md` gains the
  overlay and waterfall rows. Not looked at in a browser here: `t025`'s
  `serendipd` has API keys only, so no console sign-in; T012 does the
  browser checks on `serendip-t025`.

## T012: Live check ✅

- **Do:** spec criteria 1–11 and 14 on `serendip-t025`, criterion 2 on
  `serendip-t018`, in the browser and through the CLI and MCP; findings
  fixed.
- **Files:** `specs/025-networking/verification.md`.
- **Verify:** `verification.md` lists each criterion with its evidence
  and result.

**Done as** (2026-11-23, in two parts: 15406ad, then after T013):
`specs/025-networking/verification.md`. Criteria 1–11 and 14 pass on
`serendip-t025` (criterion 2 on `serendip-t018`). Criteria 12 and 13 are
T013's. Seven findings, all fixed:
- F1: request and trace rules were never wired;
- F2: replaced pods dropped from conditions (RP-085);
- F3–F5: wording, and the latency chart's 0 tick;
- F6: a trace opened through a Deployment it has no span in. Now 404
  (`api/traces_handler.go`, `TestSpec025Criterion9_Traces`);
- F7: the test app's scheme-less `TARGET`.

The tracing part ran through the API, CLI, MCP and console. It covered
the spoofing Component, `reader-c` with trace read at one Deployment, and
removal with its Approval, shown to delete stored traces by reinstalling.

## T013: Bench on the simulated T470 ✅

- **Do:** `bench-t470` with kindnet and with Cilium (idle, and under the
  observability load with HTTP traffic), then with tracing installed;
  the tracing package's requirements from the numbers; the edge-1
  default.
- **Files:** `profiles/edge-1/bench/results/2026-11-xx-spec025-*/`,
  `cmd/serendipd/packages.go` (requirements).
- **Verify:** spec criteria 12 and 13: control-plane idle within 10 MiB
  of spec 024's; node costs in the results summary.

**Done as** (2026-11-23; results in
`profiles/edge-1/bench/results/2026-11-23-spec025-simulated-t470/`, its
README the summary):

- **Harness** (`cmd/bench-edge1/traffic.go`): `--package-traffic` (a
  caller calling an exposed server 10 times a second under the log load)
  and `--package-tracing` (the tracing package installed, both
  Components re-released, the same traffic traced). Cilium's and the
  collector's pods are their own groups. The traffic image's source is
  `profiles/edge-1/bench/traffic-app` (T012's `otelapp`, which now takes
  a scheme-less `TARGET`: OpenChoreo gives a dependency as `host:port`
  on a non-80 port).
- **Runs:** kindnet, then Cilium (with a 1-hour disk phase), on fresh
  clusters. The first Cilium attempt died in its cold start after the
  memory reaper killed its shell (`helm` then failed `0xc0000142`); it
  was rerun.
- **Criterion 12: PASS.** `serendipd` idle: 33.2 MiB (kindnet), 82.1 MiB
  (Cilium), against spec 024's 79.0. The 82 MiB is the Windows
  working-set effect: the same process fell to 51 MiB later, and a
  restart on the same stack read 28.6 MiB. Every group is within
  tolerance of spec 024's run (`compare-kindnet-vs-spec024.md`).
- **Criterion 13:**
  - Cilium costs **+443 MiB, +92 mcores** at idle and +461 MiB,
    +128 mcores under traffic. RP-089.
  - Tracing costs **+145 to +180 MiB, ~10 mcores**, and ~730 MiB a day
    in the logs store at 10 requests a second. RP-088.
  - The heaviest sample was 3244 MiB, 23% of a 16 GB machine.
  - **The edge-1 default stays Cilium** (ADR 0023 in T015).
- **`TracingRequirements`** (`providers/observability/openchoreo/
  requirements.go`): idle 180 MiB / 10 mcores, peak 370 MiB /
  200 mcores, disk 730 MiB a day for 3 days in the logs store's 5 GiB
  (`logsCapacityMiB`). The catalogue now installs the package.
- **Reads** (`timings.txt`): trace list 119 ms, trace open 163 ms,
  flows 64 ms (medians); RP-086 and RP-087 updated.

## T014 [A]: Reviews ✅

- **Do:** go-reviewer, contract-guardian, a security review of the
  scoping and the collector's identity, test-writer for gaps; findings
  fixed.
- **Files:** as found.
- **Verify:** each review's findings fixed or recorded with a reason;
  `go test ./...` and `go vet ./...` pass.

> **Done as:** `go test ./...` and `go vet ./...` pass.
>
> **Security review:** the API's scoping and the collector's identity
> hold. Its one finding is fixed:
> - Cilium's host-network metrics ports (9962–9965) let any pod read
>   every Component's request metrics, around criteria 5 and 6.
> - The `cilium` provider now keeps `serendip-node-metrics-guard`
>   (`nodeguard.go`), a cluster-wide deny-only policy, on every pass. It
>   blocks those ports to pods outside `kube-system` and the
>   observability plane.
> - Live on `t025`: a Component pod and a `default` pod time out on all
>   four ports and still reach 10250, and Prometheus's `hubble` job stays
>   up (RP-091).
>
> **contract-guardian:** passed. Its notes are recorded:
> - visibility audit rows wait in memory, and an edited policy is
>   replaced in two steps (RP-090);
> - the networking provider leans on the application platform's labels,
>   and the guard on the observability plane's namespace name (RP-091;
>   `TestNodeGuardExemptsPrometheus` checks the name agrees);
> - a re-check of the T014 fixes passed. Its notes are applied (the
>   register's order, RP-091's residuals).
> - `TracingRequirements` is from the simulated T470, so re-measure it
>   on the L470.
>
> **go-reviewer (two passes).** Fixed:
> - **A race:** the observability package's removal and the tracing
>   package's jobs could run at once, because core's package jobs are
>   per package. They now share a lock (`opLock`), and the tracing
>   install checks `obsRemoving` under it.
> - The observability removal only removes the collector
>   (`RemoveCollector`), so a store that's down can't wedge it.
> - A truncated trace is shown under a Deployment only if that
>   Deployment's own spans exist (a second read, scoped to it).
> - The store's credentials are cached for a minute, and dropped on a
>   401.
> - `scopeWhere` checks the UIDs itself.
> - Flow order includes protocol, and `recordLess` uses `cmp`.
> - CNI manifests are written atomically.
> - Smaller fixes:
>   - the kernel minimum is one constant;
>   - shutdown audit retries are 2 s;
>   - a blank request-metrics refusal reason gets a default;
>   - the bench flags a traced phase with no traces;
>   - nits (`TraceIDLen`, `slices.Compact` on ports, `strings.Compare`,
>     `const` reasons, one `waitTrue`).
>
> **Left as is, by design:**
> - `time.After` in loops: collected since Go 1.23.
> - A failed hidden-span count fails the trace read, so a trace is never
>   shown as whole when it may not be.
> - Fingerprints are recomputed per pass (microseconds; tests swap the
>   config).
> - One store list per trace condition (a local SQLite read).
> - `ReportStart`'s background context (not this spec's code).
> - The external-change audit's transaction (one audit writer).
> - Uncached request and traffic reads, bounded by slots and the 20 s
>   deadline (RP-087).
> - The OTLP receivers have no per-pod rate limit (RP-092).

## T015 [A]: Docs and close-out

- **Do:** ADR 0023 (Networking: observed, Cilium, the edge-1 default
  with T013's numbers); `docs/architecture/networking.md`;
  `capability-model.md`, `operations.md` (CNI reinstall, flows, traces),
  `bootstrap-agent.md`; risk register entries; build-vs-adopt (Cilium,
  Hubble export over relay, kube-proxy kept, tracing module adopted with
  Serendip's collector config); `PROGRESS.md`.
- **Files:** `docs/`.
- **Verify:** docs-keeper review passes; every risk the plan names is in
  the register or marked not found.
