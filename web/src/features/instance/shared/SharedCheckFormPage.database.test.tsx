import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedCheckFormPage } from './SharedCheckFormPage.tsx';

/** The shared check form's postgres, mysql and redis plugins (backlog 3.10). */
const CASES = [
  { plugin: 'postgres', required: true, label: /^Secret/ },
  { plugin: 'mysql', required: true, label: /^Secret/ },
  { plugin: 'redis', required: false, label: /^Secret \(optional\)/ },
] as const;

describe.each(CASES)('SharedCheckFormPage — $plugin plugin', ({ plugin, required, label }) => {
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

  it('is selectable and shows the secret field with the agent.yaml hint', async () => {
    await openNew();
    expect(screen.getByLabelText(label)).toBeInTheDocument();
    expect(screen.getByText(/Name of a secret in agent\.yaml on each host/)).toBeInTheDocument();
  });

  if (required) {
    it('refuses an empty secret and sends nothing', async () => {
      const user = userEvent.setup();
      const posted = await openNew();
      await user.click(screen.getByRole('button', { name: 'Create definition' }));
      await waitFor(() => expect(screen.getByLabelText(label)).toHaveAttribute('aria-invalid', 'true'));
      expect(posted).toHaveLength(0);
    });
  } else {
    it('accepts an empty secret and does not send it', async () => {
      const user = userEvent.setup();
      const posted = await openNew();
      await user.click(screen.getByRole('button', { name: 'Create definition' }));
      await waitFor(() => expect(posted).toHaveLength(1));
      expect(posted[0]).toMatchObject({ plugin, params: {} });
    });
  }

  it('sends the secret', async () => {
    const user = userEvent.setup();
    const posted = await openNew();
    await user.type(screen.getByLabelText(label), 'db-main');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toMatchObject({ secret: 'db-main' });
  });
});
