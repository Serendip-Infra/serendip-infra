# Tasks: Spec 026, Secrets into Components

Status: Approved, 2026-11-24 (the user's standing "full auto" instruction).

[P] = can run in parallel with the task before it. [A] = subagent-eligible.

## T001: Live checks of the research ✅

**Done as** (2026-11-24): every item checked on `serendip-t018` with a
hand-made probe (a `t026-service` copy of the type, plus a probe
Component, release and binding, all deleted afterwards), and in two
`rancher/k3s:v1.36.4-k3s1` containers. Answers in `testdata/t001-*`,
facts in `VERSIONS.md`, and `plan.md`'s "Checked live in T001".
- **Changed:** the CEL needs `dyn()` (decision 3); a pre-026 release
  silently ignores the property, so the provider reports
  `NeedsNewRelease` from the release's type snapshot (decision 12, a new
  condition row).
- **Confirmed:** delivery, no value in any OpenChoreo object, rotation
  by the annotation, the namespace from `ProjectReleaseBinding.status`,
  and K3s's `k3s.io/encryption-config-hash`.

- **Do:** on a scratch kind cluster `t026` with Serendip's platform
  installed (OpenChoreo 1.3.1), and in a `rancher/k3s` container, check
  what the plan relies on:
  - The `serendip-service` type with decision 3's changes:
    - the `envFrom` `secretRef` entry is added only when
      `serendipSecretEnv.name` is set;
    - the pod annotation is rendered, and changing the version through
      the ReleaseBinding's `componentTypeEnvironmentConfigs` rolls the
      pods;
    - a pod sees the hand-made Secret's keys as variables.
  - Whether the RenderedRelease, ReleaseBinding or Workload holds
    anything but the Secret's name and version.
  - Where the data-plane namespace can first be read: the
    ProjectReleaseBinding's status, or only the RenderedRelease.
  - Whether a running pre-026 release picks up the new type when only
    its ReleaseBinding config changes, or needs a new release
    (decision 12).
  - The K3s node annotation with `--secrets-encryption` on, and its
    absence with it off. Also `k3s secrets-encrypt status` output.
- **Files:** `providers/application/openchoreo/VERSIONS.md` (spec 026
  section), `specs/026-secrets-into-components/testdata/t001-*`,
  `plan.md` ("Checked live in T001", decisions changed).
- **Verify:** each item recorded as checked live with its answer file;
  `plan.md` updated where a decision changed.

## T002: Core types and validation ✅

**Done as** (2026-11-24): `core/resource/secretenv.go`, with
`ComponentSecretEnv`, `ValidateSecretEnv`, the clash rules across both
layers, `MergeSecretEnv`, `SecretEnvVersion` (length-prefixed metadata
hash) and the server-owned Secret edges. The new fields are on
`ComponentSpec`, `DeploymentOverrides` and `DeploymentStatus`, and
`ValidateDeploymentSpec` checks the overrides' own list.
contract-guardian passed. Its cheap notes are fixed (key length is in
bytes, and this task's text). Wiring the cross-field checks into the
handlers is T006's. A clash with a platform-injected variable (spec
025's tracing variables) goes in the risk register in T011.

- **Do:**
  - `ComponentSecretEnv`, `ComponentSpec.SecretEnv` and
    `DeploymentOverrides.SecretEnv`;
  - `ValidateSecretEnv` and the clash rules (decision 5);
  - the `depends-on` edges to Secrets in `ComponentRelationships`, and a
    Deployment equivalent;
  - `DeploymentStatus.SecretEnvVersion`;
  - the `SecretEnvVersion` hash helper.
  (The `SecretEnvDelivered` reasons live with the reconciler, in T003;
  `EncryptionAtRest` with its report, in T005.)
- **Files:** `core/resource/{app,deployment,secretenv}.go`, tests.
- **Verify:** `go test ./core/...`; `bash .claude/scripts/check-imports.sh`;
  contract-guardian.

## T003: Resolution and delivery in the reconciler ✅

**Done as** (2026-11-24):
- **`core/reconcile/secretenv.go`:**
  - `SecretEnvProvider` (optional, with `ErrSecretEnvPending` and
    `ErrSecretEnvUnsupported`);
  - `UseSecrets` on the Deployment reconciler;
  - `resolveSecretEnv`: values are read only on a version mismatch; the
    reasons of decision 11; a False result or a not-ready Secrets
    capability leaves the platform as it was;
  - `EnqueueSecretUsers`.
- **`Deploy` gained `secretEnvVersion`:** the version goes in the same
  ReleaseBinding apply, so there's one writer.
- **`SecretReconciler.OnValueWritten`.**
- **`lazy.go` forwards** the two calls.
- **Reviews:** contract-guardian and go-reviewer both caught one bug: a
  failed pass blanked `status.secretEnvVersion`, which would have
  stranded values on the platform when the last variable went. Fixed
  with a `held` version and a "resolved this pass" flag, so a removal
  the platform can't do yet is retried. Regression tests are in. Also
  fixed:
  - the lazy handle now answers `ErrSecretEnvUnsupported` (Unsupported,
    not a retried error);
  - provider errors are wrapped with the Deployment ID;
  - decoded values are cleared.
- **Left as designed:**
  - Not-delivered reasons wait for the value-write hook or resync,
    rather than requeueing early.
  - Ready goes False while secret variables can't be delivered (spec
    item 5).
  - The one platform GET per pass goes in the risk register (T011).
- **Wiring** `UseSecrets` and `OnValueWritten` in `serendipd` is T006's.

- **Do:**
  - Extend `DeploymentProvider` with `EnsureSecretEnv(ctx, comp, env, d,
    vars map[string]string, version string)` and `SecretEnvVersion(ctx,
    comp, env, d) (string, bool, error)` (`ErrProviderNotReady` when the
    platform isn't up).
  - In `DeploymentReconciler`:
    - merge the two lists and compute the version;
    - read values only on a version mismatch;
    - set the condition and the status version;
    - hold the release change while delivery is False (decision 11);
    - remove the Secret when the list becomes empty.
  - `SecretReconciler.WriteValue` enqueues dependent Deployments, and
    a Component's secret change enqueues its Deployments (decision 4).
  - Test that no error, condition or log line carries a value.
- **Files:** `core/reconcile/{deployment,secrets,application}.go`,
  tests; provider fakes updated.
- **Verify:** `go test ./core/...` (table tests: delivered, no value,
  missing key, too large, provider not ready, unchanged → no read, no
  write; value write → enqueue); check-imports; contract-guardian;
  go-reviewer.

## T004: The OpenChoreo provider [A]

- **Do:**
  - Template changes (decision 3).
  - The reserved `serendipSecretEnv` property, stripped from reported
    schemas and refused in user `config`.
  - `EnsureSecretEnv`: server-side apply of `<cmp>-secret-env` with the
    version annotation, then the ReleaseBinding property.
  - `SecretEnvVersion` read.
  - `Undeploy` and the empty-list clean-up deleting it; not-found is
    done.
  - `NeedsNewRelease` if T001 says so.
- **Files:** `providers/application/openchoreo/{defaults,deployments,schema}.go`,
  tests, `VERSIONS.md`.
- **Verify:** `go test ./providers/...`; `go build ./core/... ./api/...`
  with the provider deleted (scratch copy); contract-guardian; go-reviewer.

**Done as** (2026-11-24):
- **`providers/application/openchoreo/secretenv.go`:**
  - `SecretEnvVersion` reads the version annotation on
    `<cmp>-secret-env`. It answers `ErrSecretEnvPending` while the
    project binding has no `status.namespace`.
  - `WriteSecretEnv` server-side applies the Secret. An apply 404 is
    pending too. Empty variables delete the Secret, and not-found counts
    as done.
  - API error bodies are dropped (`valueFreeError` keeps only the status
    code), so no error can echo a value.
- **The type (`defaults.go`):**
  - the reserved `serendipSecretEnv {name, version}` property;
  - the pod annotation `serendip.dev/secret-env-version`;
  - the `dyn()` `envFrom` form that T001 verified.
- **`Deploy`:**
  - sets the property in the same binding apply, only when there's a
    version. Deployments without secret variables keep their binding
    unchanged;
  - drops a user's value for the property;
  - returns `NeedsNewRelease` before the binding apply when the
    release's type snapshot lacks the property. A pinned release gets
    "deploy a newer release". The current release (a stale installed
    type) gets "waiting for it to be updated".
- **`Undeploy`** deletes the Secret first, while the project binding
  still names the namespace.
- **`ConfigSchemas`** strips the property, so the validator refuses it
  in a user's `config`.
- **Reviews:**
  - contract-guardian passed, and check-imports is clean.
  - go-reviewer found no blockers. Fixed from its findings:
    - the status error stays in the error chain;
    - a distinct message for a stale installed type;
    - a wrapped Undeploy error;
    - tests for a 404 on apply, an unannotated Secret, a bad status, no
      namespace on Undeploy, `withoutReserved` and `bindingConfig`.
- **For T011's risk register:**
  - an apply 404 reads as pending for good if the path were ever wrong
    (resync bounds it);
  - a pass costs two GETs per Deployment with secret variables (the
    project binding and the Secret);
  - clearing value buffers is best effort.
- **Verified:**
  - `go test ./providers/...` passes;
  - `go build ./core/... ./api/...` passes on a scratch copy without the
    provider.

## T005: Encryption at rest [P] ✅

**Done as** (2026-11-24):
- **Agent:** `k3sServerArgs` always starts with `--secrets-encryption`,
  so Flannel installs now set `INSTALL_K3S_EXEC=server
  --secrets-encryption` too. `TestInstallEnv_CNI` checks both CNIs.
- **Core (`core/reconcile/secrets.go`):** `EncryptionAtRest`
  (`on`/`off`/`unknown`), `EncryptionReport {encryptionAtRest, reason,
  message}`, the optional `EncryptionReporter`, and
  `EncryptionNotReported()`.
- **Provider (`providers/secrets/openbao/encryption.go`):**
  `WithCluster` and `EncryptionAtRest`, one node LIST. The outcomes are
  in the table in `secrets.md`: `EncryptionConfigured`,
  `EncryptionNotConfigured`, `ServersDisagree` (unknown) and
  `NotReported`. A failed LIST is an error.
- **`serendipd`:** `wiring.go` calls `WithCluster(client)`, and
  `lazySecrets` forwards the report, pulled forward from T006 after a
  review finding. Without a reporter it answers `NotReported`.
  `GET /v1/secrets/status` stays in T006.
- **Docs:** `secrets.md` "Encryption at rest in the cluster",
  `operations.md` "Secrets encryption at rest" (K3s's enable steps,
  marked as not yet run), and `bootstrap-agent.md`.
- **Reviews:** contract-guardian and go-reviewer both passed. Fixed:
  - the doc references that pointed nowhere;
  - the `on` message, which now says it covers Secrets written since
    encryption was turned on;
  - the lazy handle, which didn't forward the report.
- **Left as designed:**
  - `WithCluster` sets the provider's field once at wiring time, like
    `OnValueWritten`.
  - The node LIST isn't cached, because it runs only when asked.
- **For the risk register (T011):**
  - The annotation shows encryption was configured at start, not that
    older Secrets were re-encrypted.
  - A later `k3s secrets-encrypt disable` may leave the annotation in
    place (not checked).
  - The key is on the same disk.
- **Verified:** `go test ./agent/... ./providers/... ./core/...
  ./cmd/serendipd/` passes, as do `go vet`, `gofmt` and check-imports.
  The flag is shown by `TestInstallEnv_CNI`.

- **Do:**
  - The agent adds `--secrets-encryption` to K3s's server args (both
    CNIs).
  - A small `EncryptionAtRest` report in `providers/secrets/openbao`,
    built on the Kubernetes client: the node annotation means `on`, a
    K3s node without it means `off`, anything else means `unknown`.
    It's exposed through an optional interface on the secrets provider,
    like `PackageUsage`.
- **Files:** `agent/bootstrap/{cni,install}.go`,
  `providers/secrets/openbao/`, `core/reconcile/secrets.go`, tests.
- **Verify:** `go test ./agent/... ./providers/... ./core/...`; the agent's
  install-args test shows the flag; check-imports.

## T006: API ✅

**Done as** (2026-11-24):
- **`api/secretenv_check.go`, `secretEnvChecker`:**
  - It acts only on newly named entries: a new `(env, secret, key)`, so a
    changed key or variable counts.
  - The value-read check comes first: 403, audited, with the allowed
    checks in the policy evidence.
  - Then the Secret must exist (422), and sit in the Project or above it
    (422, `inProject` over the graph's ancestors).
- **Components (`topology_handler.go`):**
  - `ValidateComponentSecretEnv` (400), then the checker.
  - On update, every Deployment of the Component (graph dependents) is
    checked with `ValidateDeploymentSecretEnv`. A clash is 409, naming
    the Deployment only to a caller who may read it.
  - When `secretEnv` changes, the `WithSecretEnvChanged` hook runs with
    a detached context.
- **Deployments (`deployment_handler.go`):** `coherent` runs
  `ValidateDeploymentSecretEnv` (400) and then the checker. It now
  returns the context, so the evidence reaches the audit row. Promote
  copies no overrides, so it checks nothing new.
- **One lock for Component and Deployment writes**, shared between the
  two handlers, so the cross-layer checks can't race. A concurrent test
  didn't hit the window even without the lock, so it was dropped. The
  lock stands on review.
- **The reserved config key** is the provider's to know (api/ stays
  neutral): its reported schema leaves the key out, so the ordinary
  configuration check refuses it, and `Deploy` overwrites it (T004's
  tests).
- **The delete guard** is the store's existing `checkNotInUse` over the
  new `depends-on` edges. It's tested here.
- **`GET /v1/secrets/status`** (`secrets_status.go`) answers `{ready,
  encryptionAtRest, reason, message}`:
  - no reporter: `NotConfigured`;
  - not ready: `NotReady`;
  - a failed cluster read: ready, `unknown`, `CheckFailed`, with the
    cause logged, not answered.
  - The authz rule is `serendip/secrets/read` at `/`. The catalogue
    entry is `secrets_status` (`serendip secrets status`), listed in
    `docs/cli-and-mcp.md`.
- **`serendipd`:**
  - `UseSecrets` on the Deployment reconciler;
  - `OnValueWritten` → `EnqueueSecretUsers`, detached context;
  - `enqueueComponentDeployments` for the hook, which logs a failed read;
  - `WithSecretsStatus(lazySecrets)`.
- **Tests:** `api/spec026_acceptance_test.go` covers criterion 6
  (authorization), 7 (the delete guard), 10 (validation, 13 cases) and
  the status route. `cmd/serendipd/secretenv_wiring_test.go` covers the
  hook. A mutation of the scope rule fails the tests.
- **Reviews:** contract-guardian passed and go-reviewer found no
  blockers. Fixed: the shared lock, detached hook contexts, the hook's
  log line, graph dependents instead of listing every Deployment, the
  ctx passed into the project lookup, slice aliasing (code and tests),
  the 409's Deployment name, and table cases for the wiring test.
- **Left as designed:**
  - The status route is read at `/` (decision 9), so a Project-only
    user's console shows no banner.
  - `CheckFailed` keeps `ready: true`, because the capability is up.
  - A reordered list counts as a change (one extra pass).

- **Do:**
  - Validation on Component and Deployment create/update: the 400s, the
    Project-or-above rule (422), and a Secret that doesn't exist (422).
  - The value-read check on newly named Secrets (403, audited), as with
    `credentialSecret`.
  - Refuse the reserved config key.
  - The Secret delete guard covering the new edges (409 naming users).
  - `GET /v1/secrets/status`, with its authz entry.
  - The catalogue entries.
  - `serendipd` wiring (the status reporter, the enqueue hook).
- **Files:** `api/{handler,deployment_handler,secret_handler,authz,build_handler}.go`
  (shared check moved to one helper), `api/catalog/`, `cmd/serendipd/`,
  tests, `api/spec026_acceptance_test.go`.
- **Verify:** `go test ./api/... ./cmd/...` (criterion 6's and 10's unit
  tests named in `verification.md`); contract-guardian; go-reviewer.

## T007: CLI and MCP [P]

- **Do:**
  - `--secret-env` and `--remove-secret-env` on component and
    deployment create/update;
  - `serendip secret status`;
  - the MCP tools from the catalogue;
  - `docs/cli-and-mcp.md`.
- **Files:** `cmd/serendip/`, `api/catalog/`, `docs/cli-and-mcp.md`.
- **Verify:** `go test ./cmd/... ./api/...` (the catalogue parity test
  passes).

## T008: Console

- **Do:**
  - The Component page's "Secret variables" table and add form;
  - the same on the Deployment overrides form;
  - the `SecretEnvDelivered` condition shown;
  - the encryption banner on the Secrets page;
  - no value fetched anywhere.
- **Files:** `ui/src/` (Component, Deployment and Secrets pages, API
  types), tests.
- **Verify:** `npm test` and `npm run build` in `ui/`; the console checked
  live in T009.

## T009: Live verification

- **Do:** criteria 1–11 on `t026` (kind) and, for criterion 8, an agent
  K3s install in a `rancher/k3s` container. Search for the value
  everywhere criterion 2 lists. Record the results in `verification.md`,
  and fix what's found.
- **Files:** `specs/026-secrets-into-components/verification.md`, fixes.
- **Verify:** every criterion 1–11 recorded PASS (or a fix and re-check)
  with its evidence.

## T010: Lightweight check

- **Do:** criterion 12. `serendipd` idle RSS with a Deployment using
  secret variables, against spec 025's baseline on the same host (A/B,
  as spec 024 did), and a check that no new pods run.
- **Files:** `profiles/edge-1/bench/results/2026-11-*-spec026-*/`,
  `verification.md`.
- **Verify:** RSS within 2 MiB of the same-host baseline; pod list
  unchanged apart from the workloads.

## T011: Docs and close-out

- **Do:**
  - ADR 0024 (secrets reach workloads through a Serendip-written cluster
    Secret);
  - updates to `application-platform.md`, `secrets.md`,
    `authorization.md`, `operations.md` (including enabling encryption
    on an existing K3s), `bootstrap-agent.md`, `cli-and-mcp.md`,
    `docs/build-vs-adopt.md` and the risk register RP-094 onward;
  - `docs/PROGRESS.md`;
  - the epic body rewritten;
  - docs-keeper.
- **Files:** `docs/`.
- **Verify:** docs-keeper finds no stale statement (or its findings are
  fixed); every RP named in `plan.md` exists.
