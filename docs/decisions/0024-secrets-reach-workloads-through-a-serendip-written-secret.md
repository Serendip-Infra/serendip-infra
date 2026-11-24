# ADR 0024: Secrets reach workloads through a cluster Secret that Serendip writes

Status: Accepted
Date: 2026-11-24

## Context

Spec 009 built the Secrets capability: a `Secret` resource whose value
lives in OpenBao and never in Serendip's store. Spec 011 built
Components. The two were never joined, so a database password could
reach a running Component only as a plain `env` value. That value then
sat in Serendip's store, the audit log, the platform's ConfigMaps and
the console.

Spec 026 joins them. Three facts shaped the choice (plan "Research",
checked live in T001):
- **Upstream's path is External Secrets Operator.** OpenChoreo 1.3.1's
  Workload `valueFrom.secretKeyRef` names a `SecretReference` CR, and
  the only thing that reads one is ESO's `ExternalSecret`, against a
  `ClusterSecretStore`. ESO ends in a plain cluster Secret anyway.
- **A component type can render a Secret,** but the rendered data sits
  in `RenderedRelease` in plaintext, readable by anyone who can read
  releases.
- **Nothing restarts pods when a Secret's data changes.** Environment
  variables are fixed at container start, so a rotation needs a changed
  pod template.

## Decision

### 1. Serendip writes one plain Kubernetes Secret per Deployment

- The application provider server-side applies `<component>-secret-env`
  in the Deployment's data-plane namespace. Its keys are the variable
  names and its values the resolved values, read from the Secrets
  capability by `DeploymentReconciler` in `serendipd`.
- The `serendip-service` component type adds it to the container with
  `envFrom` (after the plain-variables ConfigMap).
- **Only the Secret's name and a version reach OpenChoreo,** through
  the ReleaseBinding's reserved
  `componentTypeEnvironmentConfigs.serendipSecretEnv {name, version}`.
  The version is the first 16 hex characters of a sha256 over metadata
  only (variable, Secret ID, key and the Secret's `lastSyncedAt`). It
  reveals nothing about a value.
- **The version is a pod-template annotation**
  (`serendip.dev/secret-env-version`), so a new value rolls the pods.
- **Values are read only when the version changes.** A resync costs
  two cluster GETs per Deployment with secret variables, not an
  OpenBao read.
- Clean-up: `Undeploy`, an emptied list, and a Component delete each
  remove the Secret. Not-found counts as done.

### 2. Naming a Secret needs the right to read its value

A Component or Deployment that newly names a Secret (a new entry, or a
changed Secret, key or variable) needs `serendip/secrets/value/read`
at that Secret, as a build's `credentialSecret` does (403, audited). The
Secret must be in the Component's Project or above it (422). Azure
analogue: an App Service Key Vault reference, which resolves only when
the app's identity holds *Key Vault Secrets User*.

### 3. A failed delivery leaves the running pods alone

The `SecretEnvDelivered` condition reports the outcome. When it's False
(no value, a missing key, over 64 KiB, a release that predates secret
variables), the reconciler doesn't apply the release change that needs
it. When the Secrets capability is down, nothing in the cluster is
changed and running pods keep their values.

### 4. K3s encrypts Secrets at rest on new installs

`serendip-agent bootstrap` starts K3s with `--secrets-encryption` (both
CNIs). `GET /v1/secrets/status` reports `encryptionAtRest` as `on`,
`off` or `unknown`, from K3s's `k3s.io/encryption-config-hash` node
annotation. The key is on the same disk, so this protects a copied
datastore, not a stolen disk.

## Alternatives rejected

- **External Secrets Operator and `SecretReference`.** It adds a
  controller, a webhook and a cert controller (spec 026 criterion 12
  says no new pods), and a `ClusterSecretStore` that needs OpenBao's
  Kubernetes auth method. It produces the same plain Secret in the end.
  ADR 0020 made the same call for build credentials.
- **Rendering the Secret from the component type.** The value would sit
  in plaintext in `RenderedRelease`.
- **OpenBao's agent injector or CSI provider.** New pods, and they need
  workload identity, which is out of scope.
- **Reloader** (or any watcher that restarts pods on a Secret change).
  The template annotation does it with nothing installed.
- **One `secretKeyRef` per variable.** It would put a per-variable shape
  into OpenChoreo's objects. `envFrom` keeps one fixed shape, and name
  clashes with plain variables are refused up front, so `envFrom`
  ordering never decides a value.

## Consequences

- **Values rest in the cluster as plain Secrets.** Anyone with `get
  secrets` in the data-plane namespace, or a cluster-admin kubeconfig,
  can read them (RP-096). On agent K3s installs they're encrypted at
  rest; on kind and external clusters Serendip says `unknown`.
- **References aren't release-bound,** so a rollback keeps today's
  references (RP-097).
- **A rotation rolls every Deployment that uses the Secret,** and
  rotating any one of a Deployment's Secrets rolls it (RP-098).
- **Every Deployment rolls once on the upgrade to spec 026,** because
  the component type is part of the release hash. A Deployment pinned to
  an older release reports `NeedsNewRelease` if it gains secret
  variables. A downgrade drops the variables from running pods until the
  upgrade returns (RP-099).
- **OpenBao being sealed now blocks deliveries,** which is why spec 026
  T012 re-unseals it with the stored key (ADR 0009, amended; RP-094).
- `serendipd` idle memory is unchanged (spec 026 T010: within 1 MiB on
  the same host), and no pods were added.

## Revisit

- With workload identity, planned as spec 032 (2026-11-24): an opt-in
  file mount through OpenBao's CSI provider, and fetches by the
  application itself, beside this decision's environment variables. The
  Azure analogues are AKS's Key Vault CSI driver and workload identity
  with the Key Vault SDK. The agent injector stays rejected: a sidecar
  per pod, and AKS has no equivalent.
- When OpenChoreo's secret path stops needing ESO.
- When a cluster without secrets encryption becomes a common target
  (a managed cluster reports `unknown`).
