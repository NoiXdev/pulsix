import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, chooseFromMenu, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

/** The check dialog's temperature, smart and raid plugins (backlog 3.9). */
const CASES = [
  { plugin: 'temperature', label: 'Sensors', param: 'sensors', good: 'coretemp_core_0\nnvme_composite', hint: /Sensor names as the host reports them/ },
  { plugin: 'smart', label: 'Devices', param: 'devices', good: '/dev/sda\n/dev/nvme0', hint: /Needs smartmontools on the host/ },
  { plugin: 'raid', label: 'Arrays', param: 'arrays', good: 'md0\nmd1', hint: /Linux software RAID only/ },
] as const;

describe.each(CASES)('CheckDialog — $plugin plugin', ({ plugin, label, param, good, hint }) => {
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

  it('is selectable, shows its list field and hint, and is not a server plugin', async () => {
    const user = userEvent.setup();
    await openNew(user);
    expect(screen.getByLabelText(new RegExp(`^${label}`))).toBeInTheDocument();
    expect(screen.getByText(hint)).toBeInTheDocument();
    expect(screen.queryByText(/Runs from the Pulsix server/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('combobox', { name: 'Plugin' }));
    expect(within(await screen.findByRole('option', { name: new RegExp(`^${plugin === 'raid' ? 'RAID' : plugin === 'smart' ? 'SMART' : 'Temperature'}`) }))
      .queryByText('runs from the Pulsix server')).not.toBeInTheDocument();
  });

  it('sends {} for an empty list', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ plugin, params: {} });
  });

  it('sends the list', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.type(screen.getByLabelText(new RegExp(`^${label}`)), good);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ [param]: good.split('\n') });
  });

  it('refuses an over-long entry and sends nothing', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    const long = plugin === 'smart' ? `/dev/${'a'.repeat(251)}` : 'x'.repeat(256);
    await user.click(screen.getByLabelText(new RegExp(`^${label}`)));
    await user.paste(long);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/at most 255 bytes/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });

  it('prefills a stored list on edit and saves it unchanged', async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    const params = { [param]: good.split('\n') };
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
    expect(await screen.findByLabelText(new RegExp(`^${label}`))).toHaveValue(good);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.params).toEqual(params);
  });
});

describe('CheckDialog — smart device paths', () => {
  it('refuses a bare device name', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted.push(req.body);
        return { check: check({ id: 'c9', plugin: 'smart', origin: 'custom' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: 'smart' });
    await user.type(screen.getByLabelText(/^Devices/), 'sda');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/must start with \/dev\//)).toBeInTheDocument();
    expect(screen.getByLabelText(/^Devices/)).toHaveAttribute('aria-invalid', 'true');
    expect(posted).toHaveLength(0);
  });
});
