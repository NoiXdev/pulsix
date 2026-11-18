import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';
import { addField, closeFields, fieldCount, fieldsDialog, group, openEdit, openFields, openTab, routes, renderAt, saveFields, stale, typeEnv } from './groupPageTestKit.tsx';

afterEach(() => {
  vi.restoreAllMocks();
});

/** A save, a reload or a rename in one place must not throw away an unsaved
 * draft in another, closing the fields dialog asks before it discards the
 * schema draft, and a draft must never follow the user to another group. */
describe('GroupDetailPage drafts', () => {
  it('asks before closing the fields with an edited draft, and Discard throws it away', async () => {
    const user = userEvent.setup();
    mockApi(routes('admin'));
    renderAt('/o/acme/groups/g1');
    await openFields(user);
    const before = fieldCount();
    await addField(user);
    expect(within(fieldsDialog()).getByText('Unsaved changes')).toBeInTheDocument();

    // Declined, the dialog and its draft stay.
    await user.click(within(fieldsDialog()).getByRole('button', { name: 'Cancel' }));
    const ask = await screen.findByRole('dialog', { name: 'Discard changes?' });
    await user.click(within(ask).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Discard changes?' })).not.toBeInTheDocument());
    expect(fieldCount()).toBe(before + 1);

    // Escape asks as well; Discard closes and drops the draft.
    within(fieldsDialog()).getByRole('button', { name: 'Add field' }).focus();
    await user.keyboard('{Escape}');
    const again = await screen.findByRole('dialog', { name: 'Discard changes?' });
    await user.click(within(again).getByRole('button', { name: 'Discard' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Manage fields' })).not.toBeInTheDocument());
    await openFields(user);
    expect(fieldCount()).toBe(before);
    expect(within(fieldsDialog()).queryByText('Unsaved changes')).not.toBeInTheDocument();
  });

  it('asks on Escape with an edited draft wherever the focus is', async () => {
    const user = userEvent.setup();
    mockApi(routes('admin'));
    renderAt('/o/acme/groups/g1');
    await openFields(user);
    await addField(user);
    (document.activeElement as HTMLElement | null)?.blur();
    await user.keyboard('{Escape}');
    const ask = await screen.findByRole('dialog', { name: 'Discard changes?' });
    await user.click(within(ask).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Discard changes?' })).not.toBeInTheDocument());
    expect(fieldsDialog()).toBeInTheDocument();
    expect(within(fieldsDialog()).getByText('Unsaved changes')).toBeInTheDocument();
  });

  it('closes unedited fields at once, on Cancel and on Escape', async () => {
    const user = userEvent.setup();
    mockApi(routes('admin'));
    renderAt('/o/acme/groups/g1');
    await openFields(user);
    expect(within(fieldsDialog()).getByRole('button', { name: 'Save' })).toBeDisabled();
    await user.click(within(fieldsDialog()).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Manage fields' })).not.toBeInTheDocument());
    expect(screen.queryByRole('dialog', { name: 'Discard changes?' })).not.toBeInTheDocument();
    await openFields(user);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Manage fields' })).not.toBeInTheDocument());
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

    const dialog = await openEdit(user);
    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'web_fe' })).toBeInTheDocument();
    expect(screen.getByLabelText('env')).toHaveValue('typed but not saved');
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
    await openFields(user);
    await addField(user);
    await saveFields(user);

    const schema = fieldsDialog();
    await user.click(await within(schema).findByRole('button', { name: 'Reload' }));
    expect(await screen.findByText('Changed elsewhere')).toBeInTheDocument();
    await waitFor(() => expect(within(schema).queryByRole('alert')).not.toBeInTheDocument());
    await closeFields(user);
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
    await openFields(user);
    await addField(user);
    await saveFields(user);
    const schema = fieldsDialog();
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
    const dialog = await openEdit(user);
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
    await typeEnv(user, 'only for web');

    await openTab(user, /^Subgroups/);
    await user.click(await screen.findByRole('link', { name: 'web_eu' }));
    // Leaving with the edit asks first.
    await acceptConfirm(user);
    expect(await screen.findByRole('heading', { level: 1, name: 'web_eu' })).toBeInTheDocument();
    expect(await screen.findByLabelText('env')).toHaveValue('prod');
  });

  it('keeps unsaved values across the tabs without asking', async () => {
    const user = userEvent.setup();
    mockApi(routes('admin'));
    renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'kept');
    await openTab(user, /^Members/);
    expect(await screen.findByRole('link', { name: 'web-01' })).toBeInTheDocument();
    // Hidden, not gone: one labelled panel per tab, the open one shown.
    expect(screen.queryByRole('dialog', { name: 'Are you sure?' })).not.toBeInTheDocument();
    expect(screen.getByLabelText('env')).not.toBeVisible();
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', screen.getByRole('tab', { name: /^Members/ }).id);
    expect(screen.getByRole('tab', { name: /^Members/ })).toHaveAttribute('aria-controls', screen.getByRole('tabpanel').id);
    await openTab(user, /^Variables/);
    expect(await screen.findByLabelText('env')).toHaveValue('kept');
    expect(screen.getByRole('button', { name: 'Save values' })).toBeEnabled();
  });

  it('asks before the page is left with unsaved values', async () => {
    const user = userEvent.setup();
    mockApi(routes('admin'));
    const { router } = renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'kept');
    await user.click(screen.getByRole('link', { name: 'prod' }));
    expect(await declineConfirm(user)).toBe('Leave this page? Your unsaved changes will be lost.');
    expect(router.state.location.pathname).toBe('/o/acme/groups/g1');
    expect(screen.getByLabelText('env')).toHaveValue('kept');

    await user.click(screen.getByRole('link', { name: 'prod' }));
    await acceptConfirm(user);
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/groups/p1'));
  });

  it('does not ask about unsaved values once the group is deleted', async () => {
    const user = userEvent.setup();
    mockApi(routes('admin', { 'DELETE /api/v1/orgs/acme/inventory/groups/g1': { ok: true } }));
    const { router } = renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'gone with the group');
    await user.click(screen.getByRole('button', { name: 'More actions for web' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete …' }));
    expect(await acceptConfirm(user)).toMatch(/^Delete group web\?/);
    expect(await screen.findByText('GROUPS')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/groups');
    expect(screen.queryByRole('dialog', { name: 'Are you sure?' })).not.toBeInTheDocument();
  });
});
