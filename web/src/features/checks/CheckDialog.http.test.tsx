import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

/** The check dialog's http plugin with JSON values and credentials
 * (backlog 3.6h): each json_values line reads one value out of a JSON
 * response; the optional secret lives in the organization's secret store. */
describe('CheckDialog — http JSON values and credentials', () => {
  async function openNew(user: ReturnType<typeof userEvent.setup>) {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { check: check({ id: 'c9', key: 'http', plugin: 'http', origin: 'custom' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: 'http' });
    return posted;
  }

  it('shows the JSON values and the optional secret, pointing to Settings → Secrets', async () => {
    const user = userEvent.setup();
    await openNew(user);
    expect(screen.getByLabelText('JSON values (one per line, name=JSONPath)')).toHaveAttribute('placeholder', 'status=$.status');
    expect(screen.getByLabelText('Secret')).toHaveAttribute('placeholder', 'api-token');
    expect(screen.getByText(/the organization’s secrets \(Settings → Secrets\)/)).toBeInTheDocument();
    expect(screen.queryByText(/agent\.yaml/)).not.toBeInTheDocument();
  });

  it('refuses a JSON value line without a name or a $ path, and sends nothing', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.type(screen.getByLabelText('URL'), 'https://{{address}/health');
    await user.type(screen.getByLabelText('JSON values (one per line, name=JSONPath)'), 'status=.status');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/every entry must be name=JSONPath/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });

  it('sends the secret and the JSON values', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.type(screen.getByLabelText('URL'), 'https://{{address}/health');
    await user.type(screen.getByLabelText('Secret'), 'api-token');
    await user.type(
      screen.getByLabelText('JSON values (one per line, name=JSONPath)'),
      'status=$.status{Enter}depth=$.queues[[0].depth',
    );
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ plugin: 'http', key: 'http' });
    expect(posted[0].params).toEqual({
      url: 'https://{address}/health',
      secret: 'api-token',
      json_values: ['status=$.status', 'depth=$.queues[0].depth'],
    });
  });
});
