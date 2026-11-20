# Capability model

Serendip's domain speaks in capabilities, never products (constitution 1).
This document is the capability catalog: what capabilities exist, the spec
format that defines one, and the default provider chosen for each. Vendor
names below are defaults and examples — never the organizing principle.

## Capability spec format

A capability declaration (registered in the Capability Registry, see
`provider-model.md`) has this shape:

```yaml
capability:
  name: identity
  required: true          # can the platform run at all without it?
  providers:
    - thunder
    - keycloak
    - external-oidc
  default: thunder
```

Fields:
- **name** — the capability, e.g. `identity`, `secrets`, `observability`.
- **required** — whether a Serendip install is functional without it.
- **providers** — registered implementations available for this capability.
- **default** — which provider a fresh install picks absent user choice.

Users change `default` per Environment or Organization without touching
Serendip's own code — that's the entire point of the registry (constitution
1: "every capability has ... at least one provider ... and an `external`
escape hatch").

## Capability catalog

| Capability | Required | Default provider | Alternative / optional providers |
|---|---|---|---|
| Identity | Yes | ThunderID (ADR 0006; `identity.md`, built in spec 015) | Keycloak, external OIDC/Entra ID (the OIDC verifier is provider-neutral; no external-oidc registration yet) |
| Secrets & PKI | Yes | OpenBao | External Secrets Operator + external vault |
| Certificates | Yes | cert-manager | — |
| Networking (CNI) | Yes | Cilium | — |
| Gateway / ingress | Yes | kgateway (ADR 0011; `gateway.md`) | WSO2 API Manager (advanced API management) |
| Compute (Kubernetes) | Yes | K3s (ADR 0001) | RKE2 (future, hardened profiles) |
| Cluster management | No | Rancher | — (native single-cluster mgmt below 3+ nodes) |
| Application platform | Yes | OpenChoreo (ADR 0005) | KubeVela + Crossplane + Argo (evaluated, not selected) |
| Infrastructure provisioning | No | Crossplane | Terraform/OpenTofu |
| GitOps delivery | Yes | Argo CD / Argo Workflows | — |
| Builds from source (`build`, optional package) | No | OpenChoreo's workflow plane (Argo Workflows) with `registry:2` (spec 022, ADR 0020) | — (buildpacks, external registries later) |
| Storage (primary) | Yes | local-path → Longhorn | External CSI (ADR 0002) |
| Storage (backup) | No | S3-compatible / NFS | — (never counted as primary redundancy) |
| Database | No | CloudNativePG (PostgreSQL) | Embedded, managed/external PostgreSQL |
| Observability (telemetry collection) | Yes | OpenTelemetry | — |
| Observability (backend) | Yes | OpenObserve (logs) and plain Prometheus (metrics), through OpenChoreo's adapters (ADR 0021) | SigNoz for larger installs; OpenSearch rejected for edge-1 |
| Ticketing / ITSM | No | Native Serendip Incidents | Zammad, GLPI, ServiceNow, Jira |
| API analytics | No | Native (via kgateway/Envoy + OTel) | Moesif adapter |
| Compute (VM) | No | *not v1* | Harvester (KubeVirt) — see `deployment-profiles.md` |
| AI inference | Yes | *unresolved* | See `ai-operations.md` open questions |

Capabilities marked "Required: Yes" must have at least a working default at
`edge-1` (see `deployment-profiles.md`); "No" capabilities are enabled only
when the topology or the user justifies them (e.g. cluster management below
3 nodes has nothing to manage).

## Native vs. external split (operations-adjacent capabilities)

A few capabilities are intentionally Serendip-native rather than delegated
to a single external product, because they are the cross-cutting layer that
makes the rest of the platform coherent:

| Native (built) | External (optional integration) |
|---|---|
| Alerting, SLO/SLI, error budgets | — (visualized via observability backend, semantics owned natively). **Alerting is built (spec 024, ADR 0022):** `serendipd` evaluates rules against the Observability capability (`ObservabilityProvider.EvaluateCondition`) and sends Telegram, email and signed webhooks through `core/notify`. There's no Notification capability or provider registry entry: these are protocols, not installed products; a paid channel (SMS, voice) or a second chat protocol arrives as a provider. Other tools are reached through the webhook (n8n, Node-RED, Apprise API). SLOs aren't built yet |
| Incident management, runbooks, postmortems | ServiceNow, Jira, PagerDuty, Zammad, GLPI |
| Change intelligence, resource/service graph | — |
| AI diagnosis/recommendation/approval | — |

This split is deliberate: Serendip owns the *semantics* (what an Incident is,
what counts as a change, how SLO burn rate is computed) while letting the
*storage/UI* of tickets be swapped. See `operations.md`.

## Bring your own capability

Every capability plans for an `external` provider: point Serendip at
something the user already runs (an existing Keycloak, an existing Grafana
stack, an existing ServiceNow instance) instead of the bundled default.
Serendip's job in that case shrinks to correlation/abstraction rather than
hosting the implementation.

## Open questions

- ~~Observability backend default~~ — resolved: ADR 0021, measured in
  spec 023 T003 on a simulated T470. OpenObserve for logs and plain
  Prometheus for metrics. On top of the platform, idle and then under
  load:
  - this setup measured +832 MiB and +1,028 MiB with the metrics module
    as shipped, and should be about 350–400 MiB idle with plain
    Prometheus;
  - OpenSearch measured +1,609 MiB and +1,659 MiB;
  - SigNoz measured +1,801 MiB and +2,068 MiB.

  T013 confirms the figure on the reference laptop.
- **AI inference backend** (local model runtime + model choice) was only
  discussed speculatively (e.g. Ollama + a Qwen-class model as one example)
  and was never settled. See `ai-operations.md`.
- ~~Application platform (OpenChoreo) and Identity (Thunder) defaults lacked
  ADRs~~ — resolved: see ADR 0005 and ADR 0006. Both carry explicit
  risk/fallback conditions (governance, resource footprint, maturity)
  rather than being unconditional defaults.
