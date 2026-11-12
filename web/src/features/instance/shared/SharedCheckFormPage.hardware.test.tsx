import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedCheckFormPage } from './SharedCheckFormPage.tsx';

/** The shared check form's temperature, smart and raid plugins (backlog 3.9). */
const CASES = [
  { plugin: 'temperature', label: 'Sensors', param: 'sensors', good: 'a\nb' },
  { plugin: 'smart', label: 'Devices', param: 'devices', good: '/dev/sda\n/dev/nvme0' },
  { plugin: 'raid', label: 'Arrays', param: 'arrays', good: 'md0' },
] as const;

describe.each(CASES)('SharedCheckFormPage — $plugin plugin', ({ plugin, label, param, good }) => {
  async function openNew() {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
      'POST /api/v1/admin/checks/definitions': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { definition: { id: 'c9', ...(req.body as Record<string, unknown>) } };
      },
    });
    renderRoutes(
      [
        { path: '/settings/shared/checks', component: () => <div>the list</div> },
        { path: '/settings/shared/checks/new', component: SharedCheckFormPage },
      ],
      '/o/acme/settings/shared/checks/new',
    );
    await screen.findByLabelText('Key');
    await pickOption(screen, 'Plugin', { value: plugin });
    return posted;
  }

  it('sends {} for an empty list', async () => {
    const user = userEvent.setup();
    const posted = await openNew();
    expect(screen.queryByText(/Runs from the Pulsix server/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ plugin, params: {} });
  });

  it('sends the list', async () => {
    const user = userEvent.setup();
    const posted = await openNew();
    await user.type(screen.getByLabelText(new RegExp(`^${label}`)), good);
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ [param]: good.split('\n') });
  });

  it('refuses 65 entries and sends nothing', async () => {
    const user = userEvent.setup();
    const posted = await openNew();
    await user.click(screen.getByLabelText(new RegExp(`^${label}`)));
    await user.paste(Array.from({ length: 65 }, (_, i) => (plugin === 'smart' ? `/dev/sd${i}` : `n${i}`)).join('\n'));
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByText(/at most 64 entries/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });
});
