import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, chooseFromMenu, org, renderPage } from './checksPageTestKit.tsx';

const GROUPS = {
  groups: [
    { id: '0198f3a0-0000-7000-8000-000000000001', name: 'Berlin', selector: 'site=ber', priority: 10, created_at: '', updated_at: '' },
    { id: '0198f3a0-0000-7000-8000-000000000002', name: 'Tokyo', selector: 'site=tyo', priority: 10, created_at: '', updated_at: '' },
  ],
};
const BERLIN = GROUPS.groups[0].id;
const TOKYO = GROUPS.groups[1].id;

/** The check dialog's vantage points (backlog 3.13a): where a synthetic
 * check is probed from, and how their results combine. */
describe('CheckDialog — vantage points', () => {
  async function openNew(user: ReturnType<typeof userEvent.setup>, plugin: string) {
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

  it('picks groups from the organization and shows their settings only once one is chosen', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'tcp');
    expect(screen.getByRole('checkbox', { name: 'Probe from the Pulsix server' })).toBeChecked();
    expect(screen.queryByLabelText('Agents per group')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('The check is down when')).not.toBeInTheDocument();
    // The groups are named, never typed as ids.
    expect(screen.queryByRole('textbox', { name: 'Probe from the agents of these groups' })).not.toBeInTheDocument();

    await pickOption(screen, 'Probe from the agents of these groups', 'Tokyo');
    await pickOption(screen, 'Probe from the agents of these groups', 'Berlin');
    expect(within(screen.getByRole('list', { name: 'Chosen groups' })).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Tokyo',
      'Berlin',
    ]);
    expect(screen.getByLabelText('Agents per group')).toHaveAttribute('placeholder', '3');
    await pickOption(screen, 'The check is down when', { value: 'majority' });
    await user.type(screen.getByLabelText('Port'), '443');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ port: 443, vantage_groups: [TOKYO, BERLIN], fail_policy: 'majority' });
  });

  it('sends the server switched off', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'icmp');
    await pickOption(screen, 'Probe from the agents of these groups', 'Berlin');
    await user.click(screen.getByRole('checkbox', { name: 'Probe from the Pulsix server' }));
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ vantage_manager: false, vantage_groups: [BERLIN] });
  });

  it('removing the last group hides its settings again', async () => {
    const user = userEvent.setup();
    await openNew(user, 'dns');
    await pickOption(screen, 'Probe from the agents of these groups', 'Berlin');
    expect(screen.getByLabelText('Agents per group')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Remove Berlin' }));
    expect(screen.queryByLabelText('Agents per group')).not.toBeInTheDocument();
  });

  it('keeps a check with a secret on the Pulsix server and says why', async () => {
    const user = userEvent.setup();
    await openNew(user, 'http');
    const groups = screen.getByRole('combobox', { name: 'Probe from the agents of these groups' });
    expect(groups).toBeEnabled();
    await user.type(screen.getByLabelText('Secret'), 'api-token');
    expect(groups).toBeDisabled();
    expect(screen.getByText('Checks with a secret are probed only from the Pulsix server.')).toBeInTheDocument();
  });

  it('leaves groups chosen before the secret removable', async () => {
    const user = userEvent.setup();
    await openNew(user, 'http');
    await pickOption(screen, 'Probe from the agents of these groups', 'Berlin');
    await user.type(screen.getByLabelText('Secret'), 'api-token');
    expect(screen.getByRole('combobox', { name: 'Probe from the agents of these groups' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Remove Berlin' }));
    expect(screen.queryByRole('list', { name: 'Chosen groups' })).not.toBeInTheDocument();
  });

  it("prefills a stored check's vantage points on edit and saves them unchanged", async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    const params = { port: 443, vantage_manager: false, vantage_groups: [BERLIN], agents_per_group: 2, fail_policy: 'all' };
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c8', key: 'tls', plugin: 'tls', origin: 'custom', params })] },
      'PUT /api/v1/orgs/acme/checks/c8': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c8', key: 'tls', plugin: 'tls', origin: 'custom' }) };
      },
    });
    renderPage();
    await chooseFromMenu(user, 'tls (Organization)', 'Edit');
    expect(await screen.findByRole('checkbox', { name: 'Probe from the Pulsix server' })).not.toBeChecked();
    expect(within(screen.getByRole('list', { name: 'Chosen groups' })).getByText('Berlin')).toBeInTheDocument();
    expect(screen.getByLabelText('Agents per group')).toHaveValue(2);
    expect(screen.getByRole('combobox', { name: 'The check is down when' })).toHaveValue('every probing point sees it down');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.params).toEqual(params);
  });

  it('offers no vantage points for a plugin the agent runs', async () => {
    const user = userEvent.setup();
    await openNew(user, 'disk');
    expect(screen.queryByText('Probe from the Pulsix server')).not.toBeInTheDocument();
    expect(screen.queryByText('Probe from the agents of these groups')).not.toBeInTheDocument();
  });
});
