import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { group, refetch, routes, renderAt, SCHEMA, stale } from './groupPageTestKit.tsx';

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
});
