import { describe, expect, it } from 'vitest';
import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { group, refetch, routes, renderAt, SCHEMA, schemaSaved, stale, typeEnv } from './groupPageTestKit.tsx';

/** A group with a stored secret, which every read returns masked: a refetch
 * of it brings the same values whatever its revision, unless `env` changed. */
const withSecret = (revision: number, env = 'prod') =>
  group({
    revision,
    schema: { fields: [...SCHEMA.fields, { name: 'db_password', type: 'secret' }] },
    vars: { env, db_password: { $secret: 'set' } },
  });

/** The revision a values save sends as its If-Match (backlogs 7.86, 7.90).
 * The form's draft follows useRevisionDraft: an unedited draft follows the
 * group (its values and its revision), an edited one stays on the revision
 * it was edited on, and a saved draft is rebuilt from what was stored. */
describe('GroupDetailPage values revision', () => {
  it('a refetch that only bumps the revision keeps the save on the revision the draft started from', async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          return puts.length === 1 ? stale() : withSecret(5);
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'staging');

    // Someone else saved the group while this draft was being edited; a
    // background refetch brings revision 4 with the same (masked) values, so
    // the form is not started over.
    current = withSecret(4);
    await act(async () => {
      await qc.refetchQueries();
    });

    await user.click(screen.getByRole('button', { name: 'Save values' }));
    const values = screen.getByRole('region', { name: 'Group values' });
    await user.click(await within(values).findByRole('button', { name: 'Reload' }));
    expect(puts[0].headers.get('If-Match')).toBe('"3"');
    await waitFor(() => expect(within(values).queryByRole('alert')).not.toBeInTheDocument());
    expect(screen.getByLabelText('env')).toHaveValue('staging');

    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(puts).toHaveLength(2);
    expect(puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it('lets an unedited draft follow a refetched revision, as after the user renamed the group', async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          return withSecret(5);
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');

    // Nothing typed yet: the draft still is the stored values, so a refetch
    // that brings revision 4 (a rename, say) is what it builds on.
    current = withSecret(4);
    await act(async () => {
      await qc.refetchQueries();
    });
    await typeEnv(user, 'staging');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(puts).toHaveLength(1);
    expect(puts[0].headers.get('If-Match')).toBe('"4"');
  });

  it('keeps an edit that was typed back to the start on the revision it started from', async () => {
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
    await typeEnv(user, 'staging');
    await typeEnv(user, 'prod');

    current = withSecret(4);
    await act(async () => {
      await qc.refetchQueries();
    });
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].headers.get('If-Match')).toBe('"3"');
  });

  it('keeps a reloaded draft on the reloaded revision through a later refetch', async () => {
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
    await typeEnv(user, 'staging');
    current = withSecret(4);
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    const values = screen.getByRole('region', { name: 'Group values' });
    await user.click(await within(values).findByRole('button', { name: 'Reload' }));
    await waitFor(() => expect(within(values).queryByRole('alert')).not.toBeInTheDocument());

    // The kept draft is still not the stored values: a later change by
    // someone else (revision 5, same mask) must not become its base.
    current = withSecret(5);
    await act(async () => {
      await qc.refetchQueries();
    });
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it('rebuilds a saved draft with keep markers, so a later save never resends a typed secret', async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          // Stored as the same mask, so the section is not started over.
          current = withSecret(3 + puts.length);
          return current;
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('db_password');
    await user.click(screen.getByRole('button', { name: 'Replace' }));
    await user.type(screen.getByLabelText('db_password'), 'n3w');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(puts[0].body).toEqual({ vars: { env: 'prod', db_password: 'n3w' } });

    // Someone else rotates the secret: revision 5, the same mask.
    current = withSecret(5);
    await act(async () => {
      await qc.refetchQueries();
    });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save values' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1].headers.get('If-Match')).toBe('"5"');
    expect(puts[1].body).toEqual({ vars: { env: 'prod', db_password: { $secret: 'keep', from: 'db_password' } } });
  });

  it('keeps an edit typed while a save was in flight, on the revision that save stored', async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    let release: () => void = () => undefined;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': async (req: MockRequest) => {
          puts.push(req);
          if (puts.length === 1) await new Promise<void>((resolve) => (release = resolve));
          current = withSecret(3 + puts.length);
          return current;
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    await typeEnv(user, 'typed meanwhile');
    release();
    await screen.findByText('Group values saved');
    expect(screen.getByLabelText('env')).toHaveValue('typed meanwhile');

    // The edit is still an edit: it builds on revision 4, which that save
    // stored, and does not follow a later refetch.
    current = withSecret(5);
    await act(async () => {
      await qc.refetchQueries();
    });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save values' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1].headers.get('If-Match')).toBe('"4"');
    expect(puts[1].body).toMatchObject({ vars: { env: 'typed meanwhile' } });
  });

  it('saves the next values on the revision the previous save stored', async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          current = withSecret(3 + puts.length);
          return current;
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');

    // Saving the values unchanged stores a new revision with the same
    // values, so the form stays as it is and the next save builds on it.
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save values' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[0].headers.get('If-Match')).toBe('"3"');
    expect(puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it('keeps an edit typed while a save that changes the stored values was in flight', async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    let release: () => void = () => undefined;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': async (req: MockRequest) => {
          puts.push(req);
          if (puts.length === 1) {
            await new Promise<void>((resolve) => (release = resolve));
            current = withSecret(4, 'staging');
            return current;
          }
          return withSecret(6, 'typed meanwhile');
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'staging');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    await typeEnv(user, 'typed meanwhile');
    release();
    await screen.findByText('Group values saved');
    // The save stored other values than the form started from; the edit
    // typed meanwhile is still the form's.
    expect(screen.getByLabelText('env')).toHaveValue('typed meanwhile');

    current = withSecret(5, 'staging');
    await refetch(qc);
    expect(screen.getByLabelText('env')).toHaveValue('typed meanwhile');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save values' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1].headers.get('If-Match')).toBe('"4"');
    expect(puts[1].body).toMatchObject({ vars: { env: 'typed meanwhile' } });
  });

  it('an unedited values draft follows values someone else saved', async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          return withSecret(5, 'staging');
        },
      }),
    );
    const { qc } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');

    current = withSecret(4, 'staging');
    await refetch(qc);
    await waitFor(() => expect(screen.getByLabelText('env')).toHaveValue('staging'));
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(puts).toHaveLength(1);
    expect(puts[0].headers.get('If-Match')).toBe('"4"');
  });

  it('Reload starts the values over when someone else changed them', async () => {
    const user = userEvent.setup();
    let current = withSecret(3);
    const puts: MockRequest[] = [];
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          puts.push(req);
          if (puts.length === 1) {
            current = withSecret(4, 'staging');
            return stale();
          }
          return withSecret(5, 'staging');
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await typeEnv(user, 'mine');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    const values = screen.getByRole('region', { name: 'Group values' });
    await user.click(await within(values).findByRole('button', { name: 'Reload' }));
    await waitFor(() => expect(within(values).queryByRole('alert')).not.toBeInTheDocument());
    expect(screen.getByLabelText('env')).toHaveValue('staging');

    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(puts).toHaveLength(2);
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
