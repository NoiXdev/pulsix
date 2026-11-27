-- +goose Up
-- A webhook channel can carry a preset (backlog 4.7, spec
-- 2026-11-27-webhook-presets-design): a chat service whose message Pulsix
-- renders itself instead of the channel's body template. '' is today's raw
-- webhook. The kind stays 'webhook', so delivery, the outbox, retries,
-- signing and every channel picker are unchanged. A constant default only
-- touches the catalog, and every existing row ('' on any kind) passes both
-- checks. Backlog 4.8 widens the first CHECK by one value per service.
ALTER TABLE notification_channels ADD COLUMN preset text NOT NULL DEFAULT '';
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_preset_check
  CHECK (preset IN ('', 'slack', 'mattermost', 'telegram'));
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_preset_kind_check
  CHECK (preset = '' OR kind = 'webhook');

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_preset_kind_check;
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_preset_check;
ALTER TABLE notification_channels DROP COLUMN preset;
