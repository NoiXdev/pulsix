# Webhook presets: Slack, Mattermost, Telegram (backlog 4.7) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** A webhook channel can be set to a Slack, Mattermost or Telegram preset that renders a native, colour-coded message, so nobody hand-writes a body template for these services.

**Architecture:** A `preset` column on `notification_channels` (kind stays `webhook`). `internal/notify` declares the `Preset` interface, a service-neutral `Message` (`Compose(Event)`) and shared helpers; one package per service under `internal/notify/presets/<name>` renders it; `presets.Registry()` is wired by `internal/app` and `apitest`. Delivery, outbox, retries and signing are untouched: only the body source changes.

**Tech Stack:** Go, Postgres (migration 00136), sqlc, huma, React SPA.

**Spec:** `docs/superpowers/specs/2026-11-27-webhook-presets-design.md` (owner decisions of backlog 4.7 verbatim, derived details marked).

## Global Constraints

- The sealed URL stays sealed: never returned, logged, or echoed in a validation or delivery error (Telegram's token lives in its path).
- Raw webhooks behave exactly as before (existing `internal/notify` tests pass unchanged).
- Migration additive; `notify` never imports a preset package.
- Tests use httptest servers and fakes only; rendered bodies are pinned by golden files (`-update` rewrites them).
- Package tests only (`go test -race -timeout 900s ./internal/notify/...`, `internal/api` with `-timeout 1800s -run`), `golangci-lint run` and `gofmt -s -l` on touched packages before each commit.

## Rulings

- R1 Preset renderers live in `internal/notify/presets/{slack,mattermost,telegram}` with `presets.Registry()`; e-mail and raw webhook stay in `notify` — moving them drags the shared config/seal/send code along, not cheap; 4.8 does it. — Cost if wrong: one more move in 4.8.
- R2 `preset` is a column with two CHECKs (known values; webhook only). — Decision 1 names a column; the CHECK must be widened by 4.8 per service (one line each).
- R3 Telegram uses `parse_mode: HTML`, not MarkdownV2 — four escapes instead of eighteen, allowed by the dispatch; decision 3's point (rich text, not plain) holds. — Cost if wrong: one renderer swap.
- R4 Validation: preset ⇒ POST only, empty body template, no Content-Type header; headers and signing stay allowed. Slack and Mattermost accept any URL the raw kind does (no host check: compatible receivers, relays, self-hosting); Telegram's path must be `/bot<id>:<token>/sendMessage` on any host. — Cost if wrong: a stricter Slack host rule is one line.
- R5 Telegram `chat_id`: required, `-?[1-9][0-9]{0,19}` or `@` + 5–32 of `[A-Za-z][A-Za-z0-9_]*`; stored unsealed in the config JSON; refused on a raw channel.
- R6 Changing the preset on update needs the URL resubmitted. — The kept URL is sealed and service-specific; re-checking it would mean unsealing in validation.
- R7 Level: alert batch = highest item severity by `rules.StateRank` (crit > warn > unknown), a `closed` item counts as ok; run failed/cancelled crit, succeeded ok; schedule skipped warn, could-not-start crit; digest and test info. Colours are the brand tokens. — Decision 4 plus the closed-item rule (an all-closed batch must not be red).
- R8 All operator/host strings pass `ChatText` (7.42 defusing) before the format escaper; links only for safe `http(s)` URLs. — Defence in depth against mentions and markup injection; the cost is look-alike brackets in a name.
- R9 Bounds: label 100, text 300 characters per line; Slack header 150, sections ≤ 3000 × 10; Mattermost text ≤ 12000; Telegram ≤ 4096 UTF-16 units; dropped lines plus the batch's own omitted count become "… and N more".
- R10 Mattermost: no button (needs an integration callback); the attachment title links to `Event.URL`. Slack: button "Open in Pulsix". Telegram: an inline "Open in Pulsix" link, previews off.
- R11 Telegram level mark (🔴 crit, 🟡 warn, ⚪ unknown, 🟢 ok, none for info) stands in for the colour Telegram lacks.
- R12 Message wording is English, like the e-mail.
- R13 An unknown preset at send time fails permanently; validation refuses one at save time.
- R14 Version skew: an older manager sends a preset channel as raw JSON (the service refuses it) — documented: stop every manager before the new one starts.

## Review Focus

- Token leak: a Telegram URL refused by validation, a transport error, a 4xx — none carry the path. Pinned by `TestTelegramURLErrorNeverEchoesToken` (presets/telegram), `TestPresetSendErrorRedactsURL` (notify), API `TestNotifyChannelTelegramPreset` (create, read and refusals carry no token).
- Hostile names (`<!channel>`, `@everyone`, `*bold*`, `</b>`, `&amp;`, bidi overrides) in schedule/host/detail: golden `hostile` cases per service.
- A 50-alert batch with long details plus an already-capped count: `TestSlackBoundsLargeBatch`, `TestMattermostBoundsLargeBatch`, `TestTelegramBoundsLargeBatch` assert the limits and the "… and N more" total.
- Mixed severity batch colour and an all-closed batch: `TestComposeLevel` (notify).
- Raw channels unchanged: the existing send, service and API channel tests pass unchanged with the registry wired.
- Update paths: switching preset without URL refused; keeping the same preset without URL keeps it; template sent with a preset refused (`TestPresetValidation` table).

---

### Task 1: Migration, store and `Channel.Preset`

**Files:** `internal/db/migrations/00136_notification_channel_presets.sql`, `internal/db/queries/notifications.sql` (create/update insert `preset`, claim returns `c.preset`), regenerated `internal/db/gen`, `internal/notify/{channel.go,store_sql.go,dispatch_sql.go}`, `internal/backup/fixture_test.go` (a webhook row with a preset), `internal/notify/store_sql_test.go`.

- [x] Failing `TestSQLStoreKeepsPreset` (create, get, list, update, claim carry the preset); the CHECKs refuse an unknown value and an e-mail channel with a preset.
- [x] Migration + queries + `make sqlc` + mapping; green. Commit `feat(notify): store a webhook channel's preset`.

### Task 2: Preset interface, Compose and helpers in `notify`

**Files:** `internal/notify/preset.go` (interface, `PresetSettings{ChatID}`, `Presets`), `internal/notify/compose.go` (`Level`, `Line`, `Message`, `Compose`, `LevelColor`), `internal/notify/chat.go` (`ChatText`, `Clip`, `MoreNote`, `SafeLink`, `Fit`), tests `compose_test.go`, `chat_test.go`; `internal/notify/notifytest/` (fixtures `RunFailed`, `ScheduleSkipped`, `Digest`, `AlertsMixed`, `AlertsClosed`, `Hostile`, `LargeBatch`, `Test`; `Golden(t, path, got, update)`).

- [x] Failing table tests: `TestComposeLevel`, `TestComposeLines` per event kind, `TestFit` (budget, reserve, omitted), `TestSafeLink`, `TestClip`.
- [x] Implement; green. Commit `feat(notify): compose a service-neutral chat message`.

### Task 3: Service validation and sender dispatch

**Files:** `internal/notify/{service.go,validate.go,send.go,channel.go}` (`WebhookInput.Preset/ChatID`, `WebhookView.Preset/ChatID`, `webhookConfig.ChatID`, `Deps.Presets`, `NewChannelSender(..., presets)`), `internal/app/app.go`, `internal/api/apitest/apitest.go`, tests `preset_service_test.go`, `preset_send_test.go` with a fake preset.

- [x] Failing `TestPresetValidation` (table: unknown preset, PUT, template, Content-Type header, settings refused by the preset, URL refused by the preset with no URL in the message, chat_id on raw, preset change without URL, same preset kept without URL, raw→preset with URL) and `TestPresetSend` (fake preset's body posted with signing + X-Pulsix headers; unknown preset permanent; raw path untouched), `TestPresetSendErrorRedactsURL`.
- [x] Implement; green. Commit `feat(notify): webhook presets own the body`.

### Task 4: Slack renderer

**Files:** `internal/notify/presets/slack/{slack.go,slack_test.go,testdata/*.golden.json}`, `internal/notify/presets/registry.go` (+ test).
- [x] Golden tests over the fixtures; `TestSlackEscapes`, `TestSlackBoundsLargeBatch`, `TestSlackNoButtonWithoutLink`. Commit `feat(notify): Slack preset`.

### Task 5: Mattermost renderer

**Files:** `internal/notify/presets/mattermost/…`, registry line.
- [x] Golden tests; `TestMattermostEscapesMarkdown`, `TestMattermostBoundsLargeBatch`. Commit `feat(notify): Mattermost preset`.

### Task 6: Telegram renderer

**Files:** `internal/notify/presets/telegram/…`, registry line.
- [x] Golden tests; `TestTelegramCheckURL`, `TestTelegramURLErrorNeverEchoesToken`, `TestTelegramChatID`, `TestTelegramEscapesHTML`, `TestTelegramBoundsLargeBatch` (UTF-16 count). The end-to-end send through the real sender is the API test of Task 7. Commit `feat(notify): Telegram preset`.

### Task 7: API

**Files:** `internal/api/notify_dto.go`, `internal/api/notify_handlers_test.go`, regenerated `web/src/api/schema.d.ts` (`npm run api:gen`).
- [x] Failing `TestNotifyChannelTelegramPreset` (create, read shows preset, chat_id and host only; Send test posts sendMessage's body; bad chat id, path, template, unknown preset refused without the token) and `TestNotifyChannelSlackPreset` (Send test posts blocks; switching preset without URL refused, keeping it accepted). Commit `feat(api): webhook channel presets`.

### Task 8: SPA

**Files:** `web/src/features/notifications/ChannelDialog.tsx`, `channelColumns.tsx`, `web/src/i18n/{en,de}/notifications.ts`, tests `ChannelDialog.preset.test.tsx`.
- [x] Failing tests: picking Slack hides method and template and sends `preset: 'slack'`, no `body_template`; Telegram shows Chat ID and sends it; editing a preset channel and changing the format requires the URL; list shows "Webhook · Slack". Commit `feat(web): preset picker in the webhook channel dialog`.

### Task 9: Docs

- [x] Backlog 4.7 → done; `docs/upgrades.md` entry for 00136 and skew. Commit `docs: backlog 4.7 done`.
