-- +goose Up
-- Backlog 2.74's retry half. An e-mail channel's delivery is one row for
-- the whole recipient list, so one refused mailbox failed the delivery and
-- the next attempt sent to EVERYONE again -- the recipients the server had
-- already accepted received the same alert on every attempt until the
-- delivery gave up. internal/mail now delivers per recipient and reports
-- who is still owed the message (mail.PartialError); this column is where
-- that answer survives until the retry.
--
-- NULL means "the channel's own recipient list", which is what every row
-- written before this migration means and what every first attempt means.
-- Only a retry after a partial failure narrows it, so the common row stays
-- NULL rather than carrying a copy of the channel's list.
--
-- Webhook deliveries never set it: a webhook has one destination, so
-- "which of them still need this" is not a question that arises.
ALTER TABLE notification_deliveries ADD COLUMN pending_recipients text[];

-- +goose Down
ALTER TABLE notification_deliveries DROP COLUMN pending_recipients;
