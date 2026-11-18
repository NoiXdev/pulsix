import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { addField, closeFields, fieldsDialog, group, openFields, refetch, routes, renderAt, SCHEMA, schemaSaved, stale, typeEnv } from './groupPageTestKit.tsx';

/** A schema someone else saved: one more group field. */
const OTHER = { fields: [...SCHEMA.fields, { name: 'region', type: 'string' }] };

interface HeldSave {
  /** What a read of the group returns: a refetch picks up whatever the
   * test set last. */
  current: ReturnType<typeof group>;
  /** The saves, in order. */
  puts: MockRequest[];
  /** What the held save answers once released. */
  answer: unknown;
  /** Lets the held save answer. */
  release: () => void;
}

/** The page against a group whose `what` saves are held until the test
 * releases them, so the group can change while one is in flight. */
function heldSavePage(what: 'vars' | 'schema') {
  const state: HeldSave = { current: group(), puts: [], answer: undefined, release: () => undefined };
  mockApi(
    routes('admin', {
      'GET /api/v1/orgs/acme/inventory/groups/g1': () => state.current,
      [`PUT /api/v1/orgs/acme/inventory/groups/g1/${what}`]: async (req: MockRequest) => {
        state.puts.push(req);
        await new Promise<void>((r) => (state.release = r));
        return state.answer;
      },
    }),
  );
  const { qc } = renderAt('/o/acme/groups/g1');
  return { state, qc };
}

/** refused is the API's 422 for a save, with one problem at `location`. */
const refused = (location: string, message: string) =>
  new Response(JSON.stringify({ status: 422, errors: [{ location, message }] }), { status: 422, headers: { 'Content-Type': 'application/json' } });

const valuesRegion = () => screen.getByRole('region', { name: 'Group values' });
const schemaRegion = fieldsDialog;
const schemaSave = () => within(schemaRegion()).getByRole('button', { name: 'Save' });
/** The schema list's field names. */
const listedNames = () => within(within(schemaRegion()).getByRole('list', { name: 'Fields' })).getAllByRole('listitem').map((r) => r.querySelector('.font-mono')?.textContent);

/** saveValues edits the values (Save waits for an edit) and saves them. */
async function saveValues(user: ReturnType<typeof userEvent.setup>) {
  await typeEnv(user, 'mine');
  await user.click(screen.getByRole('button', { name: 'Save values' }));
}

const schemaSaveEnabled = () => waitFor(() => expect(schemaSave()).toBeEnabled());

const valuesSaveEnabled = () => waitFor(() => expect(screen.getByRole('button', { name: 'Save values' })).toBeEnabled());

/** A save's problems and its stale notice belong to the draft the save was
 * started on, and to the revision an answer brought (backlog 7.91, items
 * 4 and 5): a draft that someone else's write replaced meanwhile is shown
 * without them. */
describe('GroupDetailPage problems', () => {
  // A values save needs an edit, and an edited draft does not follow
  // someone else's values: the refusal still describes what it shows.
  it("keeps the problems of a refused values save beside the edited draft while someone else's values land", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('vars');
    await saveValues(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));

    state.current = group({ revision: 4, vars: { env: 'staging' } });
    await refetch(qc);
    state.answer = refused('body.vars.env', 'is required');
    state.release();

    await valuesSaveEnabled();
    expect(screen.getByLabelText('env')).toHaveValue('mine');
    expect(await within(valuesRegion()).findByText('is required')).toBeInTheDocument();
  });

  it("drops the problems of a refused schema save when someone else's schema replaced the draft meanwhile", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('schema');
    await openFields(user);
    await addField(user);
    await schemaSaveEnabled();
    await user.click(schemaSave());
    await waitFor(() => expect(state.puts).toHaveLength(1));
    // Discarded while the save is in flight, the draft follows the group.
    await closeFields(user);

    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    state.answer = refused('body.schema.fields[0].name', 'is taken');
    state.release();

    await openFields(user);
    await waitFor(() => expect(listedNames()).toContain('region'));
    expect(within(schemaRegion()).queryByText(/The schema has/)).not.toBeInTheDocument();
  });

  it("drops the problems a values save answered when someone else's values landed before that answer", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('vars');
    await saveValues(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));

    state.current = group({ revision: 5, vars: { env: 'other' } });
    await refetch(qc);
    // The save stored revision 4, which someone else's 5 has replaced.
    state.answer = group({ revision: 4, vars: {}, problems: [{ path: 'env', message: 'is required' }] });
    state.release();

    await screen.findByText('Group values saved');
    expect(screen.getByLabelText('env')).toHaveValue('other');
    expect(within(valuesRegion()).queryByText('is required')).not.toBeInTheDocument();
  });

  it("keeps the stale notice of a refused values save beside the edited draft while someone else's values land", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('vars');
    await saveValues(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));

    state.current = group({ revision: 4, vars: { env: 'staging' } });
    await refetch(qc);
    state.answer = stale();
    state.release();

    // The edited draft still is not what made the save stale.
    expect(await within(valuesRegion()).findByRole('button', { name: 'Reload' })).toBeInTheDocument();
    expect(screen.getByLabelText('env')).toHaveValue('mine');
  });

  it("drops the stale notice of a refused schema save when someone else's schema replaced the draft meanwhile", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('schema');
    await openFields(user);
    await addField(user);
    await schemaSaveEnabled();
    await user.click(schemaSave());
    await waitFor(() => expect(state.puts).toHaveLength(1));
    await closeFields(user);

    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    state.answer = stale();
    state.release();

    // The draft already shows the schema that made the save stale.
    await openFields(user);
    await waitFor(() => expect(listedNames()).toContain('region'));
    expect(within(schemaRegion()).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
  });

  it('drops the stale notice of an earlier refusal once a save of the same draft succeeds', async () => {
    const user = userEvent.setup();
    let puts = 0;
    let release: () => void = () => undefined;
    mockApi(
      routes('admin', {
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': async (req: MockRequest) => {
          puts += 1;
          if (puts === 1) return stale();
          await new Promise<void>((r) => (release = r));
          return group({ revision: 4, vars: (req.body as { vars: Record<string, unknown> }).vars });
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    const env = await screen.findByLabelText('env');
    await user.type(env, 'x');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    expect(await within(valuesRegion()).findByRole('button', { name: 'Reload' })).toBeInTheDocument();

    // The next save succeeds with an edit typed meanwhile, so the draft is
    // kept and its generation stays the one the refusal was reported on.
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await waitFor(() => expect(puts).toBe(2));
    await user.type(screen.getByLabelText('env'), 'y');
    release();

    await screen.findByText('Group values saved');
    expect(screen.getByLabelText('env')).toHaveValue('prodxy');
    expect(within(valuesRegion()).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
  });

  it('drops the stale notice of an earlier refusal once a schema save of the same draft succeeds', async () => {
    const user = userEvent.setup();
    let puts = 0;
    let release: () => void = () => undefined;
    mockApi(
      routes('admin', {
        'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': async (req: MockRequest) => {
          puts += 1;
          if (puts === 1) return stale();
          await new Promise<void>((r) => (release = r));
          return schemaSaved(4, (req.body as { schema: unknown }).schema);
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await openFields(user);
    await addField(user);
    await schemaSaveEnabled();
    await user.click(schemaSave());
    expect(await within(schemaRegion()).findByRole('button', { name: 'Reload' })).toBeInTheDocument();

    await user.click(schemaSave());
    await waitFor(() => expect(puts).toBe(2));
    await addField(user);
    release();

    await screen.findByText('Schema saved');
    expect(within(schemaRegion()).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
  });
});
