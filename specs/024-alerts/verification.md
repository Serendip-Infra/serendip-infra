# Spec 024 verification (T010, real cluster)

Run 2026-11-15 on `serendip-t018`: kind, `kindest/node:v1.34.0`, with
the spec 017–023 stack (OpenBao, kgateway, OpenChoreo 1.3.1, ThunderID,
the builds and observability packages). Times are UTC unless marked
local (UTC+05:30). Tokens, passwords, keys and the heartbeat URL were
never printed: they were read from files and compared in memory.

## Setup

- **`serendipd`:** built from `spec-024` after T009 (`da3484a`), with the
  console embedded, on `127.0.0.1:8088`. It was rebuilt as fixes landed
  (F1–F8 below), and restarted for the heartbeat and button checks. The
  user's own dev `serendipd` (on `t023.db`) was stopped for the run, as
  in spec 023's T013, since console login is registered for
  `localhost:8088` only.
- **Store:** a copy of spec 023 T013's (`t023.db`), so its Projects,
  keys and the installed observability package carried over.
- **API keys** (from spec 023): `verifier` Owner at `/`; `t014-reader`
  Reader at `/`; `t014-contrib` Contributor at Project `t014-builds`;
  `t023-other` Contributor at Project `t023-other`. **The person:**
  `admin`, who also holds the Secrets data role. Channels, SMTP servers
  and the heartbeat were made through the CLI as `admin` (they name
  Secrets, and naming one needs its value's data action). `serendip mcp`
  acted as `agent:serendip-mcp/<admin>`.
- **The workload:** one Component, `t024-alerty`, in `t023-app` /
  `t023-dev`, from a busybox image built for the run whose `MODE`
  environment variable makes it idle, hold memory, crash or print
  `T024-KNOWN-ERROR` every 5 s. One Component, because the node's CPU
  requests were at 99% (every workload pod asks 100m). Modes were
  switched through the Deployment's `overrides.env`.
- **Receivers:**
  - an "outside" webhook receiver on the host, reached through a
    Cloudflare quick tunnel (`*.trycloudflare.com`, public HTTPS); its
    path picks the answer (`/ok`, `/fail` 500, `/429` with
    `Retry-After: 20`, `/redirect` 302); it checks
    `X-Serendip-Signature`;
  - n8n Community Edition 1.121.3 in Docker, behind its own tunnel, with
    a flow following plan decision 6's recipe: a Webhook node (raw body)
    → a Code node checking the signature and its age → a forward
    (standing in for ntfy or Slack), with a `call`/`push` route by
    severity. The webhook URL (its path is a token) went in through
    `urlSecretId`;
  - the same receiver on the host's LAN address (`192.168.1.20:9911`);
  - a receiver pod in a scratch namespace, for the in-cluster refusals.
- **Outside services, the user's (free):** a Telegram bot and chat, a
  Gmail app password (`smtp.gmail.com:587`, STARTTLS) and a
  healthchecks.io check (1 min period, 3 min grace).
- **Tooling** is outside the repo, in `~/.serendip-t018/t024/` (API, CLI
  and MCP helpers, console scripts, the image, the receiver, evidence).
  Screenshots were looked at and aren't committed.

## Evidence, by acceptance criterion

1. **A metric rule fires and resolves.** `t024-memory`: memory `value`
   above 100 MiB for 1 minute, warning, to the outside, critical-only,
   n8n, LAN, Telegram and email channels.
   - **Fired** at 12:25:39 at 167.8 MiB, about 2 minutes after the load
     started. Each channel got it: the receiver logged a
     `serendip.alert.v1` `firing` body with a valid signature; n8n
     forwarded it (route `push`); Telegram message 80; the email.
   - **One Alert:** its `resourceVersion` went 3 → 7 → 16 over repeated
     evaluations, and there was one Alert for the rule throughout.
   - **Resolved** at 12:30:24, 1 minute after the load stopped, at
     1.4 MiB (`resolvedReason: clear`). The resolution reached every
     channel. In Telegram it was a reply to the firing card ("✅
     RESOLVED … Memory back under 100 MiB (now 1.402) after 5m0s"); in
     Gmail it followed the firing.
   - **The console:** the bell read "1 warning"; the Alerts page and the
     Application view listed it; the Alert's page showed each delivery
     ("Sent", "Not sent: below the channel's minimum").
2. **Restart and log rules.**
   - **Restarts** (`t024-restarts`, above 2 in 5 min, critical): the
     memory mode's pod was OOMKilled (256 MiB limit) and crash-looped.
     The rule fired at 12:28:24 at 3 restarts, and resolved after the
     mode changed. Telegram: "🔴 FIRING · critical … Restarts above 2
     in 5 min (now 3)", then the resolution as a reply.
   - **Logs** (`t024-log`, more than 3 lines containing
     `T024-KNOWN-ERROR` in 2 min): fired at 12:29:39 at 8 lines, about
     25 s after the log mode's pod started, and resolved at 12:34:09
     after the mode went idle.
3. **Built-in disk rule.** A platform default channel was made, and the
   `observability-disk` threshold lowered from 85% to 1%: it fired at
   12:30:54 at 1.64%, and the default channel got the signed body. Back
   at 85%, it resolved. Changing `builtIn` was refused (400 "builtIn
   can't be changed"); deleting it was refused (409 "a built-in rule
   goes with its package; disable or silence it instead").
4. **Delivery on every channel type.**
   - **Telegram:** the test message, firing cards with the summary,
     where, condition, value and rule, and each resolution threaded as
     a reply (seen in the chat by the user). **The button:** with the
     console URL `http://localhost:8088`, there's no button and the URL
     is in the text, as decided (T001: Telegram refuses `localhost`).
     Restarted with `SERENDIP_PUBLIC_URL=http://127.0.0.1:8088`, the
     card carried "View in console" (message 84). The Alert's page it
     names was checked in the console on `localhost`.
   - **"Find chat"**, from the console's channel form and `serendip
     channels find-chat`: it listed the private chat; the token wasn't
     on the page or in the output.
   - **Email through a real relay (Gmail):** the test, the firing and
     the resolution arrived in the inbox. **No separate SMTP test server
     was used live:** an SMTP server has no private-destination
     allowance (plan decision 5), so a local mail catcher can't be one.
     Creating a public throwaway inbox (ethereal.email) was denied in
     this session. The SMTP sender's protocol cases (STARTTLS, implicit
     TLS, auth only after TLS) are T004's tests against an in-process
     server.
   - **Webhook:** the outside receiver, the LAN receiver and the
     platform default channel each got the documented body with a valid
     signature. **n8n** forwarded every signed body, and an unsigned
     probe was not forwarded.
   - **A 429 is honoured:** the `/429` channel's retries came 23 s and
     36 s after the previous attempt, against 12 s and 45 s for the
     `/500` one (`Retry-After: 20` beats the 10 s backoff). Telegram's
     `retry_after` itself wasn't provoked live (T001 sent 70 messages
     without a 429); T004's tests cover its shape.
   - **A failing receiver:** `t024-fail`'s only channel answers 500.
     Attempts at 12:45:25, :35, 12:46:05, 12:47:05 and 12:52:06, then
     `failed`, attempts 5, last error "…trycloudflare.com: 500 Internal
     Server Error" (host only). The receiver logged exactly those five.
   - **Minimum severity:** `t024-outside-critical` (`minSeverity:
     critical`) recorded `belowMinSeverity` for the memory warning and
     got the critical restart alert.
5. **Destination validation** (create, as the Owner, through the CLI;
   each refused with its reason):
   - `127.0.0.1`, with and without the allowance: "a loopback,
     link-local, multicast or reserved address";
   - `192.168.1.20` without the allowance: "a private address; an Owner
     can allow a private destination for this channel";
   - the receiver's Service IP, a pod IP and the node's address, **with**
     the allowance: "in the cluster's own ranges";
   - names resolving to each (`127.0.0.1.nip.io`, `192.168.1.20.nip.io`,
     `10.96.248.173.nip.io`): "resolves to a refused address";
   - `http://example.com`: "HTTPS only, unless an Owner allows plain
     HTTP for this channel";
   - **update:** the outside channel changed to `https://127.0.0.1`:
     refused the same way;
   - **redirect:** created (the URL is fine), and its test send refused:
     "302 redirect refused".

   **With an Owner's allowance**, `http://192.168.1.20:9911` (an
   in-house receiver on the LAN) was created and its test send arrived,
   signed. A Contributor sending the same allowance got 403 "missing
   …/allowPrivateDestination/action … only an Owner may". Spec
   criterion 5 said "an in-cluster receiver"; plan decision 5 keeps the
   cluster's ranges refused even with the allowance, which is what ran,
   so the criterion's text was amended (F7).
6. **Secrets stay secret.** Nine known values (the bot token and its
   secret half, the Gmail app password, the heartbeat URL and its token
   path, three signing keys and n8n's webhook path) were searched as
   bytes (UTF-8 and UTF-16) in 30 places: the store's database and WAL
   (audit rows, events, Alerts, channels, approvals), `serendipd`'s log,
   fresh API, CLI and MCP output for every alerting route and tool, every
   evidence file, and the receivers' logs. **No hit.** The heartbeat URL
   was searched again with the value read back from the Secrets
   provider (the user's file had changed since), also no hit. The
   Secrets provider held each value (read back as `admin` and compared
   in memory). Channels, SMTP servers and the heartbeat show only "Value
   stored" in the console and `secretSet` in the API; failed sends name
   the host only.
7. **Reconciled.** A rule's status says whether it's evaluated and why
   not: `Evaluating=True (Evaluated)`, `False (NoData)` ("no data in the
   condition's window yet") while a new pod had no points, and
   `False (PackageNotReady)` after removal. The CLI's list shows it (F6).
   `lastEvaluated` and `lastValue` are flushed every few minutes, not on
   each pass (plan: "No write per evaluation"). **The measured parts
   (resync CPU, evaluation at the rule cap) are T011's.**
8. **Acknowledge and silence.**
   - **Acknowledge,** from the console's Alert page: "Acknowledged by
     admin", and `acknowledged: {by: person:admin, at: 12:28:35Z}`. It
     stayed on the Alert after it resolved.
   - **Silence** (`serendip alertrules silence --until … --reason`):
     `status.silence` records who, why and until. While silenced, the
     log Alert's resolution and its next firing were recorded with every
     delivery `silenced`; the firing reopened the same Alert (inside the
     5-minute reopen window, plan decision 2). Pending retries stopped
     too. `unsilence` cleared it.
9. **Authorization.**
   - **Reader:** reads Alerts and rules (200); creating a rule: 403
     "missing serendip/alertrules/write"; silencing: 403.
   - **Contributor at `t014-builds`:** lists 0 of the other Project's
     rules and channels; reading one of its rules or channels: 403;
     creating a rule on its Component: 403.
   - **Contributor at `t023-other`:** creates a rule on its own
     Component naming its own Project's channel (201); naming another
     Project's channel: 403 "a rule sends to the channels it names";
     creating a platform channel, an SMTP server or the heartbeat: 403
     at `/`; it sees no SMTP servers.
   - **An agent** (MCP): `alerts_summary`, `alerts_list` (at most 50:
     `limit: 500` is refused), `alerts_get`, `alertrules_silence` and
     `unsilence` work under its roles; `alertrules_delete` answered 403
     "an agent's delete needs an approval". Channel, SMTP and heartbeat
     tools aren't offered ("unknown tool").
10. **No side door.** From the workload's pod, nothing new answered:
    `serendipd` (through `host.docker.internal` and the LAN address,
    refused: it listens on loopback) and every observability-plane
    Service (kube-state-metrics, Prometheus, both adapters, the
    observer, Fluent Bit, OpenObserve) timed out.
11. **Heartbeat.** Set to `hc-ping.com` every minute through `serendip
    heartbeat set` (the URL a Secret); the test send and the loop's
    beats reached healthchecks.io (pings every minute, seen by the user).
    `serendipd` was stopped at 12:52:29; healthchecks.io went DOWN and
    alerted the user at 12:56 (18:26 local). After the restart, beats
    resumed (`lastSent` 13:03:40). **A stalled evaluator** is
    `core/notify`'s test (`notify_test.go`: the beat pauses with
    `PausedEvaluator` and nothing is sent). The console's Settings ›
    Notifications showed "Last result: Sent" and the last time.
12. **Removal,** from the console: the confirm listed the package, the
    built-in rule and its Alert, and said rules go inactive, open Alerts
    resolve as "package removed", the disk rule goes and channels stay.
    It was recorded as an Approval decided by policy, and finished in
    41 s. Then: no firing Alerts, the three that were open resolved as
    `packageRemoved`, every rule `Evaluating=False (PackageNotReady)`,
    all 12 channels kept. **Reinstalled** from the console (Ready in
    119 s): rules active again, the built-in rule back.
14. **Nothing else breaks.** After the fixes: `go vet ./...`,
    `go test ./...`, the UI's 172 tests and `pnpm build` (118.8 KiB
    gzipped) pass; this run is the real-cluster run.

Criteria 7's measurements and 13 (footprint) are T011's.

## Final end-to-end run (after the fixes)

At the user's request, one alert through every channel type on the
fixed build (13:37–13:43). A new Component, `t024-final`, logged
`T024-FINAL-ERROR` every 5 s. One rule (more than 3 lines in 2 min,
warning) named Telegram, Gmail, the outside webhook (a fresh tunnel),
the critical-only webhook and the LAN receiver. Before it: a test send
to the outside and LAN webhooks and a heartbeat test, all `sent`.
- **Fired** at 13:38:47 at 18 lines: Telegram message 87, the Gmail
  email, and signed `firing` bodies at the outside and LAN receivers
  (13:39:04). The critical-only channel recorded `belowMinSeverity`.
- **Resolved** at 13:42:17, about 2 minutes after the pod went idle:
  Telegram message 88 (a reply to 87), the email resolution, and signed
  `resolved` bodies at both receivers. Every delivery took one attempt.
- **One more F4 case:** the heartbeat test's times were still `+05:30`.
  The API's notifications handler had its own `time.Now` clock. It's UTC
  now (`api/server.go`), and the repeated test recorded
  `13:43:43…Z`.
- Then the heartbeat, rule and workload were deleted, and the tunnel,
  receiver and image removed.

## Findings, all fixed in T010

- **F1: one unreachable address used up the whole send.** The
  checked dialer tried a name's addresses in order, each with the full
  10 s budget. The tunnel's name has two IPv6 addresses, one unreachable
  from this network, so every webhook send timed out while curl
  (Happy Eyeballs) worked. Now the families alternate (RFC 8305's
  order) and each attempt gets an equal share of what's left, at least
  1 s, stopping once the budget is spent (`core/notify/dialer.go`,
  `dialer_internal_test.go`). Live: sends went through in about 3 s.
  contract-guardian: pass; its nits fixed.
- **F2: the Alerts page showed IDs.** "Where" showed Component and
  Environment IDs, and an Alert's page the rule's ID. The page now reads
  the Components and Environments the caller can see, and the Alert's
  page its rule's name, falling back to IDs (`Alerts.tsx`, a new test).
- **F3: the catalogue described fields that don't exist.** The
  resource document's description (every CLI `--resource` and MCP
  create/update, since spec 021) said `metadata (name, ownerId, …)` and
  `metadata.version`. The fields are `ownerRefs` and `resourceVersion`.
  The API drops unknown fields silently, so channels made with
  `ownerId` became platform channels. Description fixed
  (`api/catalog/operations.go`). **Carried:** the silent drop itself
  (sub-task below).
- **F4: times in local offsets.** The evaluator's and heartbeat's
  clocks, the test-send time and (found in the final run) the API's
  heartbeat-test time were stored as `+05:30` beside UTC everywhere
  else. They're UTC now, as every other reconciler's are.
- **F5: the allowance's hint promised an in-cluster receiver.** The
  form said "May reach a private address (in the cluster or on the
  LAN) … For an in-cluster receiver". It now says on the LAN, and that
  loopback, the metadata address and the cluster's own addresses are
  never allowed (`Notifications.tsx`).
- **F6: the CLI's rule list had an empty STATUS.** It read only a Ready
  condition; rules have `Evaluating`, the heartbeat `Sending`. It now
  falls back to the first condition with its reason (`commands.go`, a
  new test).
- **F7: spec criterion 5's wording** (in-cluster receiver) amended to
  plan decision 5's (an in-house LAN receiver; the cluster's ranges
  always refused).
- **F8: an unused 15-minute backoff step.** Five attempts use four
  waits; the fifth, 15 min, was dead, and the plan listed it.
  Dropped from `deliveryBackoff` and the plan.
- **Doc:** `docs/cli-and-mcp.md` said `alerts_list` answers at most 50
  "whatever it asks"; a larger `limit` is refused, and it now says so.

## Carried (sub-task)

- **The API ignores unknown fields in a resource document** (F3's
  cause). A typo'd or invented field (`ownerId`) is dropped and the
  request succeeds, which made Project channels into platform ones here.
  Refusing unknown fields touches every handler and client (console,
  CLI, MCP, bench), so it's for T012's reviews to decide, not a T010
  fix. RP-079.

## Also seen

- **OpenChoreo, once:** after an `overrides.env` change, the new pod
  started with the old values (the env ConfigMap is updated in place;
  the pod came up first). Later changes rolled correctly. Not
  reproduced; spec 020's area. Worth a `bug` issue if it recurs.
- **`serendip-t018` is CPU-full:** requests at 99% of 4 cores, so a
  rollout's surge pod stays Pending until the old pod is deleted. The
  run deleted it by hand on each mode change.
- **A tooling slip:** the first memory holder (`head | tail`) wrote
  about 200 MB of NUL bytes to the pod's log when its input ended.
  Fluent Bit truncated it at its buffer limit ("multiline message
  truncated"); the disk rule's value went from 1.56% to 1.62%. The pod
  and its log were replaced within minutes.
- **Pre-existing wording:** CLI validation errors repeat "validation
  failed: resource: validation failed:"; an Approval's title says
  "providerinstance"; CLI help says "Create a AlertRule".
- **n8n 1.121.3's `import:workflow`** needs a `versionId` in the file.
  The recipe in `docs/guides/alert-integrations.md` (T013) should say
  so, or import through the editor.
- **Clean-up:** the heartbeat was deleted before `serendipd` stopped;
  the test Component, the receiver namespace, n8n, the tunnels and the
  images were removed. The user revokes the scratch bot's token and the
  Gmail app password, and deletes the healthchecks.io check.
