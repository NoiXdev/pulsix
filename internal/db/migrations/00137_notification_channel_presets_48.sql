-- +goose Up
-- Backlog 4.8 adds four webhook channel presets (spec
-- 2026-11-27-webhook-presets-design, addendum 4.8): ntfy, Pushover, Twilio
-- SMS and PagerDuty. Widen-only: every stored row passes the new CHECK.
-- Their settings and sealed secrets live in the channel's config JSON
-- like the other webhook settings, so no column changes.
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_preset_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_preset_check
  CHECK (preset IN ('', 'slack', 'mattermost', 'telegram', 'ntfy', 'pushover', 'twilio', 'pagerduty'));

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
UPDATE notification_channels SET preset = '' WHERE preset IN ('ntfy', 'pushover', 'twilio', 'pagerduty');
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_preset_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_preset_check
  CHECK (preset IN ('', 'slack', 'mattermost', 'telegram'));
