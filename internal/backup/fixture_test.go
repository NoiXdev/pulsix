package backup_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// everyTableSQL puts at least one row into every table of the public schema,
// with raw SQL through a superuser connection rather than through services
// (ruling R5): what the round trip needs is a row in every table, and the
// services for all of them would dwarf the test. Rows that point at their
// own table (bastions, inventory_groups, jobs) are there so the restore has
// to cope with self-references. Values with a tab, a newline, a backslash
// and NULLs exercise COPY's text escaping.
//
// fillEveryTable fails naming every table this script leaves empty, so a
// migration that adds a table makes the round trip fail until a row for it
// is added here.
const everyTableSQL = `
INSERT INTO organizations (id, slug, name) VALUES ('00000000-0000-4000-8000-000000000001', 'fixture', 'Fixture');
INSERT INTO users (id, email, display_name, password_hash, locale) VALUES ('00000000-0000-4000-8000-000000000002', 'fixture@example.com', 'Fixture', 'hash', 'en');
INSERT INTO org_memberships (org_id, user_id, role) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000002', 'owner');
INSERT INTO credentials (id, org_id, name, kind, secret, created_by) VALUES ('00000000-0000-4000-8000-000000000005', '00000000-0000-4000-8000-000000000001', 'key', 'ssh_key', '\x01', '00000000-0000-4000-8000-000000000002');
INSERT INTO bastions (id, org_id, name, address, username, credential_id) VALUES ('00000000-0000-4000-8000-000000000006', '00000000-0000-4000-8000-000000000001', 'jump', '10.0.0.1', 'root', '00000000-0000-4000-8000-000000000005');
INSERT INTO bastions (id, org_id, name, address, username, via_bastion_id) VALUES ('00000000-0000-4000-8000-000000000106', '00000000-0000-4000-8000-000000000001', 'inner', '10.0.0.9', 'root', '00000000-0000-4000-8000-000000000006');
INSERT INTO zones (id, org_id, slug, name, bastion_id) VALUES ('00000000-0000-4000-8000-000000000003', '00000000-0000-4000-8000-000000000001', 'default', 'Default', '00000000-0000-4000-8000-000000000006');
INSERT INTO hosts (id, org_id, zone_id, name, address, facts, kind) VALUES ('00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000003', 'web-1', '10.0.0.2', '{"os": "linux", "note": "tab\there"}', 'client');
INSERT INTO host_ssh (host_id, org_id, username, credential_id) VALUES ('00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000001', 'root', '00000000-0000-4000-8000-000000000005');
INSERT INTO host_tags (host_id, org_id, key, value, source) VALUES ('00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000001', 'env', 'prod', 'manual');
INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial, heartbeat_interval_s) VALUES ('00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000001', 'agent', 'agent-serial', 120);
INSERT INTO agent_certificates (serial, org_id, host_id) VALUES ('agent-serial', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004');
INSERT INTO agent_enrollments (id, org_id, zone_id, host_id, secret_hash, expires_at, created_by) VALUES ('00000000-0000-4000-8000-000000000020', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000003', '00000000-0000-4000-8000-000000000004', '\x02', now() + interval '1 day', '00000000-0000-4000-8000-000000000002');
INSERT INTO host_connection_tests (id, org_id, host_id, ok, host_key_state) VALUES ('00000000-0000-4000-8000-000000000021', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', true, 'pinned');
INSERT INTO inventory_groups (id, org_id, name, bastion_id) VALUES ('00000000-0000-4000-8000-000000000014', '00000000-0000-4000-8000-000000000001', 'all', '00000000-0000-4000-8000-000000000006');
INSERT INTO inventory_groups (id, org_id, name, parent_id) VALUES ('00000000-0000-4000-8000-000000000114', '00000000-0000-4000-8000-000000000001', 'web', '00000000-0000-4000-8000-000000000014');
INSERT INTO inventory_memberships (group_id, host_id, org_id) VALUES ('00000000-0000-4000-8000-000000000114', '00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000001');
INSERT INTO monitoring_groups (id, org_id, name, selector) VALUES ('00000000-0000-4000-8000-000000000012', '00000000-0000-4000-8000-000000000001', 'linux', 'env=prod');
INSERT INTO shared_repos (id, name, url, branch) VALUES ('00000000-0000-4000-8000-000000000016', 'shared', 'https://example.com/shared.git', 'main');
INSERT INTO shared_job_templates (id, name, shared_repo_id, playbook_path) VALUES ('00000000-0000-4000-8000-000000000017', 'shared template', '00000000-0000-4000-8000-000000000016', 'site.yml');
INSERT INTO shared_check_definitions (id, key, plugin, interval_seconds) VALUES ('00000000-0000-4000-8000-000000000018', 'ping', 'ping', 60);
INSERT INTO git_repos (id, org_id, name, url, credential_id, vault_credential_id, shared_repo_id) VALUES ('00000000-0000-4000-8000-000000000007', '00000000-0000-4000-8000-000000000001', 'repo', 'https://example.com/repo.git', '00000000-0000-4000-8000-000000000005', '00000000-0000-4000-8000-000000000005', '00000000-0000-4000-8000-000000000016');
INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000007', '00000000-0000-4000-8000-000000000005', 'default');
INSERT INTO repo_var_sources (repo_id, org_id, commit_sha, synced_at, worker_reads, incomplete, left_out, notes, synced_ref, sync_queued_at) VALUES ('00000000-0000-4000-8000-000000000007', '00000000-0000-4000-8000-000000000001', 'a1b2c3d', now(), true, true, '["huge.yml"]', '[{"level": "warning", "code": "read_failed", "message": "tab\there"}]', 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855', now() - interval '1 minute');
INSERT INTO repo_playbook_sources (org_id, repo_id, path, result, content) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000007', 'site.yml', '{"path": "site.yml", "failed": false, "fields": [{"name": "env", "type": "string"}]}', E'- hosts: all\n\ttasks: [] # back\\slash');
INSERT INTO repo_playbook_sources (org_id, repo_id, path, result, content_left_out) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000007', 'big.yml', '{"path": "big.yml", "failed": false, "fields": []}', 'too_large');
INSERT INTO playbooks (id, org_id, repo_id, path, source, revision) VALUES ('00000000-0000-4000-8000-000000000008', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000007', 'site.yml', 'repo', 7);
INSERT INTO job_templates (id, org_id, name, playbook_id, scope, credential_id, shared_template_id) VALUES ('00000000-0000-4000-8000-000000000009', '00000000-0000-4000-8000-000000000001', 'deploy', '00000000-0000-4000-8000-000000000008', '{}', '00000000-0000-4000-8000-000000000005', '00000000-0000-4000-8000-000000000017');
INSERT INTO job_template_vault_credentials (org_id, template_id, credential_id, label) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000009', '00000000-0000-4000-8000-000000000005', 'default');
INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ('00000000-0000-4000-8000-00000000000b', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000003', 'worker-1', 'worker-serial');
INSERT INTO worker_certificates (serial, org_id, worker_id, not_before, not_after) VALUES ('worker-serial', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000b', now(), now() + interval '1 year');
INSERT INTO worker_enrollments (id, org_id, zone_id, name, secret_hash, expires_at, worker_id, created_by) VALUES ('00000000-0000-4000-8000-000000000022', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000003', 'worker-1', '\x03', now() + interval '1 day', '00000000-0000-4000-8000-00000000000b', '00000000-0000-4000-8000-000000000002');
INSERT INTO job_templates (id, org_id, name, playbook_id, scope, credential_id) VALUES ('00000000-0000-4000-8000-00000000012d', '00000000-0000-4000-8000-000000000001', 'follow-up', '00000000-0000-4000-8000-000000000008', '{}', '00000000-0000-4000-8000-000000000005');
INSERT INTO job_template_follow_ups (org_id, template_id, follow_up_template_id) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000009', '00000000-0000-4000-8000-00000000012d');
INSERT INTO run_follow_ups (id, org_id, parent_group_id, template_id, root_trigger, path) VALUES ('00000000-0000-4000-8000-00000000012e', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000023', '00000000-0000-4000-8000-00000000012d', 'manual', '{00000000-0000-4000-8000-000000000009}');
INSERT INTO jobs (id, org_id, zone_id, kind, template_id, host_ids, worker_id, run_group_id, actor_id) VALUES ('00000000-0000-4000-8000-00000000000a', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000003', 'run', '00000000-0000-4000-8000-000000000009', '{00000000-0000-4000-8000-000000000004}', '00000000-0000-4000-8000-00000000000b', '00000000-0000-4000-8000-000000000023', '00000000-0000-4000-8000-000000000002');
INSERT INTO jobs (id, org_id, zone_id, kind, run_group_id, depends_on, check_mode, dry_run, pinned_commit) VALUES ('00000000-0000-4000-8000-00000000010a', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000003', 'run', '00000000-0000-4000-8000-000000000023', '00000000-0000-4000-8000-00000000000a', true, '{"tags": ["web"], "diff": true, "draft": false}', '0123456789abcdef0123456789abcdef01234567');
INSERT INTO job_hosts (org_id, job_id, host_id, created_at) SELECT org_id, id, '00000000-0000-4000-8000-000000000004', created_at FROM jobs WHERE id = '00000000-0000-4000-8000-00000000000a';
INSERT INTO job_events (job_id, seq, org_id, at, state, payload) VALUES ('00000000-0000-4000-8000-00000000000a', 1, '00000000-0000-4000-8000-000000000001', now(), 'ok', '{"line": "back\\slash"}');
INSERT INTO job_idempotency_keys (org_id, idempotency_key, run_group_id) VALUES ('00000000-0000-4000-8000-000000000001', 'key-1', '00000000-0000-4000-8000-000000000023');
INSERT INTO job_triggers (id, org_id, kind, template_id, name, preset, auth, signature_header, secret, rate_limit, created_by, allow_cidrs) VALUES ('00000000-0000-4000-8000-000000000015', '00000000-0000-4000-8000-000000000001', 'template_run', '00000000-0000-4000-8000-000000000009', 'hook', 'pulsix', 'hmac', 'X-Signature', '\x04', '{}', '00000000-0000-4000-8000-000000000002', '{10.0.0.0/8}');
INSERT INTO trigger_buckets (trigger_id, org_id, tokens, updated_at) VALUES ('00000000-0000-4000-8000-000000000015', '00000000-0000-4000-8000-000000000001', 1.5, now());
INSERT INTO trigger_deliveries (id, org_id, trigger_id, at, outcome) VALUES ('00000000-0000-4000-8000-000000000024', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000015', now(), 'triggered');
INSERT INTO schedules (id, org_id, template_id, name, mode, cron, timezone, guards, created_by) VALUES ('00000000-0000-4000-8000-00000000000c', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000009', 'nightly', 'cron', '0 3 * * *', 'Europe/Berlin', '{}', '00000000-0000-4000-8000-000000000002');
INSERT INTO schedule_runs (id, org_id, schedule_id, due_at, decided_at, outcome) VALUES ('00000000-0000-4000-8000-000000000025', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000c', now(), now(), 'triggered');
INSERT INTO notification_channels (id, org_id, kind, name, config, created_by) VALUES ('00000000-0000-4000-8000-00000000000d', '00000000-0000-4000-8000-000000000001', 'email', 'ops', '{"to": ["ops@example.com"]}', '00000000-0000-4000-8000-000000000002');
INSERT INTO schedule_notifications (org_id, schedule_id, channel_id, events) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000c', '00000000-0000-4000-8000-00000000000d', '{failure}');
INSERT INTO notification_deliveries (id, org_id, channel_id, event_key, event_kind, payload, status, next_attempt_at, created_at, pending_recipients) VALUES ('00000000-0000-4000-8000-000000000026', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000d', 'event-1', 'test', '{}', 'pending', now(), now(), '{ops@example.com}');
INSERT INTO notification_digests (org_id, enabled, weekday, at_time, timezone, next_run_at) VALUES ('00000000-0000-4000-8000-000000000001', true, 1, '08:00', 'UTC', now());
INSERT INTO notification_digest_channels (org_id, channel_id) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000d');
INSERT INTO oncall_rotations (id, org_id, name, period_days, anchor, timezone, created_at, updated_at) VALUES ('00000000-0000-4000-8000-00000000000f', '00000000-0000-4000-8000-000000000001', 'primary', 7, now(), 'UTC', now(), now());
INSERT INTO oncall_rotation_channels (org_id, rotation_id, position, channel_id) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000f', 0, '00000000-0000-4000-8000-00000000000d');
INSERT INTO oncall_exceptions (id, org_id, rotation_id, starts_at, ends_at, channel_id, created_at) VALUES ('00000000-0000-4000-8000-000000000027', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000f', now(), now() + interval '1 hour', '00000000-0000-4000-8000-00000000000d', now());
INSERT INTO alert_routes (id, org_id, name, scope, created_at, updated_at, rotation_id) VALUES ('00000000-0000-4000-8000-00000000000e', '00000000-0000-4000-8000-000000000001', 'all', 'org', now(), now(), '00000000-0000-4000-8000-00000000000f');
INSERT INTO alert_route_channels (org_id, route_id, channel_id) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000e', '00000000-0000-4000-8000-00000000000d');
INSERT INTO alert_route_events (org_id, route_id, kind) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000e', 'opened');
INSERT INTO alert_route_severities (org_id, route_id, severity) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000e', 'crit');
INSERT INTO alert_route_breakthrough_severities (org_id, route_id, severity) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000e', 'crit');
INSERT INTO alert_route_steps (org_id, route_id, position, delay_seconds, rotation_id) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000e', 1, 60, '00000000-0000-4000-8000-00000000000f');
INSERT INTO alert_route_step_channels (org_id, route_id, position, channel_id) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-00000000000e', 1, '00000000-0000-4000-8000-00000000000d');
INSERT INTO alerts (id, org_id, host_id, scope, check_key, metric, opened_at, severity, peak_severity, severity_changed_at, closed_at, closed_reason) VALUES ('00000000-0000-4000-8000-000000000110', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'tuple', 'cpu', 'usage', now() - interval '1 hour', 'crit', 'crit', now() - interval '1 hour', now() - interval '10 minutes', 'stale');
INSERT INTO alerts (id, org_id, host_id, scope, check_key, metric, opened_at, severity, peak_severity, severity_changed_at, acknowledged_by, continues_alert_id) VALUES ('00000000-0000-4000-8000-000000000010', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'tuple', 'cpu', 'usage', now(), 'crit', 'crit', now(), '00000000-0000-4000-8000-000000000002', '00000000-0000-4000-8000-000000000110');
INSERT INTO alert_events (id, org_id, alert_id, at, kind, actor_id) VALUES ('00000000-0000-4000-8000-000000000011', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000010', now(), 'opened', '00000000-0000-4000-8000-000000000002');
INSERT INTO alert_notify_state (org_id, alert_event_id, state, decided_at) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000011', 'sent', now());
INSERT INTO alert_escalations (org_id, alert_id, route_id, position, fired_at) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000010', '00000000-0000-4000-8000-00000000000e', 0, now());
INSERT INTO check_definitions (id, org_id, key, plugin, interval_seconds, origin, binding_kind, created_by, shared_definition_id) VALUES ('00000000-0000-4000-8000-000000000019', '00000000-0000-4000-8000-000000000001', 'ping', 'ping', 60, 'shared', 'org', '00000000-0000-4000-8000-000000000002', '00000000-0000-4000-8000-000000000018');
INSERT INTO check_definitions (id, org_id, key, plugin, interval_seconds, binding_kind, group_id) VALUES ('00000000-0000-4000-8000-000000000119', '00000000-0000-4000-8000-000000000001', 'cpu', 'cpu', 60, 'group', '00000000-0000-4000-8000-000000000012');
INSERT INTO check_rules (id, org_id, name, scope, check_key, metric, severity, mode, ast, source) VALUES ('00000000-0000-4000-8000-000000000013', '00000000-0000-4000-8000-000000000001', 'cpu high', 'org', 'cpu', 'usage', 'crit', 'form', '{}', 'usage > 90');
INSERT INTO check_current (org_id, host_id, check_key, metric, value, at) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'cpu', 'usage', 42.5, now());
INSERT INTO check_eval_cursor (org_id, host_id, dirty_at) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', now());
INSERT INTO check_host_states (org_id, host_id, state, since, evaluated_at) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'ok', now(), now());
INSERT INTO check_item_decisions (id, org_id, scope, check_key, match_kind, pattern, decision, created_by) VALUES ('00000000-0000-4000-8000-000000000028', '00000000-0000-4000-8000-000000000001', 'org', 'disk', 'glob', '/mnt/*', 'ignore', '00000000-0000-4000-8000-000000000002');
INSERT INTO check_items (org_id, host_id, check_key, item, first_seen) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'disk', '/', now());
INSERT INTO check_samples (at, org_id, host_id, check_key, metric, value) VALUES (now(), '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'cpu', 'usage', 42.5);
INSERT INTO probe_assignments (org_id, agent_host_id, target_host_id, check_key, plugin, params, interval_seconds, address) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000004', 'http', 'http', '{}', 60, '10.0.0.2');
INSERT INTO probe_current (org_id, host_id, check_key, vantage, metric, value, at) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'http', 'manager', 'latency', 12.5, now());
INSERT INTO probe_samples (at, org_id, host_id, check_key, vantage, metric, value) VALUES (now(), '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'http', '00000000-0000-4000-8000-000000000004', 'latency', 12.5);
INSERT INTO check_state_changes (at, org_id, host_id, check_key, metric, from_state, to_state, rule_id, value) VALUES (now(), '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'cpu', 'usage', 'ok', 'crit', '00000000-0000-4000-8000-000000000013', 95);
INSERT INTO check_states (org_id, host_id, check_key, metric, state, since, rule_id, evaluated_at, unmeasured_at) VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 'cpu', 'usage', 'crit', now(), '00000000-0000-4000-8000-000000000013', now(), now());
INSERT INTO maintenance_windows (id, org_id, name, target_kind, host_id, starts_at, ends_at, created_by) VALUES ('00000000-0000-4000-8000-000000000029', '00000000-0000-4000-8000-000000000001', 'patch', 'host', '00000000-0000-4000-8000-000000000004', now(), now() + interval '1 hour', '00000000-0000-4000-8000-000000000002');
INSERT INTO org_job_settings (org_id) VALUES ('00000000-0000-4000-8000-000000000001');
INSERT INTO org_assistant_settings (org_id, enabled) VALUES ('00000000-0000-4000-8000-000000000001', true);
INSERT INTO run_requests (id, org_id, kind, template_id, template_revision, reason, requested_by_user, expires_at, overrides, pinned_commit) VALUES ('00000000-0000-4000-8000-0000000000f1', '00000000-0000-4000-8000-000000000001', 'run', '00000000-0000-4000-8000-000000000009', 1, 'fixture', '00000000-0000-4000-8000-000000000002', now() + interval '1 hour', '{"check":true,"diff":true,"extra_vars":{"release":"1.2"}}', '0123456789abcdef0123456789abcdef01234567');
INSERT INTO run_requests (id, org_id, kind, template_id, template_revision, reason, requested_by_user, status, expires_at, sync_job_id, expires_in_minutes) VALUES ('00000000-0000-4000-8000-0000000000f2', '00000000-0000-4000-8000-000000000001', 'run', '00000000-0000-4000-8000-000000000009', 1, 'sync first', '00000000-0000-4000-8000-000000000002', 'waiting_for_sync', NULL, '00000000-0000-4000-8000-00000000010a', 15);
INSERT INTO run_requests (id, org_id, kind, template_id, template_revision, reason, requested_by_user, status, expires_at, sync_job_id, expires_in_minutes, sync_error) VALUES ('00000000-0000-4000-8000-0000000000f3', '00000000-0000-4000-8000-000000000001', 'run', '00000000-0000-4000-8000-000000000009', 1, 'sync failed', '00000000-0000-4000-8000-000000000002', 'failed', now(), '00000000-0000-4000-8000-00000000010a', 30, 'the repository''s sync failed: fetch	failed');
INSERT INTO org_monitoring_settings (org_id, client_absent_days) VALUES ('00000000-0000-4000-8000-000000000001', 7);
INSERT INTO org_secrets (org_id, name, fields_sealed, field_names) VALUES ('00000000-0000-4000-8000-000000000001', 'fixture', '\x0f', '{community}');
INSERT INTO rotation_groups (id, org_id, credential_id, targets) VALUES ('00000000-0000-4000-8000-00000000002a', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000005', '[]');
INSERT INTO api_tokens (id, user_id, org_id, name, role, secret_hash) VALUES ('00000000-0000-4000-8000-00000000002b', '00000000-0000-4000-8000-000000000002', '00000000-0000-4000-8000-000000000001', 'ci', 'admin', '\x05');
INSERT INTO personal_tokens (id, user_id, name, secret_hash, kind, areas) VALUES ('00000000-0000-4000-8000-000000000033', '00000000-0000-4000-8000-000000000002', 'laptop', '\x10', 'assistant', '{jobs.read}');
INSERT INTO personal_token_grants (token_id, user_id, org_id, role) VALUES ('00000000-0000-4000-8000-000000000033', '00000000-0000-4000-8000-000000000002', '00000000-0000-4000-8000-000000000001', 'operator');
INSERT INTO org_token_settings (org_id, personal_tokens_allowed) VALUES ('00000000-0000-4000-8000-000000000001', false);
INSERT INTO audit_events (id, org_id, actor_id, actor_kind, action, object_type, object_id, summary, at) VALUES ('00000000-0000-4000-8000-00000000002c', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000002', 'user', 'fixture', 'host', 'web-1', E'line one\nline two\ttab \\ backslash', now());
INSERT INTO audit_events (id, org_id, actor_id, actor_kind, action, object_type, object_id, summary, at) VALUES ('00000000-0000-4000-8000-00000000012c', NULL, NULL, 'system', 'fixture', 'instance', '', 'no org', now());
INSERT INTO invites (id, org_id, email, role, token_hash, invited_by, expires_at) VALUES ('00000000-0000-4000-8000-00000000002d', '00000000-0000-4000-8000-000000000001', 'new@example.com', 'viewer', '\x06', '00000000-0000-4000-8000-000000000002', now() + interval '1 day');
INSERT INTO instance_ca (sealed) VALUES ('\x07');
INSERT INTO backup_runs (id, kind, slot, started_at, finished_at, state, object_name, size_bytes, actor_id) VALUES ('00000000-0000-4000-8000-000000000031', 'scheduled', '2026-11-01T03:00:00Z', '2026-11-01T03:00:00Z', '2026-11-01T03:01:00Z', 'succeeded', 'pulsix-20261101T030000Z-scheduled.pxbak', 42, '00000000-0000-4000-8000-000000000002');
INSERT INTO instance_settings (key, value) VALUES ('fixture', '{"a": 1}');
INSERT INTO passkeys (id, user_id, name, credential_id, public_key, attestation) VALUES ('00000000-0000-4000-8000-00000000002e', '00000000-0000-4000-8000-000000000002', 'key', '\x08', '\x09', 'none');
INSERT INTO recovery_codes (user_id, code_hash) VALUES ('00000000-0000-4000-8000-000000000002', '\x0a');
INSERT INTO sessions (id, user_id, secret_hash, expires_at) VALUES ('00000000-0000-4000-8000-00000000002f', '00000000-0000-4000-8000-000000000002', '\x0b', now() + interval '1 day');
INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at) VALUES ('00000000-0000-4000-8000-000000000032', '00000000-0000-4000-8000-000000000002', '\x0e', now() + interval '1 hour');
INSERT INTO user_preferences (user_id, density, page_size, lists) VALUES ('00000000-0000-4000-8000-000000000002', 'compact', 50, '{"hosts": {"columns": ["name"]}}');
INSERT INTO totp_secrets (user_id, secret_sealed) VALUES ('00000000-0000-4000-8000-000000000002', '\x0c');
INSERT INTO webauthn_ceremonies (id, kind, user_id, data, expires_at) VALUES ('00000000-0000-4000-8000-000000000030', 'login', '00000000-0000-4000-8000-000000000002', '\x0d', now() + interval '1 hour');
`

// fillEveryTable runs everyTableSQL through pool, a superuser connection to
// the shared test database, and fails naming every public table it left
// without a row.
func fillEveryTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, everyTableSQL); err != nil {
		t.Fatalf("fill every table: %v", err)
	}
	var empty []string
	for _, name := range publicTables(t, pool) {
		if rowCount(t, pool, name) == 0 {
			empty = append(empty, name)
		}
	}
	if len(empty) > 0 {
		t.Fatalf("the fixture leaves these tables empty; add a row for each to everyTableSQL: %s", strings.Join(empty, ", "))
	}
}
