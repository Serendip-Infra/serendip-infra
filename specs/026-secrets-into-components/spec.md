# Spec 026: Secrets into Components

Status: Draft, 2026-11-24.

Epic #67. Spec 009 built the Secrets capability (a `Secret` resource whose
value lives in the provider, never in Serendip's store), and spec 011
built Components, but the two were never joined:
`application-platform.md` lists "Secret injection into Components" under
"Not done", and the platform's component type dropped upstream's secret
handling. A real application with a database password, an API token or a
signing key can only get it today by putting it in plain `env`, which
lands in Serendip's store, the audit log, the platform's objects and the
console in clear.

## Problem

- **No way to give a running Component a secret.** `ComponentSpec.env`
  and `DeploymentOverrides.env` are plain strings. Anything put there is
  stored, audited (whole-resource snapshots), shown to every Reader and
  copied into the platform's ConfigMaps.
- **Secrets differ per environment, Components don't.** A release is
  promoted unchanged; the development and production database passwords
  are not the same. A secret has to be choosable per Environment, the way
  variables already are (spec 020 overrides).
- **Changing a secret has no effect on what runs.** Even once a Secret
  reaches a workload, rotating its value must reach the pods, and the
  operator needs to see whether it did.
- **Naming a Secret is a way to read it.** A Component that prints its
  environment hands the value to anyone who can read its logs. Builds
  (spec 022) and notification channels (spec 024) already make naming a
  Secret need the value-read permission; Components must too, or a
  Contributor could exfiltrate any Secret in scope.
- **Where the value rests in the cluster is unstated.** To start a
  container, the value has to exist in the cluster in some form. Whether
  the cluster's own store encrypts it at rest is not known or reported
  today.

## Scope

### In

1. **Secret references on a Component.** A Component can set an
   environment variable from a Secret: the variable's name, the Secret,
   and which key of the Secret's value. The reference holds no value;
   the value never enters Serendip's store, audit log, events, API
   responses (other than the existing value endpoint) or the platform's
   own objects (releases, bindings, ConfigMaps).
2. **Per-environment secret references on a Deployment.** A Deployment's
   overrides can set or replace a secret variable for that Environment
   only, the same way `overrides.env` works for plain variables.
   Promoting a release never carries them over (spec 020's rule).
3. **Delivery into the running workload,** for every Environment a
   Component is deployed in, through the Application Platform provider,
   reconciled like everything else (constitution 3). No new in-cluster
   component is added to do it; whether to adopt one is decided in the
   plan against `docs/build-vs-adopt.md` (ADR 0020 rejected the External
   Secrets Operator for builds).
4. **Rotation reaches the pods.** Writing a new value to a Secret that a
   Deployment uses makes the workload pick it up (a rolling restart)
   without anyone editing the Component or the Deployment, and the
   Deployment's status says which value version it runs with (a time or
   version, not the value).
5. **Honest status.** A Deployment reports, as a condition, whether its
   secrets are delivered, and when not, why: the Secret has no value
   yet, the key isn't in the value, the Secrets capability isn't ready,
   or delivery failed. A release that can't get its secrets isn't
   reported as Ready.
6. **Authorization.** Naming a Secret (newly, or changing which Secret a
   variable names) on a Component or a Deployment needs
   `serendip/secrets/value/read` on that Secret, as `credentialSecret`
   does (403, audited). The Azure analogue is an App Service Key Vault
   reference, which only works when the app's identity may get the
   secret. Removing a reference needs nothing beyond the usual write
   permission.
7. **The graph and deletes.** A Component or Deployment that names a
   Secret `depends-on` it. Deleting a Secret something still uses is
   refused (409, naming the users), as spec 024 does for channels.
   Deleting a Component, Deployment, Application or Project removes the
   secret copies it caused in the cluster.
8. **Encryption at rest in the cluster.** New single-node installs by the
   agent turn on the cluster store's secret encryption. The Secrets
   capability reports whether in-cluster encryption at rest is on, off
   or unknown (an `external` cluster Serendip didn't install), and the
   docs say plainly what is and isn't protected.
9. **Interface parity** (constitution 7): API, CLI, MCP and the console
   (Component page and the Deployment overrides form) can add, change
   and remove secret references and show delivery status. The console
   shows the Secret's name and key, never a value.
10. **Docs:** an ADR for how secrets reach workloads,
    `application-platform.md`, `secrets.md`, `authorization.md`,
    `operations.md`, `cli-and-mcp.md`, `docs/build-vs-adopt.md` and
    `docs/risk-register.md`.
11. **Secrets keep working after OpenBao restarts** (added 2026-11-24,
    from T009's RP-094). When `serendipd` finds OpenBao sealed, it
    unseals it again with the key it already keeps. Before this, OpenBao
    stayed sealed until `serendipd` itself restarted, so secret variables
    couldn't change and values couldn't be written.

### Out

- **Secrets as files** (mounted volumes, TLS certificates as files).
  Environment variables only in this spec; files are a follow-up.
- **Whole-Secret import** (every key of a Secret as variables, like
  `envFrom`). One variable names one key; a follow-up can add it.
- **Workload identity / pods reading the Secrets provider directly**
  (OpenBao's Kubernetes auth, an agent sidecar, a CSI driver). The value
  is delivered by Serendip; pods never talk to the provider.
- **Secret versions pinned per release** (running an older value on
  purpose). A Deployment always gets the latest value.
- **Image pull credentials** for private registries.
- **Encrypting existing clusters' stores** or re-keying. Reported, not
  changed.
- **Auditing value reads**, including the reads delivery makes (still
  deferred, `secrets.md`).
- **Builds' own credential handling** (spec 022) stays as it is.

## Affected resources and capabilities

- **Resources:** `Component` (spec gains secret references), `Deployment`
  (overrides gain secret references; status gains a secrets condition
  and the delivered value version), `Secret` (unchanged shape; gains
  `depends-on` edges from Components and Deployments, and a delete
  guard).
- **Capabilities:** Application Platform (delivers secret variables into
  workloads), Secrets (values are read for delivery; reports in-cluster
  encryption at rest), Authorization (new check where a Secret is
  named).
- **Bootstrap agent:** turns on the cluster store's secret encryption
  for new installs.
- **Interfaces:** `/v1/components`, `/v1/deployments`, `/v1/secrets`
  (delete guard), readiness or capability status, CLI, MCP tools,
  console.

## Acceptance criteria

Run on a real cluster (kind, and the agent's K3s install for 8) unless a
unit test is named.

1. **A Component gets a secret.** A Component with a secret reference
   to an existing Secret's key, deployed to an Environment, runs with
   the variable set to the Secret's value (checked from inside the pod).
2. **No value outside the provider and the cluster's secret object.**
   After 1 and 4, the value appears in none of: Serendip's database
   (including `audit_log` and events), any API response other than
   `GET /v1/secrets/{id}/value`, `serendipd`'s log, or the platform's
   own objects (Component, Workload, releases, bindings, rendered
   release, ConfigMaps). Checked by searching for the value.
3. **Per-environment values.** The same release runs in two Environments
   with the variable from two different Secrets (via Deployment
   overrides); each pod sees its own. Promoting from one to the other
   keeps the target's reference.
4. **Rotation.** Writing a new value to the Secret rolls the pods within
   one reconcile interval plus the rollout, with no edit to the
   Component or Deployment; the new pods see the new value and the
   Deployment's status shows the new value version.
5. **Honest status.** A reference to a Secret with no value, or to a
   missing key, leaves the Deployment not Ready with a condition naming
   the Secret and the key (not a value); fixing the value clears it
   without other edits. With the Secrets capability not ready, the
   condition says so and running pods are left as they are.
6. **Authorization.** A Contributor without a Secrets role gets 403
   (audited) naming a Secret on a Component and on a Deployment; with
   Secrets User on that Secret it succeeds. Removing a reference, or
   editing other fields of a Component that already names a Secret,
   doesn't need the role. Unit-tested and checked live.
7. **Delete guard and clean-up.** Deleting a Secret a Component or
   Deployment uses is 409 naming them; after the reference is removed,
   the delete works. Undeploying, deleting the Component, and deleting
   the Application each leave no secret copy of theirs in the cluster.
8. **Encryption at rest.** On a fresh agent K3s install, the cluster
   store's secret encryption is on, and the Secrets capability reports
   it on; on a kind dev cluster it reports unknown or off, honestly.
9. **Parity.** Each of add, change, remove and view status works through
   the API, CLI, MCP and console; the console never shows a value.
10. **Validation.** Bad variable names, a variable set both plainly and
    from a Secret, duplicates, a Secret ID that doesn't exist or isn't
    reachable from the Component, and more than the limit of references
    are refused with 400 or 422 naming the field. Unit-tested.
11. **Idempotent.** With nothing changed, a reconcile pass makes no write
    to the cluster's secret objects or the platform's objects (checked by
    resource versions across several passes).
12. **Lightweight** (constitution 4): `serendipd` idle RSS stays within a
    few MiB of spec 025's (33.2 MiB on the simulated T470), and no new
    pods run in the cluster for this feature.
13. **Re-unseal.** After the OpenBao pod restarts and comes back sealed,
    the Secrets capability is ready again within one resync (30 s) with
    no `serendipd` restart. A value written then is read back, and a
    secret variable changed then reaches its pods. An uninitialized
    OpenBao is never initialized again, because that would lose every
    value. Unit-tested and checked live.

## Open questions (for the plan)

1. **How the value reaches the pod.** A Serendip-written cluster secret
   per (Component, Environment) referenced by the component type, versus
   upstream's External Secrets path, versus something else. ADR 0020's
   reasoning for builds is the starting point; the plan confirms with
   OpenChoreo 1.3.1's Workload and component type how a template refers
   to a secret and whether a rendered release would ever hold a value.
2. **How rotation is noticed.** Polling value versions on each
   reconcile versus an explicit trigger from the value write.
3. **What "reachable from the Component" means** for a Secret: same
   Project only, or anywhere the caller can read it.
4. **How encryption at rest is detected** on a cluster Serendip
   installed versus one it didn't.
5. **Existing Components:** the platform type changes, so running
   releases pick it up only on their next change (as in spec 012).
   Whether that needs a migration.
