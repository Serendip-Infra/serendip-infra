# T001: OpenChoreo 1.3.1 checks (2026-11-24, `serendip-t018`, kind v1.34.0)

These were run by hand against a probe. That meant a copy of the live
`serendip-service` type named `t026-service`, plus a hand-made Component,
Workload, ComponentRelease and ReleaseBinding (`t026-probe`) in the
existing project `app-5a9fba377b` and Environment `env-b7166194a9`. All
of it was deleted afterwards.

## Template changes

- **The CEL needs `dyn()`.** `configurations.toContainerEnvFrom() + [...]`
  is refused at admission:
  `found no matching overload for '_+_' applied to '(list(EnvFromEntry),
  list(map(string, map(string, string))))'`.
  This form is accepted and works:
  `${dyn(configurations.toContainerEnvFrom()) + (environmentConfigs.serendipSecretEnv.name != "" ? [{"secretRef": {"name": environmentConfigs.serendipSecretEnv.name}}] : [])}`.
- **The schema property** `serendipSecretEnv {name, version}` (strings,
  default `""`, object default `{}`) is accepted in `environmentConfigs`.
- **Rendered Deployment** with `name` set: the pod template has
  `annotations: {"serendip.dev/secret-env-version": "v1"}`, and
  `envFrom: [{configMapRef: <env-configs>}, {secretRef: {name:
  t026-probe-secret-env}}]`.
- **In the pod:** `printenv DB_PASSWORD PLAIN` gave the Secret's value
  and the plain variable.
- **With `name` and `version` set to `""`:** the `envFrom` is back to
  the ConfigMap only, and the annotation is `""`.

## No value in the platform's objects

The value `probe-value-xyz123` was searched for with `grep -c` over the
JSON of every RenderedRelease (all namespaces), plus the ReleaseBindings,
ComponentReleases, Workloads and Components in `openchoreo-control-plane`
and the ConfigMaps in the data-plane namespace. **0 matches** for each.
Only the Secret's name and the version are in the ReleaseBinding.

## Rotation

- I wrote a new value to the cluster Secret and patched the binding's
  `version` from `v1` to `v2`. That created a new ReplicaSet (a rollout).
- The new pod sat `Pending` because the dev node's CPU requests were at
  99%. That's this cluster, not the feature. With the old pod deleted,
  the new pod's `DB_PASSWORD` was the new value.

## Where the data-plane namespace is first readable

`ProjectReleaseBinding.status.namespace` carries it, with condition
`NamespaceReady`. For example, `app-5a9fba377b-env-b7166194a9` gives
`dp-openchoreo-co-app-5a9fba377-env-b7166194-ae9648e7`. So the
provider can read it right after `Deploy` applies the project binding,
and doesn't need the RenderedRelease. `SecretEnvPending` covers the
moment before `NamespaceReady`.

## A pre-026 release (decision 12)

- I made a ComponentRelease whose snapshot is the current (pre-026)
  `serendip-service` type, and bound it with
  `componentTypeEnvironmentConfigs.serendipSecretEnv` set.
- The binding was **accepted and synced** (`ReleaseSynced=True`). The
  unknown property is **silently ignored**, so the pods get no `envFrom`
  secret and no annotation.
- A release pins its type (`ComponentRelease.spec.componentType.spec` is
  a snapshot, hashed into the release name). So the provider has to
  detect a release without the property in its schema and report
  `NeedsNewRelease`. Upstream gives no error.
- A server-side dry-run patch adding the property to an existing binding
  of the old type was also accepted.
