import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { group, member, MEMBERS, refetch, renderAt, routes } from './groupPageTestKit.tsx';

/** Backlog 7.89 (Ruling 2): an open dialog keeps the revision it was filled
 * from; a background refetch of a newer one must not become what its save
 * sends. */
describe('GroupDetailPage dialog revision', () => {
  it('the settings dialog saves on the revision it was opened on through a refetch', async () => {
    const user = userEvent.setup();
    let current = group();
    let patched: MockRequest | undefined;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          patched = req;
          return group({ revision: 5 });
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });

    current = group({ revision: 4, description: 'Refetched' });
    await refetch(qc);
    // The refetched group is on the page; the open form is not refilled.
    expect(await screen.findByText('Refetched')).toBeInTheDocument();
    expect(within(dialog).getByLabelText('Description')).toHaveValue('Front web servers');

    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patched).toBeDefined());
    expect(patched?.headers.get('If-Match')).toBe('"3"');
  });

  /** Backlog 7.91 (Ruling 6): the page's own values saves are no foreign
   * write, so the dialog follows them, however many land while it is open. */
  it('the settings dialog opened during a values save follows that save', async () => {
    const user = userEvent.setup();
    let current = group();
    let patched: MockRequest | undefined;
    const puts: MockRequest[] = [];
    let release: () => void = () => undefined;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': async (req: MockRequest) => {
          puts.push(req);
          if (puts.length === 1) await new Promise<void>((resolve) => (release = resolve));
          current = group({ revision: 3 + puts.length });
          return current;
        },
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          patched = req;
          return group({ revision: 6 });
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });

    release();
    await screen.findByText('Group values saved');
    await refetch(qc);
    // A second save, on the revision the first one stored, and its refetch:
    // the page now shows revision 5, above the one the dialog was filled from.
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(2));
    await refetch(qc);

    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patched).toBeDefined());
    expect(patched?.headers.get('If-Match')).toBe('"5"');
  });

  /** Backlog 7.91: the dialog's own earlier save is not followed. A form
   * filled from the group as it was before that save would undo it. */
  it('a reopened settings dialog does not follow its own earlier save', async () => {
    const user = userEvent.setup();
    let reads = 0;
    let saved = false;
    const patches: MockRequest[] = [];
    mockApi(
      routes('admin', {
        // The refetch after the first save fails: the page keeps showing
        // the group from before it.
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => {
          if (!saved) return group();
          reads += 1;
          return new Response(JSON.stringify({ status: 500, detail: 'down' }), { status: 500, headers: { 'Content-Type': 'application/json' } });
        },
        // The first save stores revision 4; a later one sent on 3 is stale.
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          patches.push(req);
          if (saved && req.headers.get('If-Match') !== '"4"') {
            return new Response(JSON.stringify({ status: 412, detail: 'changed' }), { status: 412, headers: { 'Content-Type': 'application/json' } });
          }
          saved = true;
          return group({ revision: 4, name: 'web_fe' });
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'Edit' }));
    let dialog = await screen.findByRole('dialog', { name: 'Edit group' });
    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Edit group' })).not.toBeInTheDocument());
    expect(patches[0].headers.get('If-Match')).toBe('"3"');
    await waitFor(() => expect(reads).toBeGreaterThan(0));

    // Reopened, the form is filled from the group the page still shows.
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    dialog = await screen.findByRole('dialog', { name: 'Edit group' });
    expect(within(dialog).getByLabelText('Name')).toHaveValue('web');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patches).toHaveLength(2));
    expect(patches[1].headers.get('If-Match')).toBe('"3"');
    expect(await within(dialog).findByText(/This group changed since you loaded it/)).toBeInTheDocument();
  });

  it('the member values dialog saves on the member revision it was opened on through a refetch', async () => {
    const user = userEvent.setup();
    let members = MEMBERS;
    let put: MockRequest | undefined;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1/members': () => members,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/members/h1': (req: MockRequest) => {
          put = req;
          return { member: member('h1', 'web-01', { revision: 9 }) };
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
    await user.click(screen.getByRole('menuitem', { name: 'Edit values' }));
    const dialog = await screen.findByRole('dialog', { name: 'Values of web-01' });

    // Someone else saved web-01's values (revision 8) and assigned web-03.
    members = { members: [member('h1', 'web-01', { revision: 8 }), MEMBERS.members[1], member('h3', 'web-03')] };
    await refetch(qc);
    expect(await screen.findByRole('button', { name: 'Actions for web-03' })).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.headers.get('If-Match')).toBe('"7"');
  });
});
