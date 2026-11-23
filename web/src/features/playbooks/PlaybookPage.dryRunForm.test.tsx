import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderHook, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { FakeEventSource } from '../runs/runPageTestKit.tsx';
import { dryRoutes, form, startIn } from './dryRunTestKit.ts';
import { job, renderAt, template, TEMPLATES } from './playbookPageTestKit.tsx';
import { useDraftTick } from './useDraftTick.ts';

/* The Dry run form's own state: what it keeps and what it lets go of when
 * the templates or the text under it change, and what it says when the
 * templates cannot be read. */

const failing = () => new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } });

describe('PlaybookPage Dry run tab, the form under change', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('starts the chosen hosts over when the one template there is changes under the form', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    let current = { templates: [template('t1', 'Deploy web', 'p1')] };
    mockApi(
      dryRoutes('operator', {
        extra: {
          'GET /api/v1/orgs/acme/job-templates': () => current,
          'POST /api/v1/orgs/acme/playbooks/p1/dry-run': (req: MockRequest) => (sent.push(req.body), { jobs: [job('d1')] }),
        },
      }).routes,
    );
    const { qc } = renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await user.click(await within(f).findByRole('checkbox', { name: 'web-01' }));
    expect(within(f).getByRole('checkbox', { name: 'web-01' })).toBeChecked();
    current = { templates: [template('t2', 'Deploy staging', 'p1')] };
    await qc.invalidateQueries();
    await waitFor(() => expect(within(f).getByRole('checkbox', { name: 'web-01' })).not.toBeChecked());
    await startIn(f, user);
    await waitFor(() => expect(sent).toEqual([{ template_id: 't2', diff: true }]));
  });

  it('keeps the chosen hosts when the same template is read again', async () => {
    const user = userEvent.setup();
    mockApi(dryRoutes('operator', { extra: { 'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1')] } } }).routes);
    const { qc } = renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await user.click(await within(f).findByRole('checkbox', { name: 'web-01' }));
    await qc.invalidateQueries();
    await new Promise((r) => setTimeout(r, 50));
    expect(within(f).getByRole('checkbox', { name: 'web-01' })).toBeChecked();
  });

  it('says the templates could not be read, and reads them again on Retry', async () => {
    const user = userEvent.setup();
    let ok = false;
    mockApi(dryRoutes('operator', { extra: { 'GET /api/v1/orgs/acme/job-templates': () => (ok ? TEMPLATES : failing()) } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    expect(await within(f).findByRole('alert')).toHaveTextContent('The templates could not be read, so a dry run cannot be started.');
    expect(within(f).queryByText(/No template runs this playbook yet/)).not.toBeInTheDocument();
    ok = true;
    await user.click(within(f).getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(within(f).queryByRole('alert')).not.toBeInTheDocument());
    await pickOption(within(f), 'Template (brings variables and credentials)', 'Deploy web');
  });

  it('says so in German', async () => {
    mockApi(dryRoutes('operator', { extra: { 'GET /api/v1/orgs/acme/job-templates': failing } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run', 'de');
    const f = await screen.findByRole('form', { name: 'Probelauf' });
    expect(await within(f).findByRole('alert')).toHaveTextContent('Die Vorlagen konnten nicht gelesen werden');
    expect(within(f).getByRole('button', { name: 'Erneut versuchen' })).toBeInTheDocument();
  });
});

describe('useDraftTick', () => {
  it('lets go of its tick once there is no draft to check, and starts unticked when one returns', () => {
    const { result, rerender } = renderHook(({ can }) => useDraftTick(can), { initialProps: { can: true } });
    expect(result.current[0]).toBe(false);
    result.current[1](true);
    rerender({ can: true });
    expect(result.current[0]).toBe(true);
    rerender({ can: false });
    expect(result.current[0]).toBe(false);
    rerender({ can: true });
    expect(result.current[0]).toBe(false);
  });
});
