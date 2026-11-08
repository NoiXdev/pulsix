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

  /** Backlog 7.91 (Ruling 6): the page's own write is no foreign one, so the
   * dialog follows it the way an edited draft does. */
  it('the settings dialog opened during a values save follows that save', async () => {
    const user = userEvent.setup();
    let current = group();
    let patched: MockRequest | undefined;
    let put: MockRequest | undefined;
    let release: () => void = () => undefined;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': async (req: MockRequest) => {
          put = req;
          await new Promise<void>((resolve) => (release = resolve));
          current = group({ revision: 4 });
          return current;
        },
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          patched = req;
          return group({ revision: 5 });
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(put).toBeDefined());
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });

    release();
    await screen.findByText('Group values saved');

    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patched).toBeDefined());
    expect(patched?.headers.get('If-Match')).toBe('"4"');
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
