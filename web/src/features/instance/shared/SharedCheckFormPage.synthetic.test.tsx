import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedCheckFormPage } from './SharedCheckFormPage.tsx';

const LIST = '/o/acme/settings/shared/checks';

const checkDef = (over: Record<string, unknown> = {}) => ({
  id: 'c1', key: 'disk-data', plugin: 'disk', params: {}, interval_seconds: 300, ...over,
});

function renderPage(path: string) {
  return renderRoutes(
    [
      { path: '/settings/shared/checks', component: () => <div>the list</div> },
      { path: '/settings/shared/checks/new', component: SharedCheckFormPage },
      { path: '/settings/shared/checks/$id', component: SharedCheckFormPage },
    ],
    path,
  );
}

afterEach(() => vi.restoreAllMocks());

/** The shared check form's synthetic plugins (backlog 3.13), the same
 * fields and rules as the organization's own check dialog. */
describe('SharedCheckFormPage — synthetic plugins', () => {
  async function openNew(plugin?: string) {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
      'POST /api/v1/admin/checks/definitions': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { definition: checkDef({ id: 'c9', ...(req.body as Record<string, unknown>) }) };
      },
    });
    renderPage(`${LIST}/new`);
    await screen.findByLabelText('Key');
    if (plugin) await pickOption(screen, 'Plugin', { value: plugin });
    return posted;
  }

  it('offers the five plugins, each marked as running from the Pulsix server', async () => {
    const user = userEvent.setup();
    await openNew();
    await user.click(screen.getByRole('combobox', { name: 'Plugin' }));
    for (const label of ['Ping (ICMP)', 'TCP port', 'HTTP(S)', 'TLS certificate', 'DNS']) {
      const option = await screen.findByRole('option', { name: new RegExp(`^${label.replace(/[()]/g, '\\$&')}`) });
      expect(within(option).getByText('runs from the Pulsix server')).toBeInTheDocument();
    }
  });

  it('prefills the key with the plugin name and says rules apply to that key', async () => {
    await openNew('tls');
    expect(screen.getByLabelText('Key')).toHaveValue('tls');
    expect(screen.getByText(/Runs from the Pulsix server, not on the host/)).toBeInTheDocument();
    expect(screen.getByText('Alert rules on tls.* metrics apply to every check of this type, whatever its key.')).toBeInTheDocument();
  });

  it('sends only the http fields the user set', async () => {
    const user = userEvent.setup();
    const posted = await openNew('http');
    await user.type(screen.getByLabelText('URL'), 'https://example.org/health');
    await user.type(screen.getByLabelText('Body contains'), 'ok');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({
      key: 'http', plugin: 'http', interval_seconds: 60, params: { url: 'https://example.org/health', body_contains: 'ok' },
    });
  });

  it('does not submit tcp without a port and says why at the field', async () => {
    const user = userEvent.setup();
    const posted = await openNew('tcp');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByText('required')).toBeInTheDocument();
    expect(screen.getByLabelText('Port')).toHaveAttribute('aria-invalid', 'true');
    expect(posted).toHaveLength(0);
  });

  it('sends the dns record type the user picked', async () => {
    const user = userEvent.setup();
    const posted = await openNew('dns');
    await pickOption(screen, 'Record type', { value: 'AAAA' });
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ type: 'AAAA' });
  });

  it("prefills an existing http definition's fields on edit and saves them unchanged", async () => {
    const user = userEvent.setup();
    let put: unknown;
    const params = { url: 'https://{address}/', verify_tls: false, body_contains: 'ok' };
    const http = checkDef({ key: 'http', plugin: 'http', params, interval_seconds: 60 });
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [http] },
      'PUT /api/v1/admin/checks/definitions/c1': (req: MockRequest) => {
        put = req.body;
        return { definition: http };
      },
    });
    renderPage(`${LIST}/c1`);
    expect(await screen.findByLabelText('URL')).toHaveValue('https://{address}/');
    expect(screen.getByLabelText('Body contains')).toHaveValue('ok');
    expect(screen.getByRole('switch', { name: 'Verify the TLS certificate' })).toHaveAttribute('aria-checked', 'false');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put).toEqual({ key: 'http', plugin: 'http', interval_seconds: 60, params });
  });
});
