-- +goose Up
-- Alert rules for checks under any key (backlog 3.13e, spec
-- docs/superpowers/specs/2026-11-13-rules-any-check-key-design.md). Org,
-- group and host rules now judge every check that reports their metric,
-- whatever the check's key; an item-scoped rule names one tuple, and so
-- names the check's key as well: item_check_key, the instance key of the
-- check it judges (e.g. 'nginx-internal'). check_key keeps the plugin's
-- key, derived from the metric as before — the rules page filters on it.
--
-- NULL means the plugin's key, which is what every existing rule meant, so
-- the column needs no backfill and every existing row already satisfies
-- the CHECK below. Nullable and without a default, the ADD COLUMN touches
-- only the catalog. Only an item-scoped rule may carry one.
ALTER TABLE check_rules ADD COLUMN item_check_key text;
ALTER TABLE check_rules ADD CONSTRAINT check_rules_item_check_key_scope
  CHECK (item_check_key IS NULL OR scope = 'item');

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE check_rules DROP CONSTRAINT check_rules_item_check_key_scope;
ALTER TABLE check_rules DROP COLUMN item_check_key;
