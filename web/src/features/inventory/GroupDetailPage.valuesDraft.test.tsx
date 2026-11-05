import { afterEach, describe, expect, it } from 'vitest';
import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { defaultScheduler, notifyManager } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { group, refetch, routes, renderAt, schemaSaved, stale, typeEnv, withSecret } from './groupPageTestKit.tsx';

/** A group whose stored values miss the required `env`: the server flags it. */
const flagged = () => group({ revision: 3, vars: {}, problems: [{ path: 'env', message: 'is required' }] });

/** holdQueryNotifications keeps the query client from handing new results
 * to the page until the returned release runs, so a test can make a fresh
 * read reach the page only after the form's own render. */
function holdQueryNotifications() {
  const held: (() => void)[] = [];
  notifyManager.setScheduler((cb) => held.push(cb));
  return async () => {
    notifyManager.setScheduler(defaultScheduler);
    await act(async () => {
      held.splice(0).forEach((cb) => cb());
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  };
}

const valuesRegion = () => screen.getByRole('region', { name: 'Group values' });

/** What a values draft shows besides its values (backlog 7.90): the
 * problems come from the answer that replaced the draft — a save or a
 * reload is the authority for the content the draft then holds, whenever
 * the page's own read of the group lands — and a draft replaced wholesale
 * is no longer the one a stale notice was about. */
describe('GroupDetailPage values draft', () => {
  afterEach(() => notifyManager.setScheduler(defaultScheduler));

  it('a save that clears a flagged problem shows none, even when the refetch lands after it', async () => {
    const user = userEvent.setup();
    let current = flagged();
    let stored: ReturnType<typeof group> | undefined;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          // The read the save triggers still answers the group as it was:
          // the stored values reach the page only with a later refetch.
          stored = group({ revision: 4, vars: (req.body as { vars: unknown }).vars });
          return stored;
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');
    expect(within(valuesRegion()).getByText('is required')).toBeInTheDocument();
    await typeEnv(user, 'staging');

    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(within(valuesRegion()).queryByText('is required')).not.toBeInTheDocument();

    current = stored!;
    await refetch(qc);
    expect(screen.getByLabelText('env')).toHaveValue('staging');
    expect(within(valuesRegion()).queryByText('is required')).not.toBeInTheDocument();
  });

  it('a reload that brings values without a flagged problem shows none, even when the read lands after it', async () => {
    const user = userEvent.setup();
    let current = flagged();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': () => {
          // Someone else filled the value first.
          current = group({ revision: 4, vars: { env: 'staging' } });
          return stale();
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');
    expect(within(valuesRegion()).getByText('is required')).toBeInTheDocument();
    await typeEnv(user, 'mine');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    const reload = await within(valuesRegion()).findByRole('button', { name: 'Reload' });

    const release = holdQueryNotifications();
    await user.click(reload);
    await waitFor(() => expect(screen.getByLabelText('env')).toHaveValue('staging'));
    expect(within(valuesRegion()).queryByRole('alert')).not.toBeInTheDocument();

    await release();
    await refetch(qc);
    expect(screen.getByLabelText('env')).toHaveValue('staging');
    expect(within(valuesRegion()).queryByRole('alert')).not.toBeInTheDocument();
  });

  it('drops the stale notice when the draft follows values someone else saved', async () => {
    const user = userEvent.setup();
    let current = group();
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          return puts.length === 1 ? stale() : group({ revision: 5, vars: { env: 'staging' } });
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');

    // Someone else saved other values; the page has not read them yet.
    current = group({ revision: 4, vars: { env: 'staging' } });
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    expect(await within(valuesRegion()).findByRole('button', { name: 'Reload' })).toBeInTheDocument();

    // The unedited draft follows them, so the notice no longer applies.
    await refetch(qc);
    expect(screen.getByLabelText('env')).toHaveValue('staging');
    expect(within(valuesRegion()).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it("follows the page's own rename and same-schema save with an edited values draft, not someone else's write", async () => {
    const user = userEvent.setup();
    let current = group();
    const puts: MockRequest[] = [];
    const schemaPuts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          current = group({ ...current.group, revision: current.group.revision + 1, name: (req.body as { name: string }).name });
          return current;
        },
        // Stores the schema it already had, so the values keep their fields.
        'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': (req: MockRequest) => {
          schemaPuts.push(req);
          current = group({ ...current.group, revision: current.group.revision + 1 });
          return schemaSaved(current.group.revision, current.group.schema);
        },
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          if (puts.length > 1) return stale();
          current = group({ ...current.group, revision: 6, vars: (req.body as { vars: Record<string, unknown> }).vars });
          return current;
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'staging');

    // The page's own rename (3 → 4) and schema save (4 → 5): nobody else
    // wrote in between, so the edited draft follows them.
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });
    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await screen.findByRole('heading', { level: 1, name: 'web_fe' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schema' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Save schema' }));
    await screen.findByText('Schema saved');
    expect(schemaPuts[0].headers.get('If-Match')).toBe('"4"');
    expect(screen.getByLabelText('env')).toHaveValue('staging');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(puts[0].headers.get('If-Match')).toBe('"5"');

    // Edited again on 6; someone else writes 7, then the page's own schema
    // save goes 7 → 8. That step does not start at 6, so it is not followed.
    await typeEnv(user, 'mine');
    current = group({ ...current.group, revision: 7 });
    await refetch(qc);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schema' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Save schema' }));
    await waitFor(() => expect(schemaPuts).toHaveLength(2));
    expect(schemaPuts[1].headers.get('If-Match')).toBe('"7"');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save values' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1].headers.get('If-Match')).toBe('"6"');
    const values = screen.getByRole('region', { name: 'Group values' });
    expect(await within(values).findByRole('button', { name: 'Reload' })).toBeInTheDocument();
  });

  it("keeps an edited values draft over someone else's other values, and its save is refused", async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          return stale();
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'mine');

    current = withSecret(4, 'staging');
    await refetch(qc);
    expect(screen.getByLabelText('env')).toHaveValue('mine');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    const values = screen.getByRole('region', { name: 'Group values' });
    expect(await within(values).findByRole('button', { name: 'Reload' })).toBeInTheDocument();
    expect(puts[0].headers.get('If-Match')).toBe('"3"');
    expect(puts[0].body).toMatchObject({ vars: { env: 'mine' } });
  });
});
