# Webhook presets: ntfy, Pushover, Twilio SMS, PagerDuty (backlog 4.8) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** A webhook channel can be set to ntfy, Pushover, Twilio SMS or PagerDuty, each rendered natively, so nobody hand-writes a body, an auth header or an event-per-alert loop for them.

**Architecture:** Four more presets on the webhook kind (4.7's decision 1), one package each under `internal/notify/presets/`. `Preset.Render` returns `[]Request` (path, content type, headers, body) instead of one JSON body; two optional interfaces let a preset build its own URL (`Endpointed`) and take sealed secrets (`Credentialed`). Delivery, outbox, retries and signing stay as they are; the sender loops over the requests.

**Tech Stack:** Go, Postgres (migration 00137), huma, React SPA.

**Spec:** backlog 4.8 and the addendum "backlog 4.8" of `docs/superpowers/specs/2026-11-27-webhook-presets-design.md`.

## Global Constraints

- Secrets (preset secrets, the sealed URL, header values) never appear in an API answer, a log line, a validation or delivery error, or a backup in clear.
- Raw webhooks and the three 4.7 presets send byte for byte what they sent before (their golden files do not change).
- Migration additive (widen-only CHECK); `notify` never imports a preset package.
- Tests use httptest servers and fakes only: fixed-host presets take their base URL in `New(base)` (`""` = the real service), `presets.RegistryAt(base)` points all of them at one fake.
- Package tests only; `golangci-lint run` and `gofmt -s -l` on touched packages before each commit.

## Rulings

- R1 Presets, not kinds; the row's "split e-mail and webhook into packages" is left out — its motivation (a package per new channel) is met by `presets/<name>`; moving the two kinds moves shared config/seal/send code with no new caller. — Cost if wrong: one refactor item later.
- R2 `Render(ev, Target) ([]Request, error)` replaces `Render(ev, PresetSettings) ([]byte, error)`; 4.7 presets wrap their body in one JSON request. — One shape covers form bodies (Twilio, Pushover), basic auth, other paths (ntfy root, PagerDuty change events) and several requests (PagerDuty). — Cost if wrong: an interface reshuffle inside one package tree.
- R3 `Request.Path` replaces only the path of the channel's stored URL — scheme and host stay the stored destination, so the outbound policy still decides. Preset headers are set after the operator's (the preset's `Authorization` wins).
- R4 Several requests per delivery: sent in order, the first failure fails the attempt (its status decides transient/permanent), a retry resends all. Only PagerDuty sends several, and its `dedup_key` makes a resend idempotent. — Cost if wrong: duplicate PagerDuty log entries, no duplicate incidents.
- R5 Fixed hosts are built, not typed (`Endpointed`): Pushover, Twilio (SID in the path) and PagerDuty (`region` us/eu) refuse a submitted URL; the URL is rebuilt from the settings on every save and stored sealed like any other, so the sender, View and backups are unchanged. ntfy takes any host (self-hosting). — Cost if wrong: a relay in front of these services is impossible; a later "custom URL" is additive.
- R6 Secrets: a `secrets` list in the webhook config JSON, each value sealed with the channel AAD like header values; no column, so no migration beyond the CHECK. Names per preset (`token`, `user`; `auth_token`; `routing_key`), all required; an omitted one keeps its stored value only while the preset is unchanged; read returns names only. ntfy's access token stays an operator header (the existing sealed mechanism).
- R7 Clear settings: `account_sid`, `from`, `to` (Twilio), `region` (PagerDuty), next to `chat_id`; a preset refuses settings it does not take (also the 4.7 three, through one helper).
- R8 Twilio: one `To` per channel (several recipients = several channels, which routes and rotations already list); body plain text, look-alikes folded back to GSM-7 characters, bounded to two segments (306 septets GSM-7, else 134 UTF-16 units); basic auth SID:token. Send test asks for confirmation in the GUI (a billed SMS).
- R9 Pushover: form body, `html` off, title ≤ 250, message ≤ 1024 characters (never empty: the title stands in), `url` only when ≤ 512 and safe; priority crit 1, warn/unknown 0, ok/info −1, never 2 (emergency needs retry/expire/receipts).
- R10 ntfy: JSON publish to the server root (`Path` = the topic URL's parent), `message` ≤ 4096 bytes (the server's default limit), priority crit 5, warn 4, unknown/info 3, ok 2, one tag per level (an emoji shortcode), `click` = Pulsix link; Markdown off.
- R11 PagerDuty: per alert item `pulsix:alert:<id>`; closed → resolve, acknowledged/snoozed → acknowledge, other events → trigger (crit critical, warn/unknown warning). Non-alert events by `Compose` level: crit → trigger `error`, warn → trigger `warning` (run key `pulsix:run:<id>`, schedule key `pulsix:schedule:<id>:<due unix>`), ok/info (succeeded run, digest, test) → change event, which pages nobody. A capped batch's left-out alerts get no event. — Cost if wrong: Send test would page if it were a trigger; a change event proves the key without that.
- R12 Secrets and settings are validated by shape (Pushover 30 alnum, Twilio token 32 alnum, SID `AC`+32 hex, routing key 32 alnum, E.164 numbers); errors name the field, never the value.
- R13 Migration 00137 drops and re-adds `notification_channels_preset_check` with the four new values (the 00133 pattern).
- R14 Version skew as 4.7: an older manager sends new preset channels as raw JSON (refused by the service); stop every manager first.

## Review Focus

- Secret leak: create/read/refusals carry no secret (`TestNotifyChannelPushoverPreset`, `TestNotifyChannelTwilioPreset`, `TestNotifyChannelPagerDutyPreset`), stored config has none in clear (`TestPresetSecretsSealedAndKept`), a refused secret's error has no value (`TestPushoverEndpointAndSecrets`, `TestTwilioEndpointAndSecret`, `TestPagerDutyRegion`, and the "refused" cases of the API tests).
- Secret keep/rotation: omitted keeps, new replaces, preset change requires all again (`TestPresetSecretsSealedAndKept`).
- URL for built-host presets: a submitted URL refused, settings change rebuilds it (`TestPresetEndpoint`).
- PagerDuty mapping and dedup keys over a mixed batch, change events for test/digest (`TestPagerDutyGolden`, `TestPagerDutyActions`).
- SMS budget: two segments in both encodings, fold (`TestTwilioBodyBounds`, `TestTwilioFold`).
- Several requests: one failure fails the attempt, each request signed (`TestPresetSendSeveralRequests`).
- 4.7 presets unchanged: their golden tests pass with untouched golden files.

---

### Task 1: Requests, Target, Endpointed, Credentialed; sender and service

**Files:** `internal/notify/preset.go`, `send.go`, `service.go`, `validate.go`, new `preset_secrets.go`, `channel.go`; presets slack/mattermost/telegram (+ tests' call sites), `notifytest` (`Single`), tests `preset_test.go`, new `preset_secrets_test.go`.

- [x] Failing: `TestPresetSendSeveralRequests` (path override, content type, preset header wins, signature per request, the second failing fails the send), `TestPresetSecretsSealedAndKept`, `TestPresetEndpoint`, `TestPresetRefusesSettingsItDoesNotTake`.
- [x] Implement; 4.7 golden tests pass unchanged. Commit `feat(notify): presets render requests and take sealed secrets`.

### Task 2: Migration 00137, store round trip, backup fixture

**Files:** `internal/db/migrations/00137_notification_channel_presets_48.sql`, `internal/notify/store_preset_sql_test.go`, `internal/backup/fixture_test.go` (a pagerduty row with a sealed secret blob).
- [x] Failing `TestSQLStorePresetChecks` case for `pagerduty`; migration; green; `go test ./internal/backup/...`. Commit with the registry entries of Tasks 3–6 (the registry test pins registry = CHECK).

### Tasks 3–6: ntfy, Pushover, Twilio, PagerDuty renderers

One package each under `internal/notify/presets/<name>` with `testdata/*.golden.json` over `notifytest.Cases()`, plus `presets.RegistryAt`.
- ntfy: `TestNtfyGolden`, `TestNtfyPublishesToTheRoot`, `TestNtfyPriorityAndTags`, `TestNtfyCheckURL` (topic, query, no echo), `TestNtfyBounds` (4096 bytes, wide batch).
- Pushover: `TestPushoverGolden` (requests dumped by `notifytest.Dump`, a form body as an object), `TestPushoverFields`, `TestPushoverBounds` (1024/250, wide batch), `TestPushoverEndpointAndSecrets`.
- Twilio: `TestTwilioGolden`, `TestTwilioSettings`, `TestTwilioEndpointAndSecret`, `TestTwilioBodyBounds`, `TestTwilioFold`, `TestTwilioBasicAuth`.
- PagerDuty: `TestPagerDutyGolden`, `TestPagerDutyActions`, `TestPagerDutyNonAlertEvents`, `TestPagerDutyRegion`, `TestPagerDutyBounds`.
- Registry: `TestRegistryMatchesTheSchema` (unchanged), `TestRegistryAtPointsFixedHostsAtBase`.
Commits `feat(notify): ntfy preset`, `… Pushover preset`, `… Twilio SMS preset`, `… PagerDuty preset`, then `feat(notify): register ntfy, Pushover, Twilio and PagerDuty presets` (Task 2).

### Task 7: API

**Files:** `internal/api/notify_dto.go`, `internal/api/notify_presets_48_test.go`, `internal/api/apitest/apitest.go` (`WithNotifyPresets`), regenerated `web/src/api/schema.d.ts`.
- [x] Failing `TestNotifyChannelNtfyPreset`, `TestNotifyChannelPushoverPreset`, `TestNotifyChannelTwilioPreset`, `TestNotifyChannelPagerDutyPreset` (create, read without secrets, Send test reaches the fake, refusals without secrets, keep on update). Commit `feat(api): ntfy, Pushover, Twilio and PagerDuty presets`.

### Task 8: SPA

**Files:** `web/src/features/notifications/ChannelDialog.tsx`, new `PresetFields.tsx` and `presetSettings.ts`, `useChannelListActions.tsx`, `web/src/i18n/{en,de}/notifications.ts`, tests `ChannelDialog.preset48.test.tsx`, `ChannelsPage.rows.test.tsx` (the Twilio confirm).
- [x] Failing: each format shows its fields and sends them (no URL for the three built hosts, secrets only when typed); Twilio's cost note; Send test on a Twilio channel confirms first. Commit `feat(web): ntfy, Pushover, Twilio and PagerDuty in the channel dialog`.

### Task 9: Docs

- [x] Backlog 4.8 → done; `docs/upgrades.md` entry for 00137 and skew. Commit `docs: backlog 4.8 done`.
