import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { group, routes, renderAt } from './groupPageTestKit.tsx';

const stale = () => new Response(JSON.stringify({ status: 412, detail: 'changed' }), { status: 412, headers: { 'Content-Type': 'application/json' } });

type User = ReturnType<typeof userEvent.setup>;

/** The number of fields in the schema editor (its rows past the header;
 * no field is expanded in these tests, so one row is one field). */
const fieldCount = () => within(screen.getByRole('table', { name: 'Fields' })).getAllByRole('row').length - 1;

/** addField adds a new field to the schema draft through its dialog. */
async function addField(user: User) {
  await user.click(screen.getByRole('button', { name: 'Add field' }));
  const dialog = await screen.findByRole('dialog', { name: /^Field / });
  await user.click(within(dialog).getByRole('button', { name: 'Done' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
}

async function typeEnv(user: User, text: string) {
  const env = await screen.findByLabelText('env');
  await user.clear(env);
  await user.type(env, text);
}

afterEach(() => {
  vi.restoreAllMocks();
});

/** The sections are no longer behind tabs: a save, a reload or a rename in
 * one place must not throw away an unsaved draft in another, and a draft
 * must never follow the user to another group. */
describe('GroupDetailPage drafts', () => {
  it('keeps an unsaved schema draft when the group values are saved', async () => {
    const user = userEvent.setup();
    let current = group();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          current = group({ revision: 4, description: 'Refetched', vars: (req.body as { vars: Record<string, unknown> }).vars });
          return current;
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByRole('table', { name: 'Fields' });
    const before = fieldCount();
    await addField(user);
    expect(fieldCount()).toBe(before + 1);

    await typeEnv(user, 'staging');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    // The refetched group (revision 4) is on the page ...
    expect(await screen.findByText('Refetched')).toBeInTheDocument();
    // ... and has left the unsaved schema draft alone.
    expect(fieldCount()).toBe(before + 1);
    expect(screen.getByLabelText('env')).toHaveValue('staging');
  });

  it('keeps unsaved values when the group is renamed', async () => {
    const user = userEvent.setup();
    let current = group();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          current = group({ revision: 4, name: (req.body as { name: string }).name });
          return current;
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'typed but not saved');

    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });
    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'web_fe' })).toBeInTheDocument();
    expect(screen.getByLabelText('env')).toHaveValue('typed but not saved');
  });

  it('keeps the schema draft through a refused values save and its reload', async () => {
    const user = userEvent.setup();
    let current = group();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': () => {
          // Someone else changed the group first, not its values.
          current = group({ revision: 4, description: 'Changed elsewhere' });
          return stale();
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByRole('table', { name: 'Fields' });
    const before = fieldCount();
    await addField(user);
    await typeEnv(user, 'mine');
    await user.click(screen.getByRole('button', { name: 'Save values' }));

    const values = screen.getByRole('region', { name: 'Group values' });
    await user.click(await within(values).findByRole('button', { name: 'Reload' }));
    expect(await screen.findByText('Changed elsewhere')).toBeInTheDocument();
    await waitFor(() => expect(within(values).queryByRole('alert')).not.toBeInTheDocument());
    expect(screen.getByLabelText('env')).toHaveValue('mine');
    expect(fieldCount()).toBe(before + 1);
  });

  it('keeps the values draft through a refused schema save and its reload', async () => {
    const user = userEvent.setup();
    let current = group();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': () => {
          current = group({ revision: 4, description: 'Changed elsewhere' });
          return stale();
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'mine');
    await user.click(screen.getByRole('button', { name: 'Save schema' }));

    const schema = screen.getByRole('region', { name: 'Variable schema' });
    await user.click(await within(schema).findByRole('button', { name: 'Reload' }));
    expect(await screen.findByText('Changed elsewhere')).toBeInTheDocument();
    await waitFor(() => expect(within(schema).queryByRole('alert')).not.toBeInTheDocument());
    expect(screen.getByLabelText('env')).toHaveValue('mine');
  });

  it('says so when the reload itself fails, and keeps the notice', async () => {
    const user = userEvent.setup();
    let failing = false;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () =>
          failing ? new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } }) : group(),
        'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': () => {
          failing = true;
          return stale();
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'Save schema' }));
    const schema = screen.getByRole('region', { name: 'Variable schema' });
    await user.click(await within(schema).findByRole('button', { name: 'Reload' }));
    expect(await within(schema).findByText('Reloading failed. Try again.')).toBeInTheDocument();
    expect(within(schema).getByRole('button', { name: 'Reload' })).toBeEnabled();
  });

  it('reloads a refused group edit into the dialog and saves it on the new revision', async () => {
    const user = userEvent.setup();
    let current = group();
    const patches: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          patches.push(req);
          if (patches.length === 1) {
            current = group({ revision: 4, description: 'Set by someone else' });
            return stale();
          }
          return group({ revision: 5 });
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });
    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));

    await user.click(await within(dialog).findByRole('button', { name: 'Reload' }));
    // Reload refetches; it submits nothing.
    await waitFor(() => expect(within(dialog).getByLabelText('Description')).toHaveValue('Set by someone else'));
    expect(patches).toHaveLength(1);
    expect(within(dialog).getByLabelText('Name')).toHaveValue('web');
    expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await screen.findByText('Group saved');
    expect(patches).toHaveLength(2);
    expect(patches[1].headers.get('If-Match')).toBe('"4"');
    expect(patches[1].body).toMatchObject({ name: 'web', description: 'Set by someone else' });
  });

  it('does not carry a draft to another group with the same schema and values', async () => {
    const user = userEvent.setup();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/c1': group({ id: 'c1', name: 'web_eu', parent_id: 'g1', description: '' }),
        'GET /api/v1/orgs/acme/inventory/groups/c1/members': { members: [] },
      }),
    );
    // web_eu is visited first, so it is cached and shows at once later.
    renderAt('/o/acme/groups/c1');
    expect(await screen.findByRole('heading', { level: 1, name: 'web_eu' })).toBeInTheDocument();
    await user.click(await screen.findByRole('link', { name: 'web' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'web' })).toBeInTheDocument();
    await screen.findByRole('table', { name: 'Fields' });
    const before = fieldCount();
    await addField(user);
    await typeEnv(user, 'only for web');

    await user.click(screen.getByRole('link', { name: 'web_eu' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'web_eu' })).toBeInTheDocument();
    expect(screen.getByLabelText('env')).toHaveValue('prod');
    expect(fieldCount()).toBe(before);
  });
});
