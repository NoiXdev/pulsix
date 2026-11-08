import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { addField, group, refetch, routes, renderAt, SCHEMA, schemaSaved, stale } from './groupPageTestKit.tsx';

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
const schemaRegion = () => screen.getByRole('region', { name: 'Variable schema' });

async function saveValues(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByLabelText('env');
  await user.click(screen.getByRole('button', { name: 'Save values' }));
}

const schemaSaveEnabled = () => waitFor(() => expect(screen.getByRole('button', { name: 'Save schema' })).toBeEnabled());

const valuesSaveEnabled = () => waitFor(() => expect(screen.getByRole('button', { name: 'Save values' })).toBeEnabled());

/** A save's problems and its stale notice belong to the draft the save was
 * started on, and to the revision an answer brought (backlog 7.91, items
 * 4 and 5): a draft that someone else's write replaced meanwhile is shown
 * without them. */
describe('GroupDetailPage problems', () => {
  it("drops the problems of a refused values save when someone else's values replaced the draft meanwhile", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('vars');
    await saveValues(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));

    state.current = group({ revision: 4, vars: { env: 'staging' } });
    await refetch(qc);
    state.answer = refused('body.vars.env', 'is required');
    state.release();

    await valuesSaveEnabled();
    expect(screen.getByLabelText('env')).toHaveValue('staging');
    expect(within(valuesRegion()).queryByText('is required')).not.toBeInTheDocument();
  });

  it("drops the problems of a refused schema save when someone else's schema replaced the draft meanwhile", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('schema');
    await screen.findByRole('table', { name: 'Fields' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schema' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Save schema' }));
    await waitFor(() => expect(state.puts).toHaveLength(1));

    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    state.answer = refused('body.schema.fields[0].name', 'is taken');
    state.release();

    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schema' })).toBeEnabled());
    expect(within(schemaRegion()).getByText('region')).toBeInTheDocument();
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

  it("drops the stale notice of a refused values save when someone else's values replaced the draft meanwhile", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('vars');
    await saveValues(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));

    state.current = group({ revision: 4, vars: { env: 'staging' } });
    await refetch(qc);
    state.answer = stale();
    state.release();

    // The draft already shows the values that made the save stale: its own
    // save would succeed, so no notice asks to reload them.
    await valuesSaveEnabled();
    expect(screen.getByLabelText('env')).toHaveValue('staging');
    expect(within(valuesRegion()).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
  });

  it("drops the stale notice of a refused schema save when someone else's schema replaced the draft meanwhile", async () => {
    const user = userEvent.setup();
    const { state, qc } = heldSavePage('schema');
    await screen.findByRole('table', { name: 'Fields' });
    await schemaSaveEnabled();
    await user.click(screen.getByRole('button', { name: 'Save schema' }));
    await waitFor(() => expect(state.puts).toHaveLength(1));

    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    state.answer = stale();
    state.release();

    // The draft already shows the schema that made the save stale.
    await schemaSaveEnabled();
    expect(within(schemaRegion()).getByText('region')).toBeInTheDocument();
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
    await screen.findByRole('table', { name: 'Fields' });
    await addField(user);
    await schemaSaveEnabled();
    await user.click(screen.getByRole('button', { name: 'Save schema' }));
    expect(await within(schemaRegion()).findByRole('button', { name: 'Reload' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save schema' }));
    await waitFor(() => expect(puts).toBe(2));
    await addField(user);
    release();

    await screen.findByText('Schema saved');
    expect(within(schemaRegion()).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
  });

  it("keeps a refused schema draft's problems while it follows the page's own values save", async () => {
    const user = userEvent.setup();
    let current = group();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': () => refused('body.schema.fields[0].name', 'is taken'),
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          current = group({ revision: 4, vars: (req.body as { vars: Record<string, unknown> }).vars });
          return current;
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByRole('table', { name: 'Fields' });
    await addField(user);
    await schemaSaveEnabled();
    await user.click(screen.getByRole('button', { name: 'Save schema' }));
    expect(await within(schemaRegion()).findByText(/The schema has 1 problem/)).toBeInTheDocument();

    // The page's own values save moves the edited draft from 3 to 4; its
    // content, which the problems describe, is unchanged.
    await saveValues(user);
    await screen.findByText('Group values saved');
    expect(within(schemaRegion()).getByText(/The schema has 1 problem/)).toBeInTheDocument();
  });
});
