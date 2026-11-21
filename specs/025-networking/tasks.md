# Tasks: Spec 025, Networking: Cilium, Hubble and traces

Status: Approved, 2026-11-19. T001–T002 done.

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

## T006: Request metrics, traffic and flows in the observability provider

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

## T007: The tracing package

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

## T008: `serendipd` wiring

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

## T009: API

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

## T011: Console

- **Do:** the Requests, Flows and Traces sections; the topology overlay;
  Settings → Platform networking; the three condition types in the rule
  form; "not available" reasons everywhere.
- **Files:** `ui/src/screens/`, `ui/src/components/TopologyDiagram.tsx`,
  `ui/src/lib/`.
- **Verify:** `npm test` and `npm run build` in `ui/`; the gzipped JS size
  recorded.

## T012: Live check

- **Do:** spec criteria 1–11 and 14 on `serendip-t025`, criterion 2 on
  `serendip-t018`, in the browser and through the CLI and MCP; findings
  fixed.
- **Files:** `specs/025-networking/verification.md`.
- **Verify:** `verification.md` lists each criterion with its evidence
  and result.

## T013: Bench on the simulated T470

- **Do:** `bench-t470` with kindnet and with Cilium (idle, and under the
  observability load with HTTP traffic), then with tracing installed;
  the tracing package's requirements from the numbers; the edge-1
  default.
- **Files:** `profiles/edge-1/bench/results/2026-11-xx-spec025-*/`,
  `cmd/serendipd/packages.go` (requirements).
- **Verify:** spec criteria 12 and 13: control-plane idle within 10 MiB
  of spec 024's; node costs in the results summary.

## T014 [A]: Reviews

- **Do:** go-reviewer, contract-guardian, a security review of the
  scoping and the collector's identity, test-writer for gaps; findings
  fixed.
- **Files:** as found.
- **Verify:** each review's findings fixed or recorded with a reason;
  `go test ./...` and `go vet ./...` pass.

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
