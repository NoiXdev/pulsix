import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, chooseFromMenu, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

/** The check dialog's docker plugin (backlog 3.12): run by the host's
 * agent, with a selection, a label and a socket. */
describe('CheckDialog — docker plugin', () => {
  async function openNew(user: ReturnType<typeof userEvent.setup>) {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { check: check({ id: 'c9', plugin: 'docker', origin: 'custom' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: 'docker' });
    return posted;
  }

  it('shows the three fields, without the runs-from-the-server marker', async () => {
    const user = userEvent.setup();
    await openNew(user);
    expect(screen.getByRole('combobox', { name: 'Containers' })).toHaveValue('All containers');
    expect(screen.getByLabelText('Label')).toHaveAttribute('placeholder', 'pulsix.monitor=true');
    expect(screen.getByLabelText('Docker socket')).toHaveAttribute('placeholder', '/var/run/docker.sock');
    expect(screen.getByText(/left out/)).toBeInTheDocument();
    expect(screen.getByText(/npipe:\/\/\/\/\.\/pipe\/docker_engine/)).toBeInTheDocument();
    expect(screen.queryByText(/Runs from the Pulsix server/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Alert rules on/)).not.toBeInTheDocument();
    expect(screen.getByLabelText('Key')).toHaveValue('');
    await user.click(screen.getByRole('combobox', { name: 'Plugin' }));
    expect(within(await screen.findByRole('option', { name: /^Docker containers/ })).queryByText('runs from the Pulsix server')).not.toBeInTheDocument();
  });

  it('sends no params when nothing was touched', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ plugin: 'docker' });
    expect(posted[0].params).toEqual({});
  });

  it('sends the selection once "Only labeled containers" is chosen', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await pickOption(screen, 'Containers', { value: 'labeled' });
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ selection: 'labeled' });
  });

  it('refuses a bad label and a bad socket at the fields and sends nothing', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.type(screen.getByLabelText('Label'), 'bad key');
    await user.type(screen.getByLabelText('Docker socket'), 'docker.sock');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/must be a label key/)).toBeInTheDocument();
    expect(screen.getByText(/must be an absolute path/)).toBeInTheDocument();
    expect(screen.getByLabelText('Label')).toHaveAttribute('aria-invalid', 'true');
    expect(posted).toHaveLength(0);
  });

  it("prefills a stored docker check's fields on edit and saves them unchanged", async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    const params = { selection: 'labeled', label: 'watch', socket: '/x.sock' };
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c8', key: 'docker', plugin: 'docker', origin: 'custom', params })] },
      'PUT /api/v1/orgs/acme/checks/c8': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c8', key: 'docker', plugin: 'docker', origin: 'custom' }) };
      },
    });
    renderPage();
    await chooseFromMenu(user, 'docker (Organization)', 'Edit');
    expect(await screen.findByLabelText('Label')).toHaveValue('watch');
    expect(screen.getByRole('combobox', { name: 'Containers' })).toHaveValue('Only labeled containers');
    expect(screen.getByLabelText('Docker socket')).toHaveValue('/x.sock');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.params).toEqual(params);
  });
});
