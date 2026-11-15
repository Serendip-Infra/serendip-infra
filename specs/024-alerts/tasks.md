# Tasks: Spec 024 — Alert rules and notification channels

Ordered. `[P]` = parallel-safe within its block, `[A]` = subagent-eligible
(tests or docs). Run `contract-guardian` before committing anything under
`core/` or `providers/`, and `go-reviewer` after each non-trivial Go task.

Dependency sketch:
- T001 → T002 → (T003 ∥ T004) → T005 → T006 → T007 → (T008 ∥ T009) →
  T010 → T011 → T012 → T013.
- T002's core types and T004's sender don't need the cluster, so they can
  start while T001's spike runs.

**Memory:** spike and benchmark one thing at a time on `serendip-t018` and
`bench-t470`, and run the keep-awake beside long runs.

## T001 — Spike: the condition queries on serendip-t018 ✅ 2026-11-15

> **Done as:** plan.md "Spike findings" 1–6, with recorded answers in
> `providers/observability/openchoreo/testdata/condition-*.json` and
> `core/notify/testdata/` (Telegram bodies and answers, the probe reports;
> token, chat ID, names and addresses replaced). Amended: decision 3
> (restart counter added to the allowlist and read as max − min; logs read
> `total`; `for` reads N + 1 points) and decision 6 (no button for a
> `localhost` console URL; Gmail app password as the free default, a relay
> only with an owned, authenticated domain; "accepted by the relay", never
> "delivered"). No 429 recorded at 70 messages; T004 uses the documented
> shape. Scratch namespace deleted; bot token revoked (`getMe` 401), SMTP
> key and app password deleted.

- **Do,** with the observability package installed:
  1. **Metric condition:** the metrics adapter's points over a `for`
     window, for one Deployment, with all three UIDs.
  2. **Log condition:** a `logMatch` search with `limit = above + 1`.
  3. **Restarts:** kube-state-metrics' restart counter for one
     Deployment's pods, through the Service proxy to Prometheus. Find
     out:
     - whether the pods can be matched by label, or only by name from
       the Deployment's ReplicaSets;
     - whether a crash-looping pod's count rises within a minute.
  4. **Each query's latency** at the current store size.
  5. **Telegram, with a scratch bot:**
     - a card with a URL button, accepted;
     - whether Telegram refuses a `localhost` or plain-`http` console
       URL in the button;
     - a reply threaded to the firing (`reply_parameters`);
     - the answer to a malformed body and to an over-rate burst
       (`retry_after`);
     - reading a `chatId` with one `getUpdates` call.
  6. **Email:** one send through a Gmail app password and one through
     Brevo, recording their TLS mode and any sending limits shown.

  Revoke the scratch bot's token and delete the test senders afterwards.
- **Files:** `plan.md` ("Spike findings", amending decisions where the
  facts differ), and recorded answers in
  `providers/observability/openchoreo/testdata/` and
  `core/notify/testdata/`.
- **Verify:**
  - every item has a finding in `plan.md`, with the command or output it
    came from;
  - the recorded answers parse in a scratch test;
  - no scratch pods are left, and the scratch bot token is revoked.

## T002 — Core types: rules, Alerts, channels, SMTP servers, heartbeat [P] ✅ 2026-11-15

> **Done as:** `core/resource/alerting.go` (the five kinds, `Validate*`,
> caps, `*Relationships` depends-on edges, `AlertStatus.AddDelivery`
> keeping pending deliveries), `ops.go`'s placeholder gone. The 409 is in
> the store: `store.ErrInUse` from `Delete` when a kind in
> `resource.RefusesDeleteWhileUsed` (Secret, channel, SMTP server) is
> still named from outside the delete, cascade included
> (`inuse_test.go`); the approval reconciler fails such a delete as
> `DeleteFailed`. `resourcePaths` and the catalogue carry `alertrules`,
> `notificationchannels` and `smtpservers` (decision 12's paths, so the
> action namespaces are `serendip/alertrules/*` and so on: **T003 uses
> these, not decision 9's camelCase**); `alerts` is gone. Until T007
> they're served by the generic handler with `WithPrepare` (validate,
> derive edges); T003 adds the authorization rules beyond the generic
> ones. Heartbeat has no route until T007.


- **Do:**
  - the five kinds in `core/resource/alerting.go` (plan decisions 1, 3
    and 6): `AlertRule`, `Alert`, `NotificationChannel` (with
    `minSeverity`), `SMTPServer` and the `Heartbeat` singleton;
  - their validation, the caps (200 rules, 1–60 minutes, `above` ≤ 999,
    20 recipients, a heartbeat interval of 1–10 minutes) and graph
    edges;
  - remove `ops.go`'s placeholder `AlertSpec`;
  - store wiring: the kinds and `resourcePaths` (not `Alert`), and deletes
    refused with 409 while something uses them.
- **Files:** `core/resource/alerting.go`, `alerting_test.go`, `ops.go`,
  and the store and kind registration.
- **Verify:**
  - `go test ./core/...` passes, with table tests for each condition's
    and channel type's validation, the caps, the edges and the 409;
  - `bash .claude/scripts/check-imports.sh` is clean.

## T003 — Authorization: actions, roles and routes [P] ✅ 2026-11-15

> **Done as:** actions in `core/policy/builtins.go` (path-derived:
> `serendip/alertrules/silence/action`, `serendip/alerts/acknowledge/action`,
> `serendip/notificationchannels/{test,allowPrivateDestination}/action`;
> the last in Contributor's `NotActions`). `api/authz.go`'s
> `alertingRoutes()` has a rule for every route of decisions 12 and 15;
> the ones T007 registers are listed in `authz_routes_test.go`'s
> `pendingRoutes`, which the route and catalogue tests skip until then
> (**T007 empties it**). `TestAlertingRouteRules` has a row per route.
> Alerts and the heartbeat are in `nestedPaths` (actions, no generic
> CRUD). SMTP server writes are decided at `/`, like a ProviderInstance's.
> **Amended:** unsilence is `POST /v1/alertrules/{id}/unsilence`, not
> `DELETE …/silence`: every DELETE route is a delete with an Approval
> (and an agent's needs one), and unsilencing deletes nothing.
> The heartbeat's `DELETE /v1/heartbeat` uses a new `targetSingleton`
> target, so its Approval names the singleton's ID, and an approval
> request may name the Heartbeat. The body-dependent checks are a
> `WithAuthorize` hook on the generic handler (`api/alerting_authz.go`):
> a rule's newly named channels need `notificationchannels/read` (a
> platform default channel excepted; decided before existence, so an
> unreadable missing channel is 403, not 400), and setting or changing an
> allowance needs `allowPrivateDestination/action` at the channel and its
> owners (`allowancesAt`, which T007 uses for the heartbeat's allowance at
> `/`). Deletes of rules, channels and SMTP servers already went through
> Approvals (the generic DELETE routes; `ErrInUse` fails an approval in
> T002), now tested; `core/reconcile/approval.go` needed no change.
> **Review fixes** (go-reviewer):
> - a Contributor can't change where a channel with an allowance sends
>   (the Owner allowed one destination);
> - a channel's Secrets (bot token, URL, signing key) need
>   `secrets/value/read` where they're newly named, as a build's
>   credential does; the SMTP server an email channel names needs
>   nothing, since SMTP servers are platform relays (decision 6);
> - a rule moved to another owner names its channels anew;
> - owners are compared as sets.
>
> **Accepted** (RP-075): the check-before-write window, telling a
> platform default channel's ID apart, and custom wildcard roles covering
> the allowance action. **By design:** a refused create's audit row has
> no resource ID (none exists yet), as for other kinds. contract-guardian:
> pass.


- **Do:**
  - the actions of plan decision 9, with `allowPrivateDestination` in
    Contributor's `NotActions`;
  - the rule that writing a rule needs read on each channel it names;
  - a route-table row for every route of decision 12;
  - deletes of the four kinds (rules, channels, SMTP servers, the
    heartbeat) through Approvals (decision 10).
- **Files:** `core/policy/builtins.go`, `api/authz*.go`, the route-table
  test, and `core/reconcile/approval.go` (targets).
- **Verify:**
  - `go test ./core/policy/... ./api/...` passes;
  - the route-table test has a row for each new route;
  - table tests show a Contributor denied `allowPrivateDestination`, a
    Contributor in Project A denied a rule naming Project B's channel,
    and a Reader denied writing a rule.

## T004 — The sender: address check, channel types, heartbeat [P] ✅ 2026-11-15

> **Done as:** `core/notify` (`dialer.go`, `client.go`, `body.go`,
> `telegram.go`, `smtp.go`, `sender.go`, `heartbeat.go`), stdlib only.
> Every item of the Verify list has a test (`notify_test.go`, with a
> stdlib fake SMTP server in `fake_test.go`). The card matches T001's
> recording, so it reads "Summary: …" on its own line, not the summary
> in bold. The caller composes the condition line (`Message.Detail`), so
> `core/notify` knows nothing about conditions. go-reviewer's findings
> fixed: **keep-alives off** (a pooled connection from a channel with
> the private allowance could have carried another channel's send past
> the check), DNS timeouts retryable, a cancel not counted as a failure,
> Teredo, IPv4-compatible and 6to4-relay ranges refused, NAT64 and 6to4
> addresses checked by the IPv4 address they carry, From and recipients
> re-checked at send, HELO with the sender's domain. By design: an SMTP
> server has no private allowance (the error says it must be public).
> contract-guardian: pass; "Telegram" in `core/` stands under decision
> 5's reading while no Notification capability exists (ADR 0022, T013).


- **Do:** `core/notify` (plan decisions 5 and 6), standard library only:
  - the checked dialer, with every range in decision 5, IPv6 and
    IPv4-mapped;
  - no redirects, no proxy, TLS 1.2 or later;
  - Telegram (the card, the URL button, threading, HTML escaping), email
    (text and HTML parts, threading headers) and the signed
    `serendip.alert.v1` webhook;
  - severity routing (`minSeverity`);
  - the heartbeat sender (plan decision 15), gated on the evaluator,
    the delivery loop and the store;
  - `Retry-After`;
  - errors cleaned down to the host.
- **Files:** `core/notify/*.go` and tests, and `testdata/` from T001.
- **Verify:** `go test ./core/notify/...` passes, against `httptest` and a
  stdlib fake SMTP server. The tests cover:
  - each refused range, and a name that resolves to one;
  - a DNS answer that changes between the check and the dial (the
    `Control` hook refuses it);
  - a 302;
  - `HTTPS_PROXY` set;
  - the allowance skipping private ranges only;
  - the signature, checked by a reference implementation in the test;
  - the Telegram body matching T001's recording, with a summary holding
    `<b>`, `&` and `</a>` escaped;
  - `retry_after` honoured;
  - an email resolution carrying `In-Reply-To` for its firing;
  - an alert below a channel's `minSeverity` recorded as
    `belowMinSeverity`, not sent;
  - the heartbeat sending while healthy, and sending nothing once the
    evaluator stalls or the store fails;
  - SMTP refusing plain auth;
  - no token or URL path in any returned error.

## T005 — The evaluator and delivery loop ✅ 2026-11-15

> **Done as:** `core/reconcile/alerts.go` (the evaluator) and `delivery.go`
> (the delivery loop), sharing an `alertWriter` that serialises Alert
> read-modify-writes; wired in `cmd/serendipd/alerts.go` when a cluster is
> configured, and shutdown waits for both loops before the store closes.
> `ObservabilityProvider.EvaluateCondition` returns a `ConditionValue`
> (`Value` compared, `Current` shown) or `ErrNoData`; the provider answers
> "not evaluated yet" until T006, so rules read `QueryFailed` meanwhile.
> Retention is `sqlite.PurgeResolvedAlerts` (RP-071): **it records an
> Approval** (one per rule per pass, by policy, Executed, naming the
> removed Alerts; contract-guardian blocked the plan's "no Approval" under
> constitution 5, plan amended), and catches up in batches. Choices:
> - a rule naming no channels goes to its Project's default channels,
>   else the platform's (and a built-in rule to the platform's);
> - **disabling a rule resolves its firing Alerts** (`ruleDisabled`, a new
>   reason), and a rule with nothing deployed reads `NotDeployed`;
> - digests are for chat channels (Telegram, email); a webhook gets one
>   `serendip.alert.v1` body per Alert, one a second;
> - 5 attempts, with 10 s, 30 s, 1 min and 5 min between them;
> - a rule's value is flushed once a minute when it changed, and its
>   `lastEvaluated` alone every 10 minutes, so steady rules write nothing
>   (1,000 evaluations, 0 writes in the test);
> - a channel's Secret is read as its `value` key, or its only key.
>
> **Review fixes** (go-reviewer): a send is recorded even as serendipd
> stops (`WithoutCancel`), so it isn't re-sent; a resolution waits for its
> firing to the same channel; a rule that can't be read holds its
> deliveries rather than send past a silence; rates and backoff run from
> the end of the send; transitions are timed when the answers are in; a
> failed packageRemoved resolve is retried; resolved Alerts past the
> reopen window are forgotten; held values are flushed on a clean stop;
> every shutdown path waits (30 s at most) for the loops. By design: a
> digest doesn't thread (its resolution can't reply to it); duplicate
> channel IDs are refused by validation, so deliveries match by channel,
> event and time.
>
> **Azure analogues:** Azure Monitor's stateful alerts (one alert fires
> and resolves itself when the condition clears; Serendip's fingerprint
> and reopen window), action groups' rate limits (per channel here),
> alert processing rules that suppress notifications (silences), and
> fired alerts kept for 30 days (retention). Departs from Azure: digests
> (Azure sends each), and default channels (Azure has none).


- **Do:** `core/reconcile/alerts.go` and `delivery.go` (plan decisions 2,
  7 and 11):
  - due rules on each tick, 2 queries at a time, with timeouts;
  - folding, with the 5-minute reopen window;
  - `lastValue` held in memory and flushed once a minute;
  - errors setting `Evaluating=False` without changing Alerts;
  - resuming after a restart;
  - deliveries with backoff, the per-channel rate and digests, and
    silences;
  - retention: 30 days, 500 rows a pass, one audit row;
  - `ObservabilityProvider.EvaluateCondition` in the interface;
  - wiring into `cmd/serendipd`.
- **Files:** `core/reconcile/alerts.go`, `delivery.go`,
  `observability.go`, their tests, and `cmd/serendipd/*.go`.
- **Verify:** `go test ./core/reconcile/...` passes, with a fake provider
  and a fake clock. The tests cover:
  - fire, update and resolve, then a reopen inside the window and a new
    Alert after it;
  - an error leaving a firing Alert firing;
  - a restart resuming a pending delivery without re-sending a sent one;
  - retries stopping at 5, then `failed`;
  - a 400 not retried;
  - 10 queued for one channel becoming one digest;
  - a silence recording `silenced`;
  - 200 rules at 1 minute making at most 2 concurrent queries, with no
    store write per evaluation (counted).

## T006 — The provider: conditions and the built-in disk rule ✅ 2026-11-15

> **Done as:**
> - **Queries:** `conditions.go` and `prometheus.go` (a Prometheus HTTP
>   API v1 instant-query client through the Service proxy):
>   - metric: for + 1 points at a 1-minute step, the window's lowest
>     point as the value, each point paired with the request or limit
>     then in force;
>   - logMatch: `limit: 1`, reading `total`;
>   - restarts: the PromQL template, filled only from UIDs that match
>     `^[A-Za-z0-9-]{1,64}$`;
>   - packageDisk: through `WithDiskUsage`.
> - **The restart counter** is in the allowlist, and in the bench's copy
>   of the values, which a test keeps identical.
> - **VERSIONS.md** pins the query shapes.
> - **New in core:** `ConditionRefusedError` (reason `ConditionRefused`,
>   with a message a person can act on).
> - **The built-in rule:** declared in the catalogue as
>   `observability-disk` (warning, 85%). It's created on Ready by
>   `ProviderInstanceReconciler.WithAlertRules`, matched by condition
>   type. A delete aimed at it is refused with 409 (`resource.OwnerKept`
>   in the store), and the API refuses setting or clearing `builtIn`. It
>   goes by cascade, so the console's package removal now sends
>   `cascade=true` (plan decision 4 amended).
> - **The cluster's ranges** come from `kubernetes.Client.ClusterRanges`
>   (nodes' `podCIDRs` and addresses, and ServiceCIDRs). They're
>   refreshed every 5 minutes, or 30 s after a failure, and set on
>   `core/notify`'s Checker. The Checker refuses private destinations
>   until the first read (RP-076).
> - **Unit fix:** the metric `value` unit is millicores or MiB, as the
>   notification text has it (T002's message said cores or bytes).
> - **Review fixes:**
>   - go-reviewer: a request or limit on another grid still pairs; an
>     empty restarts answer is no data unless the pods' series exist;
>     the rule is matched by type, so a rename doesn't re-create it; a
>     partial ranges read adds to the last; the fail-closed Checker;
>     `builtIn` can't be cleared through the API; the fallback is capped
>     at the answer limit; node DNS-name addresses are skipped;
>     unparseable ones are reported.
>   - contract-guardian: pass. Its stale-disk-reading advisory is fixed
>     (over 5 minutes old is no data).
>   - Left as is: a UID that fails the check gives a logged
>     `QueryFailed`, not `ConditionRefused`, since a person can't act on
>     it.
> - **Risks:** RP-076 and RP-077. RP-068's "no alert" residual is closed.

- **Do:**
  - `conditions.go` (plan decision 3): the fixed queries over the metrics
    and logs adapters and Prometheus, built from validated IDs;
  - the Prometheus query client;
  - the built-in disk rule's lifecycle (decision 4) in
    `providerinstance.go`;
  - pin the queries' API versions in `VERSIONS.md`;
  - replace `conditions.go`'s T005 stub of `EvaluateCondition`;
  - give `core/notify`'s Checker the cluster's pod and Service ranges and
    the nodes' addresses (`SetClusterRanges`), refreshed with the
    cluster: nothing sets them yet (found in T005).
- **Files:** `providers/observability/openchoreo/conditions.go`,
  `prometheus.go`, tests, `VERSIONS.md`, and
  `core/reconcile/providerinstance.go`.
- **Verify:** `go test ./providers/observability/... ./core/reconcile/...`
  passes, using T001's recorded answers. The tests check:
  - each condition's request: the scope carries all three UIDs, and the
    PromQL holds only IDs;
  - the built-in rule is created on Ready, can't be deleted (409), and
    goes with the package.

## T007 — The API ✅ 2026-11-15

> **Done as:** `api/alerts_handler.go` (Alerts list with filters and
> `limit`, summary, get, acknowledge; rule silence and unsilence) and
> `api/notifications_handler.go` (channel, SMTP server and heartbeat
> views showing `secretSet`, never a value; platform default channels
> shown as `platformChannel` with their settings hidden from those who
> can't read them; test sends; the heartbeat singleton's routes). An
> `api.Notifier` (`core/notify`'s Client, `WithNotifier`) checks a
> destination when it's saved, after authorization: a refused address
> is a 400 with its reason, a name that doesn't resolve yet is left to
> the send. A test send answers 200, 422 (refused) or 502 (receiver),
> two at a time (429 beyond). Its record is the status write and that
> write's audit row. With no cluster it answers 503.
> - **Status is the server's** on rules, channels and SMTP servers
>   (`WithServerStatus` on the generic handler; `WithView` renders
>   responses). The generic PUT used to store a body's status, so a rule
>   PUT could set its own silence.
> - **The heartbeat loop** (`core/reconcile/heartbeat.go`) wasn't owned
>   by any task, so it's here: started with the evaluator, beating only
>   while Serendip can alert, writing status on change or every 10 minutes
>   (RP-078). `wireAlerting` is now built before `NewServer`, so test
>   sends share its sender.
> - The heartbeat's allowance is checked with `allowancesAt` at `/`, and
>   its URL Secret needs `secrets/value/read`. `builtInKept` and the
>   `WithAuthorize` checks are kept. `pendingRoutes` is gone, not just
>   emptied.
> - **Catalogue:** Alert reads, acknowledge, and rule CRUD, silence and
>   unsilence are MCP tools. Channel, SMTP server and heartbeat writes, and
>   the channel test, are CLI-only, with a reason. The CLI groups are
>   `channels` and `smtp`, and `get` replaces `show` (plan amended).
>   `docs/cli-and-mcp.md` is updated, and stale `alerts` exclusions are
>   gone.
> - `resource.ValidateSilence`: a reason, and an end in the future at most
>   30 days away.
>
> **Reviews:** contract-guardian passed with no blocking findings. Its
> non-blocking findings are fixed: the channel list keeps label filters,
> the heartbeat PUT is decided before validation, a varying failure
> message keeps the condition's transition time, and
> `core/reconcile/CLAUDE.md` names the loop. go-reviewer found no
> blockers. Fixed from its list:
> - the heartbeat's schedule after a pause, a failure or a conflict;
> - Alert lists filter before authorizing;
> - test-send records don't turn a sent test into an error;
> - the cap on test sends;
> - a version-less heartbeat PUT retries a status-only conflict;
> - the channel's destination check runs after authorization;
> - the acknowledge text.
>
> Left as is, by design:
> - acknowledging a resolved Alert is allowed (someone may own its
>   follow-up);
> - one Secret read per channel for `secretSet` (RP-078);
> - three heartbeat store constructors, each setting the unique key that
>   enforces the singleton.
>
> **Azure analogues:** acknowledging is Azure Monitor's alert state
> change ("Acknowledged"). Silences are alert processing rules that
> suppress notifications for a time. A test send is an action group's
> "Test action group".

- **Do:**
  - the routes of plan decision 12;
  - secrets shown as `secretSet`, and platform channels' settings hidden
    from readers below `/`;
  - test send;
  - `api/catalog` entries;
  - an audit row for every mutation, acknowledgement, silence and test
    send;
  - empty `authz_routes_test.go`'s `pendingRoutes` (T003) as the routes
    are registered; the heartbeat's allowance checked with
    `allowancesAt` at `/`, and keep the rule and channel handlers'
    `WithAuthorize` checks (T003) when replacing the generic routes.
- **Files:** `api/alerts_handler.go`, `api/notifications_handler.go`,
  `api/catalog/*.go`, `api/server.go`, and tests.
- **Verify:** `go test ./api/...` passes, including the catalogue's
  route-parity test. Handler tests cover:
  - a channel response holding no secret value;
  - a private URL refused, at create and at test, with the reason;
  - an Owner's allowance accepted;
  - Alerts scoped to the caller's Projects;
  - an acknowledgement audited with its actor and interface.

## T008 — CLI and MCP [P] [A] ✅ 2026-11-15

> **Done as:** the commands and tools were already built from T007's
> catalogue entries; T008 adds the cap and the tests.
> - **The cap:** a catalogue `Param.MCPMax` (`api/catalog/catalog.go`)
>   is the parameter's bound on MCP and the value an MCP tool sends when
>   it's left out. `alerts_list`'s `limit` has `MCPMax: 50`; the CLI keeps
>   the API's 100 default and 500 bound. No `mcp/` change was needed.
> - **CLI tables** show an Alert's state (`firing`, `resolved`) as its
>   status.
> - **Tests** (`cmd/serendip/alerts_test.go`, against the real API
>   handler): every decision-13 command's request (dry run) and 14 usage
>   errors, none of which sends anything; the commands against the
>   server (list, get, ack, summary, silence, unsilence); 60 Alerts → 50
>   through MCP, 51 refused, the CLI gets 60; and the token test: the
>   bot token, read by the sender during a test send, appears in none of
>   nine CLI outputs or five MCP calls, while each shows the channel.
>   `TestMCPMax` in `api/catalog`.
> - `docs/cli-and-mcp.md`: "Alerts and notifications".
> - **"Summarised"** in decision 13 is `alerts_summary` plus the cap: 50
>   Alerts, each with at most `MaxAlertDeliveries` deliveries, stay
>   well inside `mcp.MaxResult`, so no MCP-only view was added.
> - No `core/` or `providers/` change, so no contract-guardian review.

- **Do:** the commands and tools of plan decision 13, built from the
  catalogue, with the MCP results capped.
- **Files:** `cmd/serendip/*.go`, `mcp/*.go`, and tests.
- **Verify:** `go test ./cmd/serendip/... ./mcp/...` passes. It covers
  each command's request and usage errors, the MCP caps, and a test that
  a channel's token never appears in CLI or MCP output.

## T009 — The console [P] ✅ 2026-11-15

> **Done as:**
> - **Screens:** `ui/src/screens/Alerts.tsx` (a Component's Alerts
>   section on the Observability tab, an Application's firing Alerts and
>   own rules, the Alerts page at `/alerts` with state and severity
>   filters and the packages' built-in rules, and an Alert's page at
>   `/alerts/{id}`, which the notifications' "View in console" opens,
>   with each delivery's state in words) and
>   `ui/src/screens/Notifications.tsx` (Settings › Notifications at
>   `/settings/notifications`: channels with a form per type, test
>   sends, SMTP servers with the Gmail and Brevo notes, and the
>   heartbeat with its last result). Words in `ui/src/lib/alerts.ts`.
> - **The top bar** is new in `Shell`: a bell with the firing counts in
>   words (`GET /v1/alerts/summary`, every 30 s). The rail gains
>   Settings. The Observability tab's "not available yet" is now Traces
>   only.
> - **New route, `POST /v1/secrets/{id}/telegramchats`** (plan decision
>   14 amended): "Find chat" needs the server to read the bot token, and
>   T007 had no route for `notify.FindTelegramChats`. Its rule is the
>   Secret's `serendip/secrets/value/read` data action, as naming the
>   token in a channel needs; it answers chats only. CLI `channels
>   find-chat`; off MCP (`notifySettings`). Tests in
>   `api/notifications_handler_test.go` and `cmd/serendip/alerts_test.go`.
> - **Acknowledge is checked at the Alert's rule:** the permissions
>   route takes only kinds in `resourcePaths`, which `Alert` isn't, and
>   an Alert inherits its rule's access.
> - **Rule deletes cascade** (`?cascade=true`): a rule owns its Alerts,
>   so the confirm lists them. `DeleteConfirm` takes a fixed `path` for
>   the heartbeat singleton.
> - **The package's removal wording** (`cmd/serendipd/packages.go`)
>   now says rules go inactive, open Alerts resolve as "package removed",
>   the built-in disk rule goes and channels stay (spec IN 13).
> - **Verified:** `pnpm typecheck`, `pnpm test` (171 tests, 15 new in
>   `ui/src/screens/alerts.test.tsx`: the bell's counts, ack, silence,
>   the rule form, why a notification wasn't sent, Owner-only
>   allowances, Secrets shown only as set or not with no `/value` call,
>   Find chat) and `pnpm build` pass. **Gzipped JS: 104.1 → 118.7 KiB
>   (+14.6 KiB; budget 160).** `go test ./api/... ./cmd/...` pass. The
>   screens are seen live in T010.

- **Do:** plan decision 14: alerts in the Observability tab, the
  Application view, the bell and the Alerts page, and Settings ›
  Notifications: each channel type's form (with the Telegram "Find chat"
  step and the notice that alert content leaves the laptop, RP-070), SMTP
  servers, and the heartbeat with its last result. Each view gets its "Same request through the API".
- **Files:** `ui/src/screens/Observability.tsx`, `Alerts.tsx`,
  `Notifications.tsx`, the top bar, and tests.
- **Verify:**
  - `npm test` and `npm run build` in `ui/` pass;
  - tests cover the bell's counts, the allowance controls shown to
    Owners only, and a secret field that never shows its value;
  - the gzipped build's growth is stated.

## T010 — Live check on serendip-t018

- **Do:** spec acceptance criteria 1–11 on the real cluster, through the
  console (headless Chrome), the CLI and MCP:
  - metric, restart and log rules firing and resolving;
  - the disk rule, with its threshold lowered;
  - every channel type delivering:
    - a webhook to an in-cluster receiver (with the allowance) and to an
      outside one;
    - a real Telegram chat (cards, the button, threading), and an n8n
      flow following `docs/guides/alert-integrations.md`;
    - a channel with `minSeverity: critical` skipping a warning;
  - the heartbeat to healthchecks.io: pings arrive, stopping
    `serendipd` makes healthchecks.io alert, and so does stalling the
    evaluator (criterion 11);
    - email to a test SMTP server;
  - a failing receiver, and a 429;
  - each refused destination;
  - acknowledge and silence;
  - the side door, from a workload pod;
  - package removal.

  Then the secrets check (criterion 6): search for each known token in
  the store, audit rows, events, Alerts, `serendipd`'s log, and the CLI
  and MCP output.
- **Files:** `specs/024-alerts/verification.md`. The tooling is kept
  outside the repo, as spec 023's was.
- **Verify:**
  - `verification.md` has evidence for each of criteria 1–11;
  - each known secret is found only in the Secrets provider;
  - findings are fixed, or carried as sub-tasks.

## T011 — Benchmarks and measured cost

- **Do,** on the simulated T470 (`bench-t470`), with the harness
  extended:
  - a rule-evaluation phase: 200 rules at 1 minute, beside spec 023's
    log load;
  - evaluation latency, and `serendipd`'s CPU and memory;
  - store writes a minute;
  - resync CPU;
  - edge-1 with the package and no rules, against spec 023's run.
- **Files:** `profiles/edge-1/bench/`, `cmd/bench-edge1/`, and
  `profiles/edge-1/bench/results/2026-11-*-spec024-simulated-t470/`.
- **Verify:**
  - the results README has each number;
  - `serendipd` stays within constitution 4's control-plane budget;
  - there's no regression without rules (criterion 12);
  - the harness's own tests pass.

## T012 — Reviews

- **Do:** fix the findings (fix, don't defer) of:
  - `go-reviewer` over the Go changes;
  - `contract-guardian` over `core/` and `providers/`, including the
    `core/notify` reading of constitution 1 (plan decision 5);
  - `security-review` over the branch, with the sender, the address
    check, escaping and secrets called out.
- **Files:** as the findings require.
- **Verify:**
  - each review's findings are fixed, or recorded as by design with the
    reason;
  - `go test ./...`, `go vet ./...` and the UI tests pass.

## T013 — Docs and close-out [A]

- **Do:**
  - `operations.md`: rules, Alerts, channels, the bodies and the
    signature;
  - `docs/guides/alert-integrations.md`: the n8n recipe and the heartbeat
    monitors;
  - `resource-model.md`, `capability-model.md`,
    `application-platform.md` ("Alerts (spec 024)"), `authorization.md`
    and `approvals.md`;
  - ADR 0022;
  - `build-vs-adopt.md`:
    - adopted: Telegram's Bot API, SMTP relays and the webhook, as
      protocols; healthchecks.io, Uptime Kuma and n8n as suggested
      outside tools (not dependencies);
    - rejected: OpenChoreo's alerting path, Alertmanager, unofficial
      WhatsApp clients and email-to-SMS;
    - open: SMS, voice, WhatsApp Cloud and OTP;
  - the risk register: RP-069 to RP-074 added, and RP-062, RP-066 and
    RP-068 updated;
  - `docs-keeper`, then `docs/PROGRESS.md`.
- **Files:** as listed.
- **Verify:**
  - `docs-keeper` reports no drift;
  - every RP the plan names is indexed in the risk register;
  - `PROGRESS.md` names spec 024 done.
