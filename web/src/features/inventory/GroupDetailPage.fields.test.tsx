import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { fieldsDialog, group, openFields, routes, renderAt, saveFields, schemaSaved } from './groupPageTestKit.tsx';

/** The Manage fields dialog end to end: an edit made with a row's badges
 * is what the save sends, on the group's revision, and a save that stored
 * the draft closes the dialog. */
describe('GroupDetailPage fields dialog', () => {
  it('saves the badge edits with If-Match and closes', async () => {
    const user = userEvent.setup();
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': (req: MockRequest) => {
          puts.push(req);
          return schemaSaved(4, (req.body as { schema: unknown }).schema);
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    const dialog = await openFields(user);
    expect(dialog).toHaveAccessibleName('Manage fields');
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled();

    await user.click(within(dialog).getByRole('button', { name: 'Requirement of domain: Required' }));
    await user.click(within(dialog).getByRole('button', { name: 'Scope of env: Group' }));
    await user.click(within(dialog).getByRole('button', { name: 'Type of env: Text' }));
    await user.click(screen.getByRole('menuitemradio', { name: /^Integer/ }));
    expect(within(fieldsDialog()).getByText('Unsaved changes')).toBeInTheDocument();

    await saveFields(user);
    await screen.findByText('Schema saved');
    expect(puts).toHaveLength(1);
    expect(puts[0].headers.get('If-Match')).toBe('"3"');
    expect(puts[0].body).toEqual({
      schema: {
        fields: [
          { name: 'env', type: 'int', required: true, scope: 'host' },
          { name: 'domain', type: 'string', scope: 'host' },
        ],
      },
    });
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Manage fields' })).not.toBeInTheDocument());
  });

  it('keeps the dialog open with the problems of a refused save', async () => {
    const user = userEvent.setup();
    mockApi(
      routes('admin', {
        'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': () =>
          new Response(JSON.stringify({ status: 422, errors: [{ location: 'body.schema.fields[0].name', message: 'is taken' }] }), {
            status: 422,
            headers: { 'Content-Type': 'application/json' },
          }),
      }),
    );
    renderAt('/o/acme/groups/g1');
    const dialog = await openFields(user);
    await user.click(within(dialog).getByRole('button', { name: 'Requirement of env: Required' }));
    await saveFields(user);
    expect(await within(dialog).findByText(/The schema has 1 problem/)).toBeInTheDocument();
    // env is selected, so its name shows the problem.
    expect(within(within(dialog).getByRole('tabpanel')).getByText('is taken')).toBeInTheDocument();
  });

  it('flags the defaults an assistant set and accepts one as live', async () => {
    const user = userEvent.setup();
    let accepted: MockRequest | undefined;
    const schema = { fields: [{ name: 'env', type: 'string', default: '{{ x }}' }, { name: 'domain', type: 'string', scope: 'host', default: 'a.io' }] };
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema, assistant_default_keys: ['env'] }),
        'POST /api/v1/orgs/acme/inventory/groups/g1/schema/accept': (req: MockRequest) => {
          accepted = req;
          return group({ schema, revision: 4 });
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    const dialog = await openFields(user);
    const rows = within(within(dialog).getByRole('list', { name: 'Fields' })).getAllByRole('listitem');
    expect(within(rows[0]).getByText('assistant default')).toBeInTheDocument();
    expect(within(rows[1]).queryByText('assistant default')).not.toBeInTheDocument();
    const mark = within(dialog).getByTestId('assistant-default-env');
    expect(mark).toHaveTextContent('Default set by an assistant — used as plain text until a person changes or accepts it');
    await user.click(within(mark).getByRole('button', { name: 'Accept env as live' }));
    await screen.findByText('env is live now');
    expect(accepted?.headers.get('If-Match')).toBe('"3"');
    expect(accepted?.body).toEqual({ keys: ['env'] });
  });

  it('offers no accept while the default on screen is not the stored one (final review M3)', async () => {
    const user = userEvent.setup();
    const schema = { fields: [{ name: 'env', type: 'string', default: '{{ x }}' }] };
    mockApi(routes('admin', { 'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema, assistant_default_keys: ['env'] }) }));
    renderAt('/o/acme/groups/g1');
    const dialog = await openFields(user);
    const mark = within(dialog).getByTestId('assistant-default-env');
    const accept = within(mark).getByRole('button', { name: 'Accept env as live' });
    expect(accept).toBeEnabled();
    await user.type(within(dialog).getByLabelText('Default'), 'y');
    expect(accept).toBeDisabled();
    expect(within(mark).getByText('Save or discard your edit before accepting the stored value')).toBeInTheDocument();
  });
});
