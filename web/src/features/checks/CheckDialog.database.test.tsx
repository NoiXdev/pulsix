import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, chooseFromMenu, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

/** The check dialog's postgres, mysql and redis plugins (backlog 3.10). */
const CASES = [
  { plugin: 'postgres', required: true, label: /^Secret/ },
  { plugin: 'mysql', required: true, label: /^Secret/ },
  { plugin: 'redis', required: false, label: /^Secret \(optional\)/ },
] as const;

describe.each(CASES)('CheckDialog — $plugin plugin', ({ plugin, required, label }) => {
  async function openNew(user: ReturnType<typeof userEvent.setup>) {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { check: check({ id: 'c9', plugin, origin: 'custom' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: plugin });
    return posted;
  }

  it('is selectable and shows the secret field with the agent.yaml hint', async () => {
    const user = userEvent.setup();
    await openNew(user);
    expect(screen.getByLabelText(label)).toBeInTheDocument();
    expect(screen.getByText(/Name of a secret in agent\.yaml on each host/)).toBeInTheDocument();
    expect(screen.getByText(/The value never leaves the host/)).toBeInTheDocument();
    expect(screen.queryByText(/Runs from the Pulsix server/)).not.toBeInTheDocument();
  });

  if (required) {
    it('refuses an empty secret and sends nothing', async () => {
      const user = userEvent.setup();
      const posted = await openNew(user);
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(await screen.findByText(/is required|must not be empty|required/i)).toBeInTheDocument();
      expect(screen.getByLabelText(label)).toHaveAttribute('aria-invalid', 'true');
      expect(posted).toHaveLength(0);
    });
  } else {
    it('accepts an empty secret and does not send it', async () => {
      const user = userEvent.setup();
      const posted = await openNew(user);
      await user.click(screen.getByRole('button', { name: 'Save' }));
      await waitFor(() => expect(posted).toHaveLength(1));
      expect(posted[0]).toMatchObject({ plugin });
      expect(posted[0].params).toEqual({});
    });
  }

  it('refuses an invalid secret name and sends nothing', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.type(screen.getByLabelText(label), 'bad name!');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/must be 1 to 64 characters: letters, digits and \. _ -/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });

  it('sends the secret', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.type(screen.getByLabelText(label), 'db-main');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toMatchObject({ secret: 'db-main' });
  });

  it('prefills a stored secret on edit and saves it unchanged', async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    const params = plugin === 'postgres' ? { secret: 'db-main', databases: ['app', 'analytics'] } : { secret: 'db-main' };
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c8', key: plugin, plugin, origin: 'custom', params })] },
      'PUT /api/v1/orgs/acme/checks/c8': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c8', key: plugin, plugin, origin: 'custom' }) };
      },
    });
    renderPage();
    await chooseFromMenu(user, `${plugin} (Organization)`, 'Edit');
    expect(await screen.findByLabelText(label)).toHaveValue('db-main');
    if (plugin === 'postgres') expect(screen.getByLabelText(/^Databases/)).toHaveValue('app\nanalytics');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.params).toEqual(params);
  });
});

describe('CheckDialog — postgres databases list', () => {
  async function openPostgres(user: ReturnType<typeof userEvent.setup>) {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { check: check({ id: 'c9', plugin: 'postgres', origin: 'custom' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: 'postgres' });
    await user.type(screen.getByLabelText(/^Secret/), 'db-main');
    return posted;
  }

  it('sends the entered lines as an array', async () => {
    const user = userEvent.setup();
    const posted = await openPostgres(user);
    await user.type(screen.getByLabelText(/^Databases/), 'app\nanalytics');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ secret: 'db-main', databases: ['app', 'analytics'] });
  });

  it('does not send an empty list', async () => {
    const user = userEvent.setup();
    const posted = await openPostgres(user);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ secret: 'db-main' });
  });
});
