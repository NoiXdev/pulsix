-- +goose Up
-- Backlog 7.48.2. When every attempt of a past scheduled slot was
-- interrupted (abandoned, none succeeded or failed), the ticker mails the
-- instance administrators once. notified_at, set on the slot's newest
-- attempt when that mail goes out, is the "once": a slot with a notified
-- attempt is never mailed again, and the conditional update that sets it
-- decides which manager sends the mail.
--
-- Additive and nullable: a row from before this migration was never mailed
-- about.
ALTER TABLE backup_runs ADD COLUMN notified_at timestamptz;

-- +goose Down
ALTER TABLE backup_runs DROP COLUMN notified_at;
