import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { group, routes, renderAt } from './groupPageTestKit.tsx';

const SCHEMA = {
  fields: [
    { name: 'env', label: 'Environment', type: 'string', required: true },
    { name: 'level', type: 'enum', options: ['info', 'debug'], default: 'info' },
    { name: 'ntp', type: 'string' },
    { name: 'region', type: 'string' },
    { name: 'domain', type: 'string', scope: 'host' },
  ],
};

const preview = (inventory: Record<string, unknown>) => ({
  'GET /api/v1/orgs/acme/inventory/preview': { inventory, connections: {}, warnings: [] },
});

const row = (name: string) => within(screen.getByTestId(`variable-${name}`));

describe('GroupDetailPage variables', () => {
  it('shows one row per group field: key, type, input and where the value comes from', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod' } }),
        ...preview({ all: { children: ['prod'] }, prod: { vars: { env: 'staging', ntp: 'ntp.example.test' }, children: ['web'] }, web: { vars: { env: 'prod' } } }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    expect(await screen.findByLabelText('Environment')).toHaveValue('prod');

    // Only the group's own fields; the host-scoped one is the members'.
    expect(screen.queryByTestId('variable-domain')).not.toBeInTheDocument();
    expect(row('env').getByText('env')).toHaveClass('font-mono');
    expect(row('env').getByText('string')).toBeInTheDocument();
    expect(row('env').getByText('required')).toBeInTheDocument();
    expect(row('level').getByText('enum')).toBeInTheDocument();

    await waitFor(() => expect(row('env').getByText('overrides prod (staging)')).toBeInTheDocument());
    expect(row('level').getByText('schema default info')).toBeInTheDocument();
    expect(row('ntp').getByText('inherits ntp.example.test from prod')).toBeInTheDocument();
    // Set nowhere: nothing to say.
    expect(screen.getByTestId('variable-region').querySelectorAll('p')).toHaveLength(0);
  });

  it('follows the draft: a value typed over an inherited one overrides it', async () => {
    const user = userEvent.setup();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod' } }),
        ...preview({ prod: { vars: { ntp: 'ntp.example.test' } } }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    await waitFor(() => expect(row('ntp').getByText('inherits ntp.example.test from prod')).toBeInTheDocument());
    await user.type(screen.getByLabelText('ntp'), 'ntp.local');
    expect(row('ntp').getByText('overrides prod (ntp.example.test)')).toBeInTheDocument();
  });

  it('says nothing it cannot know: an ancestor missing from the preview', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod' } }),
        ...preview({ all: {} }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    await waitFor(() => expect(row('level').getByText('schema default info')).toBeInTheDocument());
    expect(screen.getByTestId('variable-env').querySelectorAll('p')).toHaveLength(0);
    expect(screen.getByTestId('variable-ntp').querySelectorAll('p')).toHaveLength(0);
  });

  it('keeps Save off until an edit, and Discard takes the edit back', async () => {
    const user = userEvent.setup();
    mockApi(routes('admin', { 'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod' } }) }));
    renderAt('/o/acme/groups/g1');
    const env = await screen.findByLabelText('Environment');
    const save = screen.getByRole('button', { name: 'Save values' });
    expect(save).toBeDisabled();
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument();

    await user.type(env, '-eu');
    expect(save).toBeEnabled();
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Discard' }));
    expect(await screen.findByLabelText('Environment')).toHaveValue('prod');
    expect(screen.getByRole('button', { name: 'Save values' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument();
  });
});
