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

## T005: Encryption at rest [P]

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

## T006: API

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
