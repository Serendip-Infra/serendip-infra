# Spec 026 verification (T009, real cluster)

Run 2026-11-24 on `serendip-t018`: kind, `kindest/node:v1.34.0`, with
the spec 017–025 stack (OpenBao, kgateway, OpenChoreo 1.3.1, ThunderID,
the builds and observability packages). Criterion 8 also ran on a fresh
`rancher/k3s:v1.36.4-k3s1` container started with the agent's flags.
Times are local (UTC+05:30) unless marked UTC.

Values are random markers made for the run (`T026dev…`, `T026prod…`,
`T026rot…`) and kept in `ids.txt`. They were written through
`POST /v1/secrets/{id}/value` and never passed on a command line.

## Setup

- **`serendipd`:** built from `spec-026` after T008 (`a71fa92`), with the
  console embedded (`corepack pnpm build`, 127.6 KiB gzipped JS), on
  `127.0.0.1:8088`. It was rebuilt as fixes landed (F1–F4 below). The
  user's dev `serendipd` (`dev-up.ps1`, `t023.db`) was stopped for the
  run, because console sign-in is registered for `localhost:8088` only.
- **Store:** a copy of the dev store (`t023.db` → `t026/t026.db`).
- **Principals:**
  - `verifier` is Owner at `/`. For the run it also got **Secrets
    Officer at Project `t014-builds`**, because Owner has no data
    actions, and keeps it.
  - `t014-contrib` is Contributor at `t014-builds`. It got Secrets User
    on Secret `t026-db` for criterion 6; that Secret is now deleted.
  - `admin` (the person) signed the CLI in for `serendip mcp` and the
    console.
- **Fixtures:**
  - In Project `t014-builds` (pipeline `t014-dev` → `t014-prod`):
    Application `t026-app` and Component `t026-sec` (`nginx:1.27-alpine`,
    plain `PLAIN`, secret `DB_PASSWORD` from `t026-db`'s `password`), with
    Deployments in both Environments.
  - Secrets `t026-db`, `t026-db-prod` and `t026-empty`, each with its own
    value.
- **Freed for the run:** the node's CPU requests were at 96%. Five
  Components from older test stores weren't in this store (`t020-web`,
  `t020-api`, `rp018-check`, `t019-stale-web`, `t019-web-reject`, 11
  pods). Their ReleaseBindings were set to `state: Undeploy`, which can
  be undone with `Active` (`evidence/notes.txt`).
- **Tooling** in `~/.serendip-t018/t026/`: `lib.py`, `api.py`, `mcp.py`,
  one script per criterion (`c1`–`c11`), `console/c9.mjs`,
  `cli-login.mjs`, `start.sh`, and the evidence under `evidence/`.

## The upgrade (decision 12)

On the first start of the new build, the `serendip-service` type took
`serendipSecretEnv`, so every Component got a new release, and the three
Deployments that follow the latest release rolled once. The fourth,
`t014-pub` in `t014-prod`, didn't move. It names its release
(`spec.release`), so that's expected. If it ever gets secret variables,
it reports `NeedsNewRelease` (T004).

## Criteria

1. **A Component gets a secret. PASS.** `t026-sec` deployed to
   `t014-dev`: `printenv` in the pod gave the Secret's value for
   `DB_PASSWORD` and `plain-ok` for `PLAIN`. The pod's `envFrom` is the
   env ConfigMap plus `secretRef cmp-8ce3afbb81-secret-env`, and its
   annotation `serendip.dev/secret-env-version` matches the Deployment's
   `status.secretEnvVersion`. `SecretEnvDelivered=True`. (`c1.txt`)
2. **No value outside the provider and the cluster's secret object.
   PASS.** Run after 1, 3, 4, 5 and 9. Every value, and the base64 form
   of each, was searched for in:
   - all three store files (`t026.db`, `-wal`, `-shm`), as raw bytes;
   - 44 API answers: every list and item of Secrets, Components,
     Deployments, Applications, Environments, Projects and role
     assignments, plus `/v1/secrets/status`;
   - `serendipd`'s log;
   - the platform's Components, Workloads, ComponentReleases,
     ReleaseBindings, ProjectReleaseBindings, RenderedReleases (all
     namespaces), ConfigMaps, Deployments, ReplicaSets and Events.

   **No match** in any of them. The one hit is the delivered Secret
   `cmp-8ce3afbb81-secret-env` itself, holding the current value, which
   also shows the search works. The console's rendered pages (criterion
   9) had no match either. (`c2.txt`)
3. **Per-environment values. PASS.**
   - The prod Deployment named dev's release, with an override of
     `DB_PASSWORD` to `t026-db-prod`.
   - A change to `PLAIN` made a new release on dev, which was promoted
     (`POST …/promote {"to": prod}`).
   - Prod then ran the new release and kept its override, and its
     `secretEnvVersion` didn't change.
   - From inside each pod: dev saw dev's value, and prod saw prod's,
     both with `plain-v2`. (`c3.txt`)
4. **Rotation. PASS.**
   - A new value written to `t026-db` changed dev's
     `status.secretEnvVersion` **1.0 s** later (the value-written
     wake-up, decision 4).
   - A new pod saw the new value **3.8 s** after the write.
   - The Component's and the Deployment's specs and the Component's
     `resourceVersion` were unchanged.
   - Prod, on another Secret, kept its version. (`c4.txt`)
5. **Honest status. PASS after F1–F3.**
   - **A Secret with no value:** `Ready=False` and
     `SecretEnvDelivered=False`, reason `SecretHasNoValue`, and the pod
     was untouched. Writing the value cleared it with no other edit.
   - **A missing key:** `SecretKeyNotFound`, naming the Secret, key and
     variable. Adding the key to the value cleared it.
   - **The Secrets capability down** (OpenBao scaled to 0):
     - a variable change gave `Ready=False`, with `SecretEnvDelivered`
       Unknown and reason `ProviderNotReady`: "the secrets capability
       isn't ready: still being set up, or unavailable";
     - the pod and `secretEnvVersion` were unchanged;
     - a value write answered 503 "capability not ready";
     - `/v1/secrets/status` said `ready: false`.

   (`c5.txt`, `c5c.txt`)
6. **Authorization. PASS.** As `t014-contrib`, without a Secrets role:
   - Naming `t026-db` on a new Component, on an existing Component, and
     in a Deployment override each got 403 "missing
     serendip/secrets/value/read at Secret/…". Each is in `audit_log` as
     `deny`.
   - Changing another field of `t026-sec`, which already names a Secret,
     and removing a Deployment's reference were both 200.
   - With Secrets User on `t026-db` only, naming `t026-db` worked on both
     kinds, and naming `t026-db-prod` was still 403. The allow rows name
     the Secret data action they used.

   (`c6.txt`)
7. **Delete guard and clean-up. PASS after F4.**
   - Deleting each Secret in use answered 409 naming the user: "Secret …
     is used by Component t026-sec" for `t026-db`, and "… by Deployment
     t026-sec" for `t026-db-prod`. Once the reference was removed, the
     delete was 204.
   - Deleting the prod Deployment removed its namespace's
     `-secret-env`.
   - Deleting a Component left its copy behind: **F4**, fixed. After the
     fix, deleting Component `t026-del` while its Deployment was
     delivering removed `cmp-8a6dbbbbdc-secret-env`.
   - Deleting `t026-app` with `cascade=true` removed the last copy and
     the namespace.

   (`c7.txt`)
8. **Encryption at rest. PASS.**
   - `serendip-agent bootstrap --dry-run --cni default` gives `server
     --secrets-encryption`. A `rancher/k3s:v1.36.4-k3s1` container
     started with exactly that showed:
     - `k3s secrets-encrypt status`: Enabled, AES-CBC, all hashes match;
     - the node: `k3s.io/encryption-config-hash`.
   - A scratch `serendipd` on that cluster (its own store, `:8090`)
     answered `/v1/secrets/status` with `encryptionAtRest: on`, reason
     `EncryptionConfigured`.
   - On kind it answers `unknown`, reason `NotReported` ("the cluster
     doesn't say whether it encrypts Secrets at rest").
   - The `--cni cilium` flags are `server --secrets-encryption
     --flannel-backend=none --disable-network-policy`, unit-tested in
     `agent/bootstrap`. T001 checked them live in a container.

   The container and the scratch store were removed afterwards.
   (`c8-k3s.txt`)
9. **Parity. PASS.**
   - **API:** criteria 1–7.
   - **CLI:**
     - `components update --secret-env NAME=ID:KEY` added and changed a
       variable, and `--remove-secret-env` removed one. The same flags
       work on `deployments update`.
     - A remove of an unset name, and a malformed `--secret-env`, each
       exit 2 with the reason.
     - `secrets status` and `deployments get` show the status.
   - **MCP** (`serendip mcp` as `admin`):
     - `components_update` and `deployments_update` with the list in the
       document added and removed variables;
     - `secrets_status` and `deployments_get` show the status;
     - the tools describe `secretEnv`.
   - **Console** (headless Chrome, as `admin`):
     - The Component overview table lists the variables (variable, Secret
       name, key).
     - The edit form added, changed and removed a variable, and kept
       `spec.dependencies` through each save. That was T008's fix,
       checked live with a dependency on `t026-dep`.
     - The Deployment's Configure form ("Secret variables for t014-dev")
       added and removed an override.
     - The Deploy tab showed "Overrides: 1 secret variable" and "2 secret
       variables, delivered".
     - The Secrets page showed the "isn't known whether the cluster
       encrypts" banner.
     - No value was found in any rendered page's HTML.

   (`c9-cli-mcp.txt`, `c9-console.txt`, `c9-console-pages.html`,
   `console-*.png`)
10. **Validation. PASS.** Each case below was refused on the Component,
    and the ones marked "both" on a Deployment override as well.
    - **400**, naming the field:
      - a bad variable name, `"1BAD-NAME"` (both);
      - a variable set plainly and from a Secret: `PLAIN is also set in
        env`;
      - a duplicate: `A is set twice` (both);
      - an empty key;
      - 65 references: `at most 64 variables`;
      - an override's plain `env` shadowing a Component secret variable.
    - **422**, for a caller who may read values:
      - a Secret that doesn't exist: `secretEnv[0].secret: Secret … not
        found` (both);
      - a Secret in another Project: `the Secret must be in this Project
        or above it` (both).

    A caller without value read on the named Secret gets 403 first,
    before its existence or scope is looked at. That's by design (plan
    decision 7), so a 403 doesn't tell anyone whether the Secret exists.
    The 422s were checked with a temporary Secrets User grant at `/`,
    deleted afterwards. Unit tests cover the rest. (`c10.txt`)
11. **Idempotent. PASS.** With nothing changed, over about four 30 s
    passes (130 s), these resource versions didn't change:
    - the cluster Secret, ReleaseBinding, Component and
      ProjectReleaseBinding;
    - the Kubernetes Deployment;
    - the Workload and the three ComponentReleases (checked over another
      65 s).

    Serendip's store versions didn't change, and no audit rows were
    added for the Component or Deployment. (`c11.txt`)

13. **Re-unseal (T012). PASS.**
    - The Component `t026-reseal` had `PW` delivered from `t026-reseal`'s
      value.
    - OpenBao was scaled to 0: `/v1/secrets/status` said `ready: false`.
    - It was scaled back to 1. By the time its pod was Ready and checked,
      it was **already unsealed**. That was `serendipd`'s own resync:
      the log says "openbao: was sealed (its pod restarted?); unsealed it
      with the stored key", and no request was sent meanwhile.
    - Status went back to `ready: true` with no `serendipd` restart.
    - A value write was 200 and read back the same, and the new value was
      in the pod 5.1 s later.

    The unit tests cover the other cases: unreachable, uninitialized
    (never `Init`), the 10 s limit, an unreadable credentials file, and
    no unseal configured. (`c13.txt`)

12. **Lightweight (T010). PASS.** A same-host A/B on `serendip-t018`,
    2026-11-24 15:28–15:51. `serendipd` from `main` (spec 025, A) and
    from `spec-026` (B), run as A, B, A, B, each on a fresh copy of this
    store, with `t026-reseal`'s Deployment using a secret variable, and
    read 4.5 min after start:
    - pair 1: A 83.2 MiB, B 83.1 MiB (−0.1);
    - pair 2: A 35.1 MiB, B 36.1 MiB (+1.0).

    Both pairs are within 2 MiB. The 83/35 split hit both binaries
    alike: it's the Windows working-set effect from specs 024 and 025.
    The same 39 pods ran in every listing, apart from the 4 workload pods
    that rolled. Switching to `main` rolls the Deployments following the
    latest release and drops their secret variables until `spec-026`
    comes back (decision 12, both ways). The Secret itself was never
    rewritten. (`profiles/edge-1/bench/results/2026-11-24-spec026-same-host/`)

## Findings

- **F1, fixed.** `SecretHasNoValue` and `SecretNotFound` named the Secret
  but not what uses it, while criterion 5 asks for the key too. They now
  add `(for DB_PASSWORD, key "password")`, through `secretEnvUses` in
  `core/reconcile/secretenv.go`. A unit test checks the wording.
- **F2, fixed.** An unreachable or sealed OpenBao was a plain error.
  Delivery said `SecretReadFailed` ("can't read …; retrying") instead of
  the capability being down, and a value write answered 500.
  `openbao.Client` now marks transport errors and 503 answers as
  `ErrUnavailable`, and the provider maps that to `ErrProviderNotReady`.
  Delivery now says `ProviderNotReady`, and the API answers 503. The
  first version of the fix put OpenBao's raw answer in the 503's detail,
  so the error now carries a fixed text instead ("the secrets store isn't
  answering, or is sealed"). Unit tests cover both.
- **F3, fixed.** `GET /v1/secrets/status` said `ready: true` with OpenBao
  gone, because readiness only reflected setup at start. The provider's
  `EncryptionAtRest` now asks OpenBao's health first: sealed,
  uninitialized or unreachable is not ready. Checked live: down, and back
  but sealed, both give `ready: false`. The shared "not ready" message
  said "still being set up", which is wrong for an outage. It now reads
  "isn't ready: still being set up, or unavailable".
- **F4, fixed.** Deleting a Component left `<cmp>-secret-env` in every
  Environment it ran in. Plan decision 8 covered undeploy and namespace
  deletion, not this. `DeleteComponent` now deletes those Secrets first,
  while the bindings still name the namespaces, and stops before the
  Component on a failure. Unit-tested and checked live.
- **Older than spec 026, recorded:**
  - **RP-094:** OpenBao came back sealed after its pod restarts and
    stayed sealed until `serendipd` restarted. Fixed by T012 (criterion
    13), the user's choice.
  - **RP-095:** deleting a Component leaves its Deployments behind,
    waiting for it.
- **Seen, not a defect:** a write sent right after a read can meet a
  409 when a reconcile has written status in between, because they
  share `resourceVersion`. Two MCP updates hit this, and a fresh read
  fixed it. That's the usual optimistic-concurrency check.

## State left

- `serendipd` (the T009 build, on `t026/t026.db`) runs on `:8088`. The
  user's dev one is stopped. Restart it with `.\scripts\dev-up.ps1
  -Restart` (a fresh build, so it has the fixes).
- The fixtures are deleted: Application, Components, Deployments and
  the four Secrets.
- `verifier` keeps Secrets Officer at `t014-builds`.
- The five older test Components stay at `state: Undeploy`.
- The CLI is signed in as `admin`.
