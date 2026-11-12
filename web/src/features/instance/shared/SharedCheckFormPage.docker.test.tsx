import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedCheckFormPage } from './SharedCheckFormPage.tsx';

/** The shared check form's docker plugin (backlog 3.12). */
describe('SharedCheckFormPage — docker plugin', () => {
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
    await pickOption(screen, 'Plugin', { value: 'docker' });
    return posted;
  }

  it('offers docker with its fields and sends no params untouched', async () => {
    const user = userEvent.setup();
    const posted = await openNew();
    expect(screen.getByLabelText('Docker socket')).toBeInTheDocument();
    expect(screen.queryByText(/Runs from the Pulsix server/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ plugin: 'docker', params: {} });
  });

  it('sends the chosen selection, label and socket', async () => {
    const user = userEvent.setup();
    const posted = await openNew();
    await pickOption(screen, 'Containers', { value: 'labeled' });
    await user.type(screen.getByLabelText('Label'), 'watch=yes');
    await user.type(screen.getByLabelText('Docker socket'), 'npipe:////./pipe/docker_engine');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ selection: 'labeled', label: 'watch=yes', socket: 'npipe:////./pipe/docker_engine' });
  });

  it('refuses a bad socket', async () => {
    const user = userEvent.setup();
    const posted = await openNew();
    await user.type(screen.getByLabelText('Docker socket'), 'docker.sock');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByText(/must be an absolute path/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });
});
