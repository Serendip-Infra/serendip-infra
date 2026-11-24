# Plan: Spec 026, Secrets into Components

Status: Approved, 2026-11-24 (under the user's standing "full auto"
instruction for this spec; open to change at the user's review).
Amended 2026-11-24 with T001's findings.

Spec: `spec.md`. Epic #67.

## Checked live in T001 (2026-11-24)

On `serendip-t018` (kind, OpenChoreo 1.3.1) with a hand-made probe, and in
two `rancher/k3s:v1.36.4-k3s1` containers. Answers in `testdata/t001-*`
and `providers/application/openchoreo/VERSIONS.md` (spec 026).

- **Decision 1 and 3 hold:** `envFrom` with the Serendip-written Secret
  delivers the value, and a changed version annotation rolls the pods.
  Searching every RenderedRelease, ReleaseBinding, ComponentRelease,
  Workload, Component and the data-plane ConfigMaps for the value found
  nothing.
- **The CEL needs `dyn()`** around `configurations.toContainerEnvFrom()`
  before the list concatenation (decision 3), or the type is refused at
  admission.
- **The namespace is in `ProjectReleaseBinding.status.namespace`**
  (decision 3), so the provider doesn't wait for a RenderedRelease.
- **A pre-026 release silently ignores the new property** (decision 12):
  the binding syncs, and no secret reaches the pods. So the provider
  checks the running release's type snapshot and reports
  `NeedsNewRelease`.
- **K3s** (decision 9): with `--secrets-encryption` the node has
  `k3s.io/encryption-config-hash` (`start-<sha>`), and `k3s
  secrets-encrypt status` shows `Enabled` with an AES-CBC key. Without
  it there's no annotation, and the status says `Disabled`. K3s nodes
  are told apart by the label `node.kubernetes.io/instance-type=k3s`.

## Research (2026-11-24, oss-researcher; confirmed live in T001, above)

OpenChoreo v1.3.1, read from source at the tag (types and the sample),
not run:

- **Workload `env[]`** is `{key, value | valueFrom}`, and `valueFrom` has
  only `secretKeyRef {name, key}` (no namespace). ReleaseBinding
  `workloadOverrides.container.env[]` reuses the same type.
- **What `secretKeyRef.name` points at** is a `SecretReference` CR
  (`data[].secretKey → remoteRef{key, property, version}`,
  `refreshInterval`). The only consumer in every sample and doc is
  **External Secrets Operator**: the sample `service` type renders an
  `ExternalSecret` per container against `dataplane.secretStore` (a
  `ClusterSecretStore`) and adds it through
  `configurations.toContainerEnvFrom()`.
- **A component type can render any kind**, a `Secret` included,
  because `RenderedRelease.spec.resources[].object` is schemaless. But
  that object **holds the rendered data in plaintext** in the control
  plane, readable by anyone who can read RenderedReleases.
- **Nothing restarts pods when a referenced Secret's data changes.**
  Environment variables are fixed at container start, so a rollout needs
  a changed pod template, for example an annotation.

## Decisions

### 1. Serendip writes the cluster Secret; no ESO, no rendered Secret

For each Deployment with secret variables, the application provider
server-side applies **one plain Kubernetes `Secret`**,
`<cmp-name>-secret-env`, in that Deployment's data-plane namespace. Its
keys are the variable names and its values are the resolved values. The
component type's container gets
`envFrom: [{secretRef: {name: <it>}}]` added after the env ConfigMap.
Only the Secret's **name** and a **version** reach OpenChoreo's objects,
through the ReleaseBinding (decision 3).

- **Rejected: upstream's path** (Workload `valueFrom.secretKeyRef` →
  `SecretReference` → ESO `ExternalSecret`). It adds ESO's controller,
  webhook and cert controller (criterion 12 says no new pods), plus a
  `ClusterSecretStore` that needs OpenBao's Kubernetes auth method (out
  of scope, spec). It also produces the same plain cluster Secret in the
  end. ADR 0020 made the same call for builds.
- **Rejected: rendering the Secret from the component type.** The value
  would sit in `RenderedRelease` in plaintext, which fails criterion 2.
- **Rejected: OpenBao's agent injector or CSI provider.** These are new
  pods, and they need workload identity (spec: out).
- **Why `envFrom` and not one `secretKeyRef` per variable:** the
  template gets one fixed shape no matter how many variables there are,
  so nothing per variable reaches OpenChoreo's objects. Name clashes
  with plain variables are refused up front (decision 5), so `envFrom`
  ordering never decides a value.

### 2. Who reads the value, and when

`DeploymentReconciler` resolves secret variables. It merges the
Component's `secretEnv` with the Deployment's `overrides.secretEnv`, and
an override replaces by variable name. It then computes the **version**,
the first 16 hex characters of sha256 over the sorted
`(variable, Secret ID, key, Secret.status.lastSyncedAt)` tuples. That is
built from metadata only, so it reveals nothing about a value, and it
changes on every value write (spec 009 sets `lastSyncedAt` on each
write). The reconciler passes the variables and version to a new
`DeploymentProvider` call, `EnsureSecretEnv`.

- **Values are read only when the version changes.** The provider
  reports the version it last wrote (an annotation on the cluster
  Secret). When that matches and the Secret exists, the reconciler skips
  `SecretsProvider.ReadValue` and writes nothing (criterion 11). So a
  resync costs one cluster GET per Deployment, not an OpenBao read.
- **The value of a key:** a string is used as is. A number, bool, list
  or object is JSON-encoded, and that is documented. A missing key is
  condition reason `SecretKeyNotFound`.
- **Values go from `core/reconcile` straight to the provider, in
  memory.** They're never logged, put in an error, an event or a
  condition message. Messages name the Secret ID and key only. A
  dedicated test asserts that no error string contains a value.
- **The 64 KiB total cap** on the resolved Secret stays well under
  Kubernetes' 1 MiB Secret limit. Going over is reason
  `SecretEnvTooLarge`.

### 3. How the pods roll, and how the template knows

The component type's `environmentConfigs` schema gains a property,
`serendipSecretEnv {name, version}`, defaulting to empty strings. The
Deployment provider sets it in the ReleaseBinding's
`componentTypeEnvironmentConfigs`, next to the user's own `config`. The
template:

- adds the `envFrom` entry only when `name` isn't empty (the CEL wraps
  `configurations.toContainerEnvFrom()` in `dyn()` to concatenate, T001);
- adds the pod-template annotation `serendip.dev/secret-env-version:
  ${version}`, so a changed version rolls the pods (criterion 4).

**The property is reserved.** The provider removes it from the schema it
reports (`GET` schema routes, the console's config form). Spec 020's
configuration validator refuses a user's `config.serendipSecretEnv`
(422), so it can't be set or overwritten by hand.

**Order inside `EnsureSecretEnv`:** apply the cluster Secret first, then
the ReleaseBinding's version. New pods therefore never start against a
Secret older than their annotation.

- **On a first deploy** the data-plane namespace may not exist yet,
  because the ProjectReleaseBinding makes it. `Deploy` already applies
  that binding first, and its `status.namespace` names the namespace
  once `NamespaceReady` (T001). Before then, the pass reports
  `SecretEnvPending` and the next early re-check writes the Secret.
  Kubernetes retries a container whose `envFrom` Secret is missing, so
  this only delays the first start.

### 4. Rotation is noticed by an event, with the resync as backstop

`SecretReconciler.WriteValue`, after its store update, asks the engine
to `Enqueue` every Deployment that resolves that Secret. Those are its
`depends-on` dependents (Deployments directly, and Components, whose
Deployments are then enqueued). The periodic resync covers anything
missed (a restart between write and enqueue), because the version
compares on every pass.

- **A Component edit that changes `secretEnv`** also enqueues the
  Component's Deployments. Today a Component change reaches Deployments
  through a new release. T003 checks whether a path already exists and
  adds one if not.

### 5. Shape and validation (core)

```go
// ComponentSecretEnv sets one environment variable from one key of a
// Secret's value.
type ComponentSecretEnv struct {
    Env    string `json:"env"`    // the variable's name
    Secret string `json:"secret"` // the Secret's ID
    Key    string `json:"key"`    // a key of the Secret's value
}
```

- `ComponentSpec.SecretEnv []ComponentSecretEnv` and
  `DeploymentOverrides.SecretEnv []ComponentSecretEnv`.
- **Validation** (`resource.ValidateSecretEnv`, 400): `env` is a
  variable name (the existing `envNameRE`), `key` is 1–256 characters
  with no control character, `secret` is an ID, and there are at most
  64 entries per list.
- **Clashes are refused:** a duplicate `env` in one list, an `env` also
  set in plain `env`, or one also used by a dependency's `env`. Across
  the two layers, an override's secret variable must not be a plain
  variable in either layer, and an override's plain `env` must not
  shadow a Component secret variable. So "which one wins" never depends
  on ordering.
- **Not release-bound.** Secret references are read from the current
  Component and Deployment at reconcile time. They aren't part of a
  `ComponentRelease`, so a rollback doesn't revert them. That's
  deliberate, since a reference names no value and a rollback that
  restored an old password reference would surprise people more. It is
  documented, and recorded as an RP.
- **Graph:** each named Secret is a `depends-on` edge from the Component
  or Deployment, kept by the server like dependencies' edges
  (`ComponentRelationships`). A delete of a Secret with dependents
  answers 409 naming them. That is the existing spec 024 guard,
  extended to the new edges.

### 6. Which Secrets a Component may name (spec open question 3)

The Secret has to sit **inside the Component's Project, or at a scope
above it** (the root, an Organization or Team on the Project's path).
Otherwise the answer is 422, "the Secret must be in this Project or
above it". There are two reasons:

- Deployments already stay inside one Project, ADR 0019's
  "subscription".
- A reference from another Project would let one Project's deletes be
  blocked by another's Component.

A Secret above the Project is fine, because deleting that scope deletes
the Project too. The Azure analogue is Key Vault references across
resource groups in one subscription.

### 7. Authorization

This is the same check as `credentialSecret` (`api/build_handler.go`).
On Component create/update and Deployment create/update, for each
Secret that's **newly named**, the caller needs
`serendip/secrets/value/read` at that Secret. Otherwise the answer is
403 and the denial is audited. "Newly named" covers a new entry and an
entry whose `secret` changed. Changing only `env` or `key` on the same
Secret also counts, because a key choice is a value choice. Removing an
entry and any unchanged entry need nothing more (criterion 6). The
Azure analogue is an App Service Key Vault reference, which resolves
only when the app's identity holds *Key Vault Secrets User*.

Promotion copies no overrides, so it needs nothing new.

### 8. Clean-up

- `Undeploy` deletes `<cmp>-secret-env` in that namespace with the
  ReleaseBinding.
- A Deployment with no secret variables left has its cluster Secret
  deleted on the next pass, and the ReleaseBinding's property emptied.
- Deleting the namespace also removes it, which covers a whole
  Application.
- Each step is idempotent, because not-found counts as done.

### 9. Encryption at rest in the cluster

- **The agent** adds `--secrets-encryption` to K3s's server flags (both
  CNIs). K3s then encrypts Secrets with AES-CBC through an
  `EncryptionConfiguration` it manages under
  `/var/lib/rancher/k3s/server/cred/`. The key sits on the same disk,
  which `secrets.md` says plainly: it protects a copied datastore, not a
  stolen disk.
- **Detection:** K3s marks server nodes with the annotation
  `k3s.io/encryption-config-hash` when secrets encryption is on (checked
  in T001). A node with the label `node.kubernetes.io/instance-type=k3s`
  is a K3s node. The
  application provider has no part in this. It is a `SecretsProvider`
  side report built on the `providers/kubernetes` client and kept out of
  `core/` names: `core` sees `EncryptionAtRest` with values `on`, `off`
  or `unknown`, plus a reason.
  - **A K3s node without the annotation** is `off`.
  - **A non-K3s cluster** (kind, any `external` one) is `unknown`,
    reason "the cluster doesn't say".
- **Surface:** `GET /v1/secrets/status` answers `{ready,
  encryptionAtRest, reason, message}`, under action
  `serendip/secrets/read` at `/`. The console's Secrets page shows it in
  a banner, the CLI prints it from `serendip secret status`, and MCP has
  `secrets_status`.
- **Existing installs** aren't changed. `operations.md` gives K3s's
  `k3s secrets-encrypt enable` steps.

### 10. Interfaces

- **API:** no new routes for references, because they live in the
  Component and Deployment specs. Responses show references only.
  `GET /v1/deployments/{id}` gains `status.secretEnvVersion` and the
  `SecretEnvDelivered` condition. There is one new route,
  `GET /v1/secrets/status` (decision 9).
- **Catalogue (`api/catalog`)**, and through it the CLI and MCP:
  - `serendip component update --secret-env NAME=SECRET_ID:KEY`
    (repeatable) and `--remove-secret-env NAME`;
  - the same on `serendip deployment create|update`;
  - `serendip secret status`;
  - MCP tools from the same entries.
  The parity test (`api/catalog_parity_test.go`) covers them.
- **Console:**
  - the Component page gets a "Secret variables" table (variable, Secret
    name with a link, key, Remove) and an add form that picks the Secret
    from the ones the caller can read in scope, then types the key;
  - the Deployment overrides form gets the same table, labelled "for
    this environment";
  - the Deployment shows the `SecretEnvDelivered` condition;
  - the Secrets page shows the encryption banner;
  - no value is ever fetched.

### 11. Conditions

There is one new Deployment condition, **`SecretEnvDelivered`**,
present only when the merged list isn't empty:

| Status | Reason | When |
|---|---|---|
| True | `Delivered` | cluster Secret and version are in place |
| False | `SecretHasNoValue` | a named Secret has never been written |
| False | `SecretKeyNotFound` | the key isn't in the value |
| False | `SecretNotFound` | the Secret is gone (shouldn't happen: delete guard) |
| False | `SecretEnvTooLarge` | over 64 KiB |
| False | `NeedsNewRelease` | the running release predates secret variables (decision 12) |
| Unknown | `ProviderNotReady` | Secrets or the platform not ready; **nothing is changed in the cluster**, running pods keep their values |
| Unknown | `SecretEnvPending` | waiting for the data-plane namespace |

A Deployment is Ready only when this is True or absent (spec item 5).
When it is False, the reconciler does **not** apply the release change
that would need it. The ReleaseBinding keeps its last good state, so a
typo in a key doesn't take down running pods. That's the same choice
spec 022 made for failed builds.

### 12. Migration

No store migration: both new fields are optional and empty. The
component type change reaches clusters on the next platform-defaults
pass (spec 012's update path). A running release snapshots the old type,
which has no `envFrom` secret entry and no annotation. So the first
Deployment change after upgrading picks up the new type, and adding a
secret variable is such a change (it changes `componentTypeEnvironmentConfigs`).
**T001 found that a release pins its type**, and a pre-026 release
silently ignores the property. So the provider checks the running
release's type snapshot for `serendipSecretEnv`. When it's missing,
`EnsureSecretEnv` reports reason `NeedsNewRelease` with the message
"this release predates secret variables: deploy a newer release". In
practice a Component gets a new release on its next reconcile after the
type changes, because the type snapshot is part of the release hash. So
only Deployments pinned to an old release see this.

## What it costs

- **`serendipd`:** a few small types, one extra cluster GET per
  Deployment per resync, and no new goroutines. Target: idle RSS within
  2 MiB of spec 025's 33.2 MiB (criterion 12).
- **Cluster:** one small Secret per (Component with secrets ×
  Environment), and no new pods. K3s secrets encryption costs a few
  microseconds of AES per Secret read or write. That's negligible on
  the T470.

## Risks to register (RP-094 onward)

- **Values rest in the cluster's store** as plain Secrets. They are
  encrypted at rest only on agent K3s installs, with the key on the same
  disk. Anyone with `get secrets` in the data-plane namespace, or a
  cluster-admin kubeconfig, can read them. This is the same class as
  RP-050 (build credentials).
- **Secret references aren't release-bound** (decision 5), so a
  rollback keeps the current references.
- **Delivery reads aren't audited** (spec: out; `secrets.md` deferral).
- **A rotation restarts every pod** of every Deployment using the
  Secret. That's a rolling restart, not zero-downtime for a single
  replica.
- **One version for many Secrets:** rotating any one of a Deployment's
  Secrets rolls it.

## Build vs adopt

- **Builds:** the delivery (decision 1).
- **Adopts:** K3s's own secrets encryption.
- **Rejects:** ESO and `SecretReference` (again), a rendered Secret,
  OpenBao's injector or CSI provider, and Reloader (rollouts come from
  the template annotation instead).

## Tasks

See `tasks.md`.
