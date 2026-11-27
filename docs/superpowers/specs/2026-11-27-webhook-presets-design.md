# Webhook presets for Slack, Mattermost and Telegram — design

Date: 2026-11-27. Backlog 4.7 ("Channels: Telegram, Slack, Mattermost native"). The design was
approved by the owner on 2026-10-19 and lived only in the backlog row; this document records it.
Section "Owner decisions" quotes the row verbatim. Everything under "Derived details" is the
implementer's derivation (marked **[derived]**), made on 2026-11-27 while the owner was away, and
recorded again as rulings in the plan (`docs/superpowers/plans/2026-11-26-webhook-presets-4.7.md`).

## Owner decisions (approved 2026-10-19, verbatim from backlog 4.7)

> Finding that shaped it: all three are HTTP POSTs, and the existing `webhook` kind already does
> method, sealed URL, sealed header values, a body template and HMAC signing — so a user can reach
> Slack today by hand-writing a template. What "native" buys is the absence of that handwriting,
> not a transport.
>
> **Decision 1: presets on the webhook kind, not three new kinds.** A `preset` column (slack /
> mattermost / telegram, empty = today's raw mode), chosen over first-class kinds because the
> latter would touch the CHECK, `View()`, `Send()`, the GUI kind picker and every channel picker in
> routes, rotations and escalation steps — and again for each of 4.8's four services. Precedent:
> inbound webhook triggers already carry `preset` (github, gitlab) on one kind.
>
> **Decision 2: a preset owns the body**, so the template field disappears when one is set; two
> sources for one body is the duplication that has bitten this repo twice.
>
> **Decision 3: rich per-service rendering**, chosen over reusing `renderEmail`'s plain text —
> Slack blocks with an attachment colour by severity and a button to `Event.URL`, Mattermost
> sharing Slack's attachment format but getting its own tests because "compatible" is a claim
> until proven, Telegram Markdown with `chat_id` as a non-secret config field and the bot token
> inside the sealed URL.
>
> **Decision 4:** where one event batches alerts of mixed severity, the highest decides the colour
> — the convention the alert list already rolls up by.
>
> Unchanged by all of it: delivery, outbox, retries, signing, and every channel picker, because the
> kind stays `webhook`. The scope is deliberately shaped so each of 4.8's services is one renderer
> plus one line of validation.

And from backlog 4.8 (decided 2026-11-12): split `internal/notify`'s channel code into one package
per channel, the layout 3.52 gave the agent plugins.

## Derived details

### Package layout [derived]

- `internal/notify` keeps the channel kinds, storage, validation, the outbox and the sender, and
  gains the extension point: `type Preset interface { CheckURL(*url.URL) error;
  CheckSettings(PresetSettings) (PresetSettings, error); Render(Event, PresetSettings) ([]byte, error) }`
  and `type Presets map[string]Preset`. It also gains the service-neutral message every chat
  renderer starts from: `Compose(Event) Message` (title, level, lines with optional links, the
  count the event itself left out, the link to Pulsix) and small shared helpers (`ChatText`,
  `Clip`, `MoreNote`, `LevelColor`). `notify` never imports a preset (that would be a cycle).
- `internal/notify/presets/<name>/` holds one package per service (`slack`, `mattermost`,
  `telegram`), each exporting only `New()`; `internal/notify/presets` holds `Registry()`, which
  `internal/app` and `internal/api/apitest` wire into the sender and the service — the 3.52 layout.
- `internal/notify/notifytest` holds the event fixtures and the golden-file helper the preset
  tests share (and 4.8's will).
- The e-mail and raw webhook code stays in `internal/notify`: moving it out means moving the
  config building, sealing and sending that the service and sender share, which is not cheap.
  4.8 does that split; a 4.8 service as a preset is one package with `New()` plus a registry line.

### Data model [derived]

Migration `00136_notification_channel_presets.sql`, additive: `notification_channels.preset text
NOT NULL DEFAULT ''` with `CHECK (preset IN ('', 'slack', 'mattermost', 'telegram'))` and
`CHECK (preset = '' OR kind = 'webhook')`. A constant default only touches the catalog; every
existing row is raw (`''`) and passes both checks. 4.8 widens the first CHECK (widen-only, the
pattern 00133 used for trigger outcomes).

Telegram's `chat_id` is stored unsealed in the webhook config JSON (`chat_id`), like the method:
it is not a secret. The bot token stays inside the sealed URL. Nothing else in the stored config
changes; a preset channel stores an empty body template.

### API [derived]

Additive, on the existing channel routes:

- Read (`webhook` object): `preset` (`""`, `slack`, `mattermost`, `telegram`), `chat_id`
  (Telegram only). The URL keeps coming back as scheme and host only; the token-bearing path never
  appears in an answer, a log line or a delivery error.
- Create and update (`webhook` body): `preset` (default `""`), `chat_id`.

### Validation per preset [derived]

Common to every preset:

- method `POST` only (empty defaults to it);
- the body template must be empty (decision 2);
- no `Content-Type` header (the preset owns the body and sends `application/json`);
- other headers and request signing stay allowed and unchanged (a relay in front of the service
  may want them);
- changing a channel's preset (raw to preset, preset to raw, or between presets) needs the URL
  submitted again: the URL is service-specific and a kept, sealed URL cannot be re-checked.

Per service:

- **Slack:** any URL the raw webhook accepts. No host check: Slack-compatible receivers and relays
  exist, and the destination is already policed at send time (internal/outbound).
- **Mattermost:** any URL the raw webhook accepts (Mattermost is self-hosted; no host can be
  required).
- **Telegram:** the URL path must be `/bot<id>:<token>/sendMessage` (any host, so a self-hosted
  Bot API server works); the error never echoes the URL. `chat_id` is required: a chat id
  (`-?[1-9][0-9]{0,19}`) or a public `@username` (5–32 characters, letter first, then letters,
  digits, underscores).
- A raw channel takes no `chat_id`.

### Rendering [derived]

All three start from `Compose(Event)`. Every string an operator or a host chose (organization,
schedule, template, host, check, item, metric, detail) passes through `ChatText` first (the
existing 7.42 defusing: `@ < > [ ]` become look-alikes, control and format characters U+FFFD),
then through the format's own escaper. Links are only rendered for `http(s)` URLs made of safe
characters.

Level (the colour) [derived beyond decision 4]: an alert batch takes the highest item severity in
the alert list's order (crit > warn > unknown); an item whose event is `closed` counts as ok, so
an all-closed batch is green. A run that failed or was cancelled is crit, a succeeded run ok; a
skipped schedule warn, one that could not start crit; the digest and the test message info.
Colours are the brand tokens (crit `#F87171`, warn `#FBBF24`, unknown `#6B7280`, ok `#34D399`,
info `#00E5FF`).

- **Slack** (decision 3): top-level `text` is the title (≤ 150 characters, mrkdwn-escaped:
  `&`, `<`, `>`). Slack shows it as the message body, because the blocks sit in an attachment, so
  no header block repeats it (review M1). One attachment with the level colour holds blocks:
  `section` blocks of mrkdwn lines (≤ 3000 characters each, at most 10), a `context` block naming
  the organization, and an `actions` block with one button ("Open in Pulsix", URL `Event.URL`)
  when the event has a link. mrkdwn's `*`, `_`, `~` (at a word's edge) and `` ` `` in chosen
  text become look-alikes, since Slack has no escape for them (review M3).
- **Mattermost**: Slack's legacy attachment format, which Mattermost renders: `fallback`, `color`,
  `title`, `title_link` (`Event.URL`), `text` (Markdown, ASCII punctuation backslash-escaped,
  ≤ 12000 characters) and `footer`. No button: Mattermost buttons need an integration callback;
  the title links to Pulsix instead.
- **Telegram**: `sendMessage` with `parse_mode: HTML` instead of MarkdownV2 — the dispatch for
  4.7 allows it, and HTML needs four escapes (`& < > "`) where MarkdownV2 needs eighteen. Bold
  title prefixed with a level mark (the colour's stand-in), lines, then an "Open in Pulsix" link;
  link previews off. Total ≤ 4096 UTF-16 units.

Bounds: each line is clipped first (label 100, text 300 characters); lines are kept in order
while they fit the service's budget and a byte budget (40 KiB as JSON, so wide characters and long
links stay under the sender's 64 KiB cap — review I1), and the rest — together with what the event itself left out
(a capped alert batch) — is announced as "… and N more".

### Delivery [derived]

`ChannelSender.Send` asks the preset to render the body when the channel has one and uses the
raw template path otherwise. Everything after the body is unchanged: method, headers, the
`X-Pulsix-*` headers, signing, the outbound client, the status classification, retries, the
outbox and the URL redaction in errors. "Send test" goes through the same `Send`, so it renders
through the preset. An unknown preset at send time (a registry that lacks it) fails the delivery
permanently.

### GUI [derived]

The webhook form of the channel dialog gains a "Format" select (Raw webhook, Slack, Mattermost,
Telegram). With a preset set, the method select and the body template disappear, Telegram adds a
"Chat ID" field, and the URL placeholder shows the service's URL shape. Changing the format while
editing requires the URL again (the field becomes required). The channel list's kind column
names the service ("Webhook · Slack").

### Version skew [derived]

An older manager still running during the roll ignores the column and sends a preset channel's
deliveries as the raw event JSON, which the service refuses (a permanent failure), and its API
neither shows nor keeps-checking the preset. Stop every manager instance before the new version
starts (docs/upgrades.md).

## Out of scope

Telegram forum topics (`message_thread_id`), Slack threads, per-route mentions, message editing
on resolve, and the 4.8 services.

## Addendum: backlog 4.8 — ntfy, Pushover, Twilio SMS, PagerDuty

Date: 2026-11-27. Everything here is **[derived]** by the 4.8 implementer while the owner was
away, from the owner decisions above ("each of 4.8's services is one renderer plus validation")
and recorded again as rulings in `docs/superpowers/plans/2026-11-26-webhook-presets-4.8.md`.
4.8 adds four presets, not four `notify.Kind`s; the backlog row's older wording is updated.

### What the preset interface gains

Two of the four services do not fit "one JSON body to the channel URL": Twilio takes a
form-encoded body with basic auth, PagerDuty takes one event per alert, ntfy's JSON publish goes
to the server root. So `Render` returns requests instead of bytes:

- `Render(ev Event, t Target) ([]Request, error)`. `Target` is what a send opened: the stored
  `PresetSettings`, the opened channel URL and the opened preset secrets — held for one send only.
- `Request{Path, ContentType, Header, Body}`: `Path` replaces the channel URL's path when set
  (same scheme and host, so the outbound policy and the stored destination still decide where a
  request goes); `ContentType` defaults to `application/json`; `Header` is set after the operator's
  headers (the preset's own `Authorization` wins); X-Pulsix headers and signing apply to every
  request. A delivery is sent when every request succeeded. (Review round 1 replaced the first
  version's "first failure fails, a retry resends all": see "Review fixes" below.)
- Two optional interfaces: `Endpointed` (`Endpoint(PresetSettings) string`) — Pulsix builds the
  URL from the settings, the operator submits none; `Credentialed` (`SecretNames() []string`,
  `CheckSecret(name, value string) error`) — secrets beyond the URL and headers.

The three 4.7 presets render one JSON request each and are otherwise unchanged.

### Storage

- Migration `00137` widens `notification_channels_preset_check` to add `ntfy`, `pushover`,
  `twilio`, `pagerduty` (drop and re-add; every stored row passes).
- New clear settings in the webhook config JSON, like `chat_id`: `account_sid`, `from`, `to`
  (Twilio), `region` (PagerDuty, `us` or `eu`).
- New sealed secrets in the webhook config JSON: `secrets: [{name, value_sealed}]`, sealed with
  the channel's AAD exactly like header values; never returned (the read shows their names), never
  logged, never echoed in an error; a backup carries them sealed, like every other channel secret.
  On update an omitted secret keeps its stored value while the preset stays the same; rotation is
  entering a new value. Changing the preset drops the old preset's secrets.

### Per service

| | URL | Settings | Secrets | Body |
|---|---|---|---|---|
| ntfy | the topic URL, any host (self-hosted servers), last path segment a topic `[-_A-Za-z0-9]{1,64}`, no query; the error never repeats it | — | — (an access token is an operator `Authorization: Bearer …` header, sealed) | JSON publish to the server root: `topic`, `title`, `message` (plain, ≤ 4096 bytes), `priority` (crit 5, warn 4, unknown and info 3, ok 2), `tags` (a level emoji shortcode), `click` = link |
| Pushover | built: `https://api.pushover.net/1/messages.json` | — | `token`, `user` (30 letters/digits each) | form: `token`, `user`, `title` (≤ 250), `message` (plain, ≤ 1024 characters), `priority` (crit 1, warn and unknown 0, ok and info −1; never 2), `url` (≤ 512) + `url_title`, `timestamp` |
| Twilio SMS | built: `https://api.twilio.com/2010-04-01/Accounts/<SID>/Messages.json` | `account_sid` (`AC` + 32 hex), `from` (E.164 or an alphanumeric sender ID), `to` (one E.164 number) | `auth_token` (32 letters/digits), sent as basic auth with the SID | form: `From`, `To`, `Body` — plain text folded to the GSM-7 alphabet where a look-alike has one, at most two SMS segments (306 GSM-7 septets, else 134 UTF-16 units) |
| PagerDuty | built: `https://events.pagerduty.com/v2/enqueue`, or `events.eu.pagerduty.com` for `region: eu` | `region` | `routing_key` (32 letters/digits) | see below |

PagerDuty (Events API v2): an alert batch sends one event per alert it carries (at most 50, the
batch's own cap), `dedup_key` `pulsix:alert:<alert id>`: `closed` resolves, `acknowledged` and
`snoozed` acknowledge, every other alert event triggers with `severity` crit → critical,
warn and unknown → warning. A failed or cancelled run triggers (`error`, key `pulsix:run:<run id>`),
a schedule that could not start triggers (`error`) and a skipped one too (`warning`), keyed by
schedule and due time; a succeeded run, the digest and Send test are **change events**
(`/v2/change/enqueue`), which show on the service's timeline and page nobody. A trigger carries
`summary` (≤ 1024), `source` (the host, else the organization), `component` (check), `class`
(metric), `group` (organization), `timestamp`, `custom_details.lines` and a link to Pulsix.

Every renderer starts from `Compose` (or, per alert, `Compose` of a one-item batch), so the
7.42 defusing applies; the SMS fold turns its look-alikes back into the GSM-7 characters, since
SMS has no markup. The 40 KiB byte budget applies to every request body.

### API, GUI

- Read (`webhook`): `account_sid`, `from`, `to`, `region`, `secrets` (names only). Create and
  update: the same settings and `secrets` as an object of name → value; the `url` must be left
  out for Pushover, Twilio and PagerDuty.
- The Format select gains the four. Pushover, Twilio and PagerDuty hide the URL field and show
  their own fields (secrets as password fields that keep the stored value when left empty);
  ntfy keeps the URL and hints at the Authorization header. Twilio's dialog says every message is
  a billed SMS, and Send test on a Twilio channel asks for confirmation first.

### Version skew

As in 4.7: an older manager sends a new preset channel as the raw event JSON to the stored URL
(the service refuses it) — stop every manager before the new version starts.

### Review fixes (round 1, 2026-11-27)

- **Alert state at send time (I1).** Deliveries are not ordered per channel, so after an outage
  an alert's retried trigger could reach PagerDuty after its resolve and open an incident for a
  closed alert. The sender now reads, for a preset that follows alert state
  (`AlertStateAware`, PagerDuty only), which of the batch's alerts are closed — or gone with
  their host — at send time (`AlertStates`, an org-scoped read of `alerts.closed_at` wired by
  `internal/app`), and PagerDuty sends `resolve` for those whatever the event said. A failed
  read is a transient error.
- **A send budget and progress (I2).** A delivery's requests are bounded by half the lease (60 s);
  a request is only started while its own timeout still fits, so none is cut off mid-flight. What
  the budget does not reach is owed: the keys of the requests still owed (`Request.Key`; default
  their position, PagerDuty `alert:<item index>`) are stored in the delivery's existing
  `pending_recipients` column — the column that narrows an e-mail retry to the recipients still
  owed (backlog 2.74) — and the retry sends only those. A request that succeeded is never sent
  again (a resend of a failed delivery keeps the same narrowing). No migration.
- **Every request is tried (M1).** One failed request no longer stops the rest. The error names
  the failed ones by position and status ("request 3: the endpoint answered 400", at most five,
  then a count; no URL, no secret). The retry owes the failed and unreached ones; when only
  permanent refusals remain, the delivery fails permanently, like e-mail's partial semantics.
- **Twilio's token belongs to its account (M2).** `SecretScoped`: changing `account_sid` needs
  the auth token again (400: "changing account_sid needs the secret auth_token again"); the
  dialog makes the field required as soon as the SID differs from the stored one.
- **Unacknowledged sends nothing** to PagerDuty (a trigger would not undo its acknowledgement);
  a batch that leaves nothing to send is delivered as sent.
- **Owed keys survive no preset switch (re-review N1).** A key in the pending list is the
  preset's name plus the request's key (`pagerduty:alert:3`; `webhook:0` for a raw channel), and
  when the stored list names none of the requests rendered now — the channel was switched to
  another preset meanwhile, as an operator might during a PagerDuty outage — every request is
  sent. A final failure stores the owed keys too (as a retry does; e-mail recipients likewise), so
  a later Resend repeats only what was owed.
- **Known limit (M3), narrowed by backlog 4.43:** alerts beyond a batch's cap of 50 get no
  PagerDuty event. The follower drops closed items last when it caps a batch (every channel
  kind), so an incident opened earlier for an alert the batch resolves is still resolved; only
  a trigger or an acknowledgement beyond the cap is missed, and more than 50 resolves in one
  tick still leave the rest unresolved.

### Out of scope (4.8)

Splitting e-mail and the raw webhook out of `internal/notify` (the backlog row's 2026-11-12
note): its motivation was a package per new channel, which the presets now are; moving the two
existing kinds drags the shared config, sealing and sending along with no new caller. Pushover
emergency priority, devices and sounds; Twilio messaging services, API keys and several
recipients per channel (one channel per number); a PagerDuty event for the alerts a capped batch
leaves out; ntfy attachments, actions and Markdown.
