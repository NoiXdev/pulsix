import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { CHECK_PLUGINS } from '../../test/checkPlugins.ts';
import { en } from '../../i18n/en/index.ts';
import { check, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

/** The check dialog builds its plugin list and fields from GET
 * /check-plugins (backlog 3.54). */
describe('CheckDialog — from the plugin descriptors', () => {
  function mock(extra: Record<string, unknown> = {}) {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { check: check({ id: 'c9', origin: 'custom' }) };
      },
      ...extra,
    });
    return posted;
  }

  it("offers the plugins in the descriptors' order", async () => {
    const user = userEvent.setup();
    mock();
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await user.click(await screen.findByRole('combobox', { name: 'Plugin' }));
    const offered = (await screen.findAllByRole('option')).map((o) => o.getAttribute('data-label'));
    expect(offered).toEqual(CHECK_PLUGINS.map((p) => en.checks.dialog.plugins[p.name]));
  });

  it('says so when the descriptors cannot be read, and offers no form', async () => {
    const user = userEvent.setup();
    mock({ 'GET /api/v1/check-plugins': () => new Response(JSON.stringify({ detail: 'boom' }), { status: 500 }) });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load the check plugins.');
    expect(screen.queryByRole('combobox', { name: 'Plugin' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it("does not carry one plugin's values into another", async () => {
    const user = userEvent.setup();
    const posted = mock();
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: 'tls' });
    await user.type(screen.getByLabelText('Port'), '8443');
    await pickOption(screen, 'Plugin', { value: 'tcp' });
    expect(screen.getByLabelText('Port')).toHaveValue(null);
    await pickOption(screen, 'Plugin', { value: 'tls' });
    expect(screen.getByLabelText('Port')).toHaveValue(null);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({});
  });

  it('renders and sends a parameter the descriptors add, labelled by its name until the catalogue has it', async () => {
    const user = userEvent.setup();
    const plugins = CHECK_PLUGINS.map((p) =>
      p.name === 'uptime' ? { ...p, params: [{ name: 'grace_s', type: 'integer', min: 1, max: 9 }] } : p,
    );
    const posted = mock({ 'GET /api/v1/check-plugins': { plugins } });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: 'uptime' });
    await user.type(screen.getByLabelText('grace_s'), '12');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('must be a whole number between 1 and 9')).toBeInTheDocument();
    await user.clear(screen.getByLabelText('grace_s'));
    await user.type(screen.getByLabelText('grace_s'), '4');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ plugin: 'uptime', params: { grace_s: 4 } });
  });

  it('refuses a service check without units at the field', async () => {
    const user = userEvent.setup();
    const posted = mock();
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: 'service' });
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('required')).toBeInTheDocument();
    expect(screen.getByLabelText('Units (one per line)')).toHaveAttribute('aria-invalid', 'true');
    expect(posted).toHaveLength(0);
  });
});
