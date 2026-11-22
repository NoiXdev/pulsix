import { expect } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import type userEvent from '@testing-library/user-event';
import type { MockRequest, MockRoute } from '../../test/mockApi.ts';
import { job, routes } from './playbookPageTestKit.tsx';

/* The Dry run tab's test harness, shared by the PlaybookPage.dryRun*.test.tsx
 * files: the newest dry run `d1` of `p1` on two hosts, its events (three
 * changes, one with a before/after diff, one with a prepared diff, one with
 * none) and recap counts, and the form's helpers. */

export const HOSTS = { hosts: [{ id: 'h1', name: 'web-01' }, { id: 'h2', name: 'web-02' }], next_cursor: '' };

export const changed = (seq: number, host: string, task: string, diff: unknown) => ({
  seq, attempt: 1, at: '2026-11-22T10:00:00Z', host, task, state: 'changed',
  payload: { event: 'runner_on_ok', event_data: { host, task, res: { changed: true, diff } } },
});

export const EVENTS = [
  { seq: 1, attempt: 1, at: '2026-11-22T10:00:00Z', host: 'web-01', task: 'Install package', state: 'ok', payload: { event: 'runner_on_ok' } },
  changed(2, 'web-01', 'Render nginx config', [{ before: 'listen 80;\nroot /srv;\n', after: 'listen 8080;\nroot /srv;\n', before_header: '/etc/nginx/site.conf', after_header: '/etc/nginx/site.conf' }]),
  changed(3, 'web-02', 'Render nginx config', { prepared: '--- old\n+++ new\n@@ -1 +1 @@\n-user www;\n+user nginx;\n' }),
  changed(4, 'web-02', 'Restart service', undefined),
];

export const STATS = {
  host_counts: {
    h1: { ok: 4, changed: 1, skipped: 1, failed: 0, unreachable: 0 },
    h2: { ok: 2, changed: 2, skipped: 0, failed: 1, unreachable: 1 },
  },
};

export const detail = (id: string, extra = {}) => ({
  job: {
    id, kind: 'run', status: 'succeeded', trigger: 'manual', check_mode: true, created_at: '2026-11-22T10:00:00Z', run_group_id: id, zone_id: 'z1',
    template: { id: 't1', name: 'Deploy web' }, hosts: ['h1', 'h2'], stats: STATS, attempts: 1, cancel_stops_play: true, chain: { follow_ups: [] }, group: [],
    vault_identities: [], vault_identities_source: 'recorded', ...extra,
  },
});

/** Routes for a page whose newest dry run is `d1` (or none). */
export function dryRoutes(role = 'operator', opts: { latest?: string | null; detailOf?: Record<string, unknown>; extra?: Record<string, MockRoute> } = {}) {
  const asked: URLSearchParams[] = [];
  const latest = opts.latest === undefined ? 'd1' : opts.latest;
  return {
    asked,
    routes: routes(role, {
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        asked.push(req.search);
        if (req.search.get('check_mode') === 'true') return { jobs: latest ? [job(latest, { check_mode: true })] : [], next_cursor: '' };
        return { jobs: [], next_cursor: '', total: 0 };
      },
      'GET /api/v1/orgs/acme/jobs/d1': opts.detailOf ?? detail('d1'),
      'GET /api/v1/orgs/acme/jobs/d1/events': { events: EVENTS },
      'GET /api/v1/orgs/acme/hosts': HOSTS,
      ...opts.extra,
    }),
  };
}

export const form = () => screen.findByRole('form', { name: 'Dry run' });

/** Waits for the templates, then presses Start dry run. */
export async function startIn(f: HTMLElement, user: ReturnType<typeof userEvent.setup>) {
  const button = within(f).getByRole('button', { name: 'Start dry run' });
  await waitFor(() => expect(button).toBeEnabled());
  await user.click(button);
}

