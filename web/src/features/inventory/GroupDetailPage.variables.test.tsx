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

  it('marks the values and defaults an assistant wrote as plain text until a person changes or accepts them', async () => {
    const user = userEvent.setup();
    const accepts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({
          schema: SCHEMA, vars: { env: 'prod', ntp: 'ntp.example.test' }, assistant_keys: ['ntp'], assistant_default_keys: ['level', 'env'],
        }),
        ...preview({ web: { vars: { env: 'prod' } } }),
        'POST /api/v1/orgs/acme/inventory/groups/g1/vars/accept': (req: MockRequest) => {
          accepts.push(req);
          return group({ schema: SCHEMA, revision: 4, assistant_default_keys: ['level', 'env'] });
        },
        'POST /api/v1/orgs/acme/inventory/groups/g1/schema/accept': (req: MockRequest) => {
          accepts.push(req);
          return group({ schema: SCHEMA, revision: 5 });
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('Environment');
    expect(await screen.findByTestId('assistant-ntp')).toHaveTextContent('Written by an assistant — used as plain text until a person changes or accepts it');
    expect(screen.getByTestId('assistant-level')).toHaveTextContent('Default set by an assistant — used as plain text until a person changes or accepts it');
    // env holds a person's value, so its (assistant's) default does not apply.
    expect(screen.queryByTestId('assistant-env')).not.toBeInTheDocument();
    expect(screen.queryByTestId('assistant-region')).not.toBeInTheDocument();

    await user.click(within(screen.getByTestId('assistant-ntp')).getByRole('button', { name: 'Accept ntp as live' }));
    await screen.findByText('ntp is live now');
    expect(accepts[0].path).toContain('/vars/accept');
    expect(accepts[0].headers.get('If-Match')).toBe('"3"');
    expect(accepts[0].body).toEqual({ keys: ['ntp'] });
    await user.click(within(screen.getByTestId('assistant-level')).getByRole('button', { name: 'Accept level as live' }));
    await screen.findByText('level is live now');
    expect(accepts[1].path).toContain('/schema/accept');
    expect(accepts[1].body).toEqual({ keys: ['level'] });
  });

  it('offers no accept while the value on screen is not the stored one (final review M3)', async () => {
    const user = userEvent.setup();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod', ntp: 'x' }, assistant_keys: ['ntp'] }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    const accept = await screen.findByRole('button', { name: 'Accept ntp as live' });
    expect(accept).toBeEnabled();
    await user.type(screen.getByLabelText('ntp'), 'y');
    expect(accept).toBeDisabled();
    expect(within(screen.getByTestId('assistant-ntp')).getByText('Save or discard your edit before accepting the stored value')).toBeInTheDocument();
    await user.clear(screen.getByLabelText('ntp'));
    await user.type(screen.getByLabelText('ntp'), 'x');
    expect(accept).toBeEnabled();
  });

  it('offers no accept to a reader', async () => {
    mockApi(
      routes('viewer', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod', ntp: 'x' }, assistant_keys: ['ntp'] }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    expect(await screen.findByTestId('assistant-ntp')).toBeInTheDocument();
    expect(within(screen.getByTestId('assistant-ntp')).queryByRole('button')).not.toBeInTheDocument();
  });

  it('quotes an inherited plain-text value unwrapped, saying it is plain text', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod' } }),
        ...preview({ prod: { vars: { ntp: { __ansible_unsafe: '{{ x }}' } } } }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    await waitFor(() => expect(row('ntp').getByText('inherits {{ x }} (plain text) from prod')).toBeInTheDocument());
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
