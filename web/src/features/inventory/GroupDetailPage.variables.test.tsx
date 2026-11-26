import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
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
    // The type as the fields dialog names it.
    expect(row('env').getByText('Text')).toBeInTheDocument();
    expect(row('env').getByText('required')).toBeInTheDocument();
    expect(row('level').getByText('Choice')).toBeInTheDocument();

    await waitFor(() => expect(row('env').getByText('overrides prod (staging)')).toBeInTheDocument());
    expect(row('level').getByText('schema default info')).toBeInTheDocument();
    expect(row('ntp').getByText('inherits ntp.example.test from prod')).toBeInTheDocument();
    // Set nowhere: nothing to say.
    expect(screen.getByTestId('variable-region').querySelectorAll('p')).toHaveLength(0);
  });

  it('marks the values and defaults an assistant wrote as plain text until a person saves them', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({
          schema: SCHEMA, vars: { env: 'prod', ntp: 'ntp.example.test' }, assistant_keys: ['ntp'], assistant_default_keys: ['level', 'env'],
        }),
        ...preview({ web: { vars: { env: 'prod' } } }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('Environment');
    expect(await screen.findByTestId('assistant-ntp')).toHaveTextContent('Written by an assistant — used as plain text until a person saves it');
    expect(screen.getByTestId('assistant-level')).toHaveTextContent('Default set by an assistant — used as plain text until a person saves the fields');
    // env holds a person's value, so its (assistant's) default does not apply.
    expect(screen.queryByTestId('assistant-env')).not.toBeInTheDocument();
    expect(screen.queryByTestId('assistant-region')).not.toBeInTheDocument();
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

  it('reads the ancestors from the organization preview when the scoped one lacks them', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod' } }),
        // No managed host below web: its own preview renders nothing above it.
        'GET /api/v1/orgs/acme/inventory/preview': (req: MockRequest) => ({
          inventory: req.search.get('group') ? { all: {} } : { all: { children: ['prod'] }, prod: { vars: { ntp: 'ntp.example.test' }, children: ['web'] }, web: {} },
          connections: {},
          warnings: [],
        }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    await waitFor(() => expect(row('ntp').getByText('inherits ntp.example.test from prod')).toBeInTheDocument());
  });

  it('lets the orphaned values be dropped without an edit, as the notice says', async () => {
    const user = userEvent.setup();
    let seen: MockRequest | undefined;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod', gone: 1 }, orphaned: ['gone'] }),
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          seen = req;
          return group({ schema: SCHEMA, vars: { env: 'prod' }, revision: 4 });
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    expect(await screen.findByText(/Saving this form drops them/)).toBeInTheDocument();
    const save = screen.getByRole('button', { name: 'Save values' });
    expect(save).toBeEnabled();
    await user.click(save);
    await screen.findByText('Group values saved');
    expect(seen?.body).toEqual({ vars: { env: 'prod' } });
  });
});
