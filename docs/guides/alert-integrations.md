# Alert integrations

Serendip sends alerts to Telegram, to email through an SMTP relay, and to
a signed webhook (`docs/architecture/operations.md` "Alerts"). Everything
else (Slack, Discord, Teams, ntfy, SMS, phone calls) goes through the
webhook to a relay you run or rent. Separately, a **heartbeat** tells an
outside monitor that Serendip can still alert.

None of the tools below is installed, bundled or required by Serendip.
Free limits were checked on 2026-11-15 and 2026-11-16; check them again
before you rely on them.

## The rule that matters: watch from somewhere else

If the laptop or cluster is down, its own alerting is down too. The
heartbeat's monitor and anything that pages you about Serendip itself
**must not run on the same machine**.

| Profile | Alerts | Heartbeat monitor |
|---|---|---|
| `edge-1` (one person, one laptop) | Telegram; email through a Gmail app password or Brevo | healthchecks.io's free tier |
| `small-3`, `standard-5` (a small team) | the same, plus the webhook to a relay for calls or SMS | healthchecks.io, or Uptime Kuma on an always-free VM (Gatus needs a relay, below) |

## Telegram, in two minutes

1. In Telegram, talk to **@BotFather**, send `/newbot`, and copy the
   token.
2. Store it as a Secret (`serendip secrets create`, then
   `serendip secrets value set <id>`).
3. Send your new bot any message (or add it to a group and send one
   there).
4. In the console, **Settings › Notifications › New channel ›
   Telegram**, pick the Secret and press **Find chat**. It reads the
   bot's recent updates once and lists the chats
   (`serendip channels find-chat <secret-id>` does the same).
5. Press **Test**.

The token is the whole credential: anyone holding it can read the bot's
chats and send as it. If it leaks, `/revoke` it with BotFather (RP-069).

## Email

Use a relay on the internet, not a mail server on the laptop (home
addresses are on blocklists, and port 25 is often blocked).
- **Gmail with an app password:** `smtp.gmail.com`, port 587, `starttls`.
  About 500 messages a day, no domain needed. Create the app password
  under your Google account's security settings (2-Step Verification
  must be on).
- **Brevo:** 300 a day free, no card. Use its SMTP key, not your
  password.

An Owner creates the SMTP server (**Settings › Notifications › SMTP
servers**) and needs **Secrets User** on its two Secrets. Email channels
then name it.

## The webhook

A webhook channel `POST`s one `serendip.alert.v1` JSON body per Alert
event, signed with `X-Serendip-Signature: t=<unix>,v1=<hex HMAC-SHA256>`
over `t + "." + body` (the body and headers are in `operations.md`).
- **Always set a signing Secret** and check it in the receiver, with the
  age of `t`, so a replayed or forged request is refused.
- **If the URL holds a token** (an n8n webhook's path does), store the
  whole URL as a Secret and use `urlSecretId`; the channel then shows
  only its host.
- **The receiver must be reachable from the internet** (HTTPS), or be an
  in-house LAN address that an Owner allows with a reason
  (`allowPrivateDestination`). Loopback and the cluster's own addresses
  are always refused.

### Checking the signature (Node.js)

```js
const crypto = require('crypto');

// raw: the request body exactly as received (a Buffer), not re-encoded JSON.
function verify(raw, header, secret, maxAgeSeconds = 300) {
  const parts = Object.fromEntries(String(header || '').split(',').map(p => p.split('=')));
  if (!parts.t || !parts.v1) return false;
  if (Math.abs(Date.now() / 1000 - Number(parts.t)) > maxAgeSeconds) return false;
  const want = crypto.createHmac('sha256', secret).update(parts.t + '.').update(raw).digest('hex');
  return want.length === parts.v1.length &&
    crypto.timingSafeEqual(Buffer.from(want), Buffer.from(parts.v1));
}
```

## The n8n recipe

n8n Community Edition turns an alert into anything n8n can reach. It's
fair-code (the Sustainable Use License), not open source, and it wants
2–4 GB, so run it on another machine, not beside edge-1. This flow was
checked live with n8n 1.121.3 in spec 024 T010 (`specs/024-alerts/verification.md`).

**Settings n8n needs** (environment variables on the n8n container):
- `NODE_FUNCTION_ALLOW_BUILTIN=crypto`, so a Code node can
  `require('crypto')`;
- `N8N_BLOCK_ENV_ACCESS_IN_NODE=false` and
  `SERENDIP_SIGNING_SECRET=<the signing Secret's value>`, so the Code
  node reads the key from `$env`. Set the first explicitly: its default
  may differ between n8n releases.

**The flow:**
1. **Webhook** node: method `POST`, a long random path, response
   "Immediately", and the **Raw Body** option on (the body arrives as
   binary data `data`). Copy its production URL into a Secret, and use
   it as the channel's `urlSecretId`.
2. **Code** node, "Check signature":

   ```js
   const crypto = require('crypto');
   const secret = $env.SERENDIP_SIGNING_SECRET;
   const item = $input.first();
   const raw = await this.helpers.getBinaryDataBuffer(0, 'data');
   const sig = String(item.json.headers['x-serendip-signature'] || '');
   const parts = Object.fromEntries(sig.split(',').map(p => p.split('=')));
   const want = crypto.createHmac('sha256', secret).update(parts.t + '.').update(raw).digest('hex');
   const fresh = Math.abs(Date.now() / 1000 - Number(parts.t)) < 300;
   if (!parts.v1 || want.length !== parts.v1.length ||
       !crypto.timingSafeEqual(Buffer.from(want), Buffer.from(parts.v1)) || !fresh) {
     throw new Error('bad or stale signature: not forwarded');
   }
   const body = JSON.parse(raw.toString('utf8'));
   const a = body.alert;
   return [{ json: { event: body.event, severity: a.severity, state: a.state,
     summary: a.summary, component: a.component, environment: a.environment,
     consoleUrl: a.consoleUrl } }];
   ```
3. **Switch** on `severity` and `event`, to the branches below.

**Example branches:**

| Branch | Node | Cost |
|---|---|---|
| A Slack card | HTTP Request `POST` to a Slack incoming webhook, with a Block Kit body | free |
| An ntfy push (can break through a phone's silent mode with `Priority: urgent`) | HTTP Request `POST https://ntfy.sh/<long random topic>`, body the summary, headers `Title`, `Priority`, `Click: <consoleUrl>` | free on ntfy.sh, rate-limited; topic names are public, so make it unguessable |
| A phone call for `critical` firings | HTTP Request `POST https://api.twilio.com/2010-04-01/Accounts/<sid>/Calls.json`, basic auth, form fields `To`, `From`, `Twiml=<Response><Say>…</Say></Response>` | paid: about 3¢ a minute in the UK, plus a number |
| SMS | Twilio's SMS, or an Android phone as a gateway | paid per message, or only the SIM |

**Importing the flow from a file** with `n8n import:workflow`: n8n 1.121.3
refused a file without a `versionId`. Add one, or build the flow in the
editor.

**Lighter relays:** Apprise API (one small service, 150+ targets; it
checks no signature, so keep it on a trusted network or check the
signature in front of it) and Node-RED (Apache-2.0, runs on a Pi). Hosted
and free: Activepieces Cloud (1,000 runs a month) or Make (1,000 credits).

## Heartbeat monitors

Serendip's heartbeat sends a plain `GET` (or an empty `POST`) to a URL
every 1–10 minutes, **only while it can alert** (the evaluator, the
delivery loop and the store are working). When the beats stop, the
monitor's own timeout pages you. It can't set request headers, so the
monitor's token must be in the URL.

Set it as an Owner: store the monitor's URL as a Secret, then
`serendip heartbeat set` (or **Settings › Notifications › Heartbeat**),
and press **Test**.

### healthchecks.io (the suggested default)

1. Create a check with a **period** equal to the heartbeat's interval and
   a **grace** of a few intervals (spec 024 used 1 minute and 3 minutes).
2. Copy its ping URL (`https://hc-ping.com/<uuid>`, or
   `https://hc-ping.com/<ping-key>/<slug>`) into a Secret.
3. Add the integrations you want paged on (email, Telegram).

The free (Hobbyist) plan has 20 checks; SMS, WhatsApp and phone-call
credits are on paid plans. It's BSD-3, so it can be self-hosted later on
another machine. Don't use its `/fail` URL: Serendip never sends "fail",
because absence is the signal.

### Uptime Kuma

A **Push** monitor gives a URL like
`https://<kuma>/api/push/<token>?status=up&msg=OK&ping=`; put the whole
URL in the Secret. Set its heartbeat interval a little above Serendip's,
and its retries. It's Node.js, heavier than Gatus, with a friendlier UI.
Run it on another machine, such as an always-free VM.

### Gatus: not directly

Gatus's `external-endpoints` heartbeat needs its token in an
`Authorization: Bearer` header, which Serendip's heartbeat doesn't send.
Use it through a relay that adds the header (an n8n Webhook node on the
same VM, forwarding to `POST /api/v1/endpoints/<group>_<name>/external?success=true`),
or use healthchecks.io or Uptime Kuma instead.

### Not suggested

UptimeRobot's free tier (heartbeats and Telegram are paid), Better
Stack's free tier (no Telegram), Cronitor (5 checks), Site24x7 (free plan
email-only).

### Where to run a monitor

Always-free VMs (check before relying on them):
- **Google Cloud e2-micro** (1 GB, in `us-west1`, `us-central1` or
  `us-east1`): enough for Uptime Kuma or a relay, and not reclaimed for
  being idle.
- **Oracle Cloud Always Free:** more capacity, but an idle Always Free
  instance is reclaimed after 7 days unless the account is Pay As You
  Go, and a watcher is idle by nature.
- **Not:** Render or Koyeb free instances, which sleep and would miss the
  silence they're meant to catch.

**Watch the watcher:** a self-hosted monitor should itself ping
healthchecks.io, so a crashed VM is noticed too.
