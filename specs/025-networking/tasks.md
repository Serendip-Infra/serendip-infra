# Tasks: Spec 025, Networking: Cilium, Hubble and traces

Status: Approved, 2026-11-19. T001 done.

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

## T002: Core types

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

## T003 [P]: The `external` networking provider

- **Do:** detection from kube-system DaemonSets and node annotations,
  the facts plan decision 3 lists.
- **Files:** `providers/networking/external/`.
- **Verify:** table-driven tests (kindnet, Flannel on K3s, unknown, read
  failure); live on `serendip-t018`: kindnet reported, policies
  enforced.

## T004: The `cilium` provider

- **Do:** facts (version, Hubble settings, IPAM, ranges, drift from the
  pinned values); the embedded `values.yaml`; the L7 visibility ensurer
  (mirror, owner reference, skip-with-reason, interval); `FlowSource`.
- **Files:** `providers/networking/cilium/`.
- **Verify:** unit tests (mirroring keeps peers and ports, never adds
  one; untranslatable policies skipped); live on `t025`: a Component's
  policy created, deleted with the Component, and a refused peer still
  refused.

## T005: Install path

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
  so it can't be installed before); the notifier's ranges merge (RP-076).
- **Files:** `cmd/serendipd/`, `providers/kubernetes/ranges.go`.
- **Verify:** `go test ./cmd/... ./providers/kubernetes/...`;
  `serendipd` started on `t025` and on `serendip-t018` reports each
  CNI in its log.

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
