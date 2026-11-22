import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { FakeEventSource } from '../runs/runPageTestKit.tsx';
import { dryRoutes, form } from './dryRunTestKit.ts';
import { OUTLINE, renderAt, template } from './playbookPageTestKit.tsx';

/* The Dry run form's notes: what --diff puts into the run log, and what
 * the warning about tasks that run for real cannot see. */

const REAL_LIST = 'These tasks run for real, even in a dry run (check_mode: false):';

describe('PlaybookPage Dry run tab notes', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('says under Show differences that diffs carry file contents into the run log', async () => {
    mockApi(dryRoutes().routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    const diff = within(f).getByRole('checkbox', { name: 'Show differences (diff)' });
    expect(diff).toHaveAccessibleDescription(/file contents from the hosts/);
    expect(diff).toHaveAccessibleDescription(/no_log or diff: false hide theirs/);
  });

  it('says so in German, addressing the reader as du', async () => {
    mockApi(dryRoutes().routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run', 'de');
    const f = await screen.findByRole('form', { name: 'Probelauf' });
    const diff = within(f).getByRole('checkbox', { name: 'Unterschiede zeigen (diff)' });
    expect(diff).toHaveAccessibleDescription(/Dateiinhalte von den Hosts/);
    expect(diff).toHaveAccessibleDescription(/setze/);
  });

  it('says the tasks that run for real are not checked yet while the outline loads', async () => {
    mockApi(dryRoutes('operator', { extra: { 'POST /api/v1/orgs/acme/playbooks/outline': () => new Promise(() => {}) } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    expect(await within(f).findByText('Tasks that run for real could not be checked yet: the outline is still loading.')).toBeInTheDocument();
    expect(within(f).queryByRole('list', { name: REAL_LIST })).not.toBeInTheDocument();
  });

  it('says the tasks that run for real could not be checked when the outline fails', async () => {
    const fail = () => new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } });
    mockApi(dryRoutes('operator', { extra: { 'POST /api/v1/orgs/acme/playbooks/outline': fail } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    expect(await within(f).findByText('Tasks that run for real could not be checked: the outline is not available.')).toBeInTheDocument();
  });

  it('says a shortened outline may miss tasks that run for real, and still lists those it has', async () => {
    mockApi(dryRoutes('operator', { extra: { 'POST /api/v1/orgs/acme/playbooks/outline': { ...OUTLINE, truncated: true } } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    expect(await within(f).findByText('The outline is shortened: tasks past its end that run for real could not be checked.')).toBeInTheDocument();
    expect(within(f).getByRole('list', { name: REAL_LIST })).toBeInTheDocument();
  });

  it('says a shortened outline may miss them even when it lists none', async () => {
    const plays = OUTLINE.plays.map((p) => ({ ...p, entries: p.entries.map((e) => ({ ...e, runs_in_check_mode: true })) }));
    mockApi(dryRoutes('operator', { extra: { 'POST /api/v1/orgs/acme/playbooks/outline': { ...OUTLINE, plays, truncated: true } } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    expect(await within(f).findByText('The outline is shortened: tasks past its end that run for real could not be checked.')).toBeInTheDocument();
    expect(within(f).queryByRole('list', { name: REAL_LIST })).not.toBeInTheDocument();
  });

  it('says a repository playbook cannot be checked for tasks that run for real', async () => {
    mockApi(dryRoutes('operator', { extra: { 'GET /api/v1/orgs/acme/job-templates': { templates: [template('t2', 'Deploy infra', 'p2')] } } }).routes);
    renderAt('/o/acme/playbooks/p2?tab=dry-run');
    const f = await form();
    expect(await within(f).findByText('Tasks that run for real could not be checked: repository playbooks have no outline.')).toBeInTheDocument();
  });

  it('says nothing more when the full outline was read', async () => {
    mockApi(dryRoutes().routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await within(f).findByRole('list', { name: REAL_LIST });
    expect(within(f).queryByText(/could not be checked/)).not.toBeInTheDocument();
  });

  it('speaks German about an outline it could not read', async () => {
    mockApi(dryRoutes('operator', { extra: { 'GET /api/v1/orgs/acme/job-templates': { templates: [template('t2', 'Deploy infra', 'p2')] } } }).routes);
    renderAt('/o/acme/playbooks/p2?tab=dry-run', 'de');
    const f = await screen.findByRole('form', { name: 'Probelauf' });
    expect(await within(f).findByText(/für Playbooks aus einem Repository gibt es keine Gliederung/)).toBeInTheDocument();
  });
});
