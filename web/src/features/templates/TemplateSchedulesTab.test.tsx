import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pageRoutes, renderTemplatePage, schedule } from './templatePageTestKit.tsx';

const SCHEDULES = '/o/acme/templates/t1?tab=schedules';
const list = async () => within(await screen.findByRole('list', { name: 'Schedules of this template' }));
const row = async (name: string) => within((await list()).getByRole('listitem', { name }));
const editor = () => within(screen.getByRole('region', { name: 'Schedule editor' }));

const editorRoutes = {
  'GET /api/v1/orgs/acme/schedules/s1': { schedule: schedule('s1') },
  'GET /api/v1/orgs/acme/schedules/s1/runs': { runs: [] },
  'GET /api/v1/orgs/acme/schedules/s1/notifications': { rules: [] },
  'POST /api/v1/orgs/acme/schedules/preview': { cron: '0 3 * * *', description: '', next: [] },
};

async function openMenu(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click((await row(name)).getByRole('button', { name: `Actions for ${name}` }));
  return screen.getAllByRole('menuitem').map((i) => i.textContent);
}

describe('TemplateSchedulesTab', () => {
  it("lists the template's schedules with state, timetable, next run and overlap", async () => {
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/schedules': {
            schedules: [
              schedule('s1', { guards: { overlap: 'skip' } }),
              schedule('s2', { paused: true, guards: { overlap: 'queue' } }),
              schedule('s3', { paused: true, paused_reason: 'assistant_change', review_request_id: 'rv1' }),
            ],
          },
        },
      }),
    );
    renderTemplatePage(SCHEDULES);
    const active = await row('nightly-s1');
    expect(active.getByText('active')).toBeInTheDocument();
    expect(active.getByText('0 3 * * *')).toBeInTheDocument();
    expect(active.getByText(/every day at 03:00/)).toBeInTheDocument();
    expect(active.getByText(/^next in /)).toBeInTheDocument();
    expect(active.getByText('skips while a run is active')).toBeInTheDocument();
    const paused = await row('nightly-s2');
    expect(paused.getByText('paused')).toBeInTheDocument();
    expect(paused.getByText('no run while paused')).toBeInTheDocument();
    expect(paused.getByText('queues while a run is active')).toBeInTheDocument();
    expect((await row('nightly-s3')).getByText('needs review')).toBeInTheDocument();
  });

  it('says when no schedule runs the template', async () => {
    mockApi(pageRoutes());
    renderTemplatePage(SCHEDULES);
    expect(await screen.findByText('No schedule runs this template yet.')).toBeInTheDocument();
  });

  it('pauses and resumes a schedule from its menu', async () => {
    const user = userEvent.setup();
    const posted: string[] = [];
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/schedules': { schedules: [schedule('s1'), schedule('s2', { paused: true })] },
          'POST /api/v1/orgs/acme/schedules/s1/pause': (req: MockRequest) => (posted.push(req.path), { schedule: schedule('s1', { paused: true }) }),
          'POST /api/v1/orgs/acme/schedules/s2/resume': (req: MockRequest) => (posted.push(req.path), { schedule: schedule('s2') }),
        },
      }),
    );
    renderTemplatePage(SCHEDULES);
    expect(await openMenu(user, 'nightly-s1')).toEqual(['Pause', 'Edit', 'Delete', 'Open on the Schedules page']);
    await user.click(screen.getByRole('menuitem', { name: 'Pause' }));
    await openMenu(user, 'nightly-s2');
    await user.click(screen.getByRole('menuitem', { name: 'Resume' }));
    await waitFor(() => expect(posted).toEqual(['/api/v1/orgs/acme/schedules/s1/pause', '/api/v1/orgs/acme/schedules/s2/resume']));
  });

  it("opens a schedule's editor beside the list from its name and from Edit", async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes({ over: { 'GET /api/v1/orgs/acme/schedules': { schedules: [schedule('s1')] }, ...editorRoutes } }));
    renderTemplatePage(SCHEDULES);
    await user.click((await row('nightly-s1')).getByRole('button', { name: 'Open nightly-s1' }));
    expect(await editor().findByRole('heading', { name: /nightly-s1/ })).toBeInTheDocument();
    await user.click(editor().getByRole('button', { name: 'Close' }));
    expect(screen.queryByRole('region', { name: 'Schedule editor' })).not.toBeInTheDocument();
    await openMenu(user, 'nightly-s1');
    await user.click(screen.getByRole('menuitem', { name: 'Edit' }));
    expect(await editor().findByLabelText('Name')).toHaveValue('nightly-s1');
  });

  it('creates a schedule for this template and keeps it open on the page', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi(
      pageRoutes({
        over: {
          'POST /api/v1/orgs/acme/schedules': (req: MockRequest) => ((posted = req.body), { schedule: schedule('s1') }),
          ...editorRoutes,
        },
      }),
    );
    const { router } = renderTemplatePage(SCHEDULES);
    await user.click(await screen.findByRole('button', { name: 'New schedule' }));
    expect(editor().getByRole('heading', { name: 'New schedule' })).toBeInTheDocument();
    await user.type(editor().getByLabelText('Name'), 'weekly');
    await user.click(editor().getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toMatchObject({ template_id: 't1', name: 'weekly' }));
    expect(await editor().findByLabelText('Name')).toHaveValue('nightly-s1');
    expect(router.state.location.pathname).toBe('/o/acme/templates/t1');
    expect(router.state.location.search).toEqual({ tab: 'schedules' });
  });

  it('opens a schedule on the Schedules page', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes({ over: { 'GET /api/v1/orgs/acme/schedules': { schedules: [schedule('s1')] } } }));
    renderTemplatePage(SCHEDULES);
    await openMenu(user, 'nightly-s1');
    await user.click(screen.getByRole('menuitem', { name: 'Open on the Schedules page' }));
    expect(await screen.findByText(/SCHEDULES LIST/)).toHaveTextContent('{"id":"s1","template":"t1"}');
  });

  it('offers a viewer no change: no New schedule, only the way to the Schedules page', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes({ role: 'viewer', over: { 'GET /api/v1/orgs/acme/schedules': { schedules: [schedule('s1')] }, ...editorRoutes } }));
    renderTemplatePage(SCHEDULES);
    expect(await openMenu(user, 'nightly-s1')).toEqual(['Open on the Schedules page']);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('button', { name: 'New schedule' })).not.toBeInTheDocument();
    await user.click((await row('nightly-s1')).getByRole('button', { name: 'Open nightly-s1' }));
    expect(await editor().findByLabelText('Name')).toBeDisabled();
    expect(editor().queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
  });
});
