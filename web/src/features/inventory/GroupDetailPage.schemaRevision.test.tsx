import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { addField, fieldCount, group, refetch, routes, renderAt, SCHEMA, schemaSaved, stale, typeEnv } from './groupPageTestKit.tsx';

/** A schema someone else saved: one more group field. */
const OTHER = { fields: [...SCHEMA.fields, { name: 'region', type: 'string' }] };

interface PageState {
  /** What a read of the group returns: a background refetch picks up
   * whatever the test set last. */
  current: ReturnType<typeof group>;
  /** The schema saves, in order. */
  puts: MockRequest[];
}

/** The page against a group whose reads return `state.current` and whose
 * schema saves land in `state.puts`, the nth answered by `answer`. */
function schemaPage(
  answer: (req: MockRequest, n: number, state: PageState) => unknown,
  extra: (state: PageState) => Record<string, unknown> = () => ({}),
) {
  const state: PageState = { current: group(), puts: [] };
  mockApi(
    routes('admin', {
      'GET /api/v1/orgs/acme/inventory/groups/g1': () => state.current,
      'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': (req: MockRequest) => {
        state.puts.push(req);
        return answer(req, state.puts.length, state);
      },
      ...extra(state),
    }),
  );
  const { qc } = renderAt('/o/acme/groups/g1');
  return { state, qc };
}

const schemaRegion = () => screen.getByRole('region', { name: 'Variable schema' });

async function saveSchema(user: ReturnType<typeof userEvent.setup>) {
  await waitFor(() => expect(screen.getByRole('button', { name: 'Save schema' })).toBeEnabled());
  await user.click(screen.getByRole('button', { name: 'Save schema' }));
}

async function reloadSchema(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await within(schemaRegion()).findByRole('button', { name: 'Reload' }));
  await waitFor(() => expect(within(schemaRegion()).queryByRole('alert')).not.toBeInTheDocument());
}

const fieldNames = (req: MockRequest) => (req.body as { schema: { fields: { name: string }[] } }).schema.fields.map((f) => f.name);

/** The revision a schema save sends as its If-Match (backlog 7.89): the
 * model of the values save (7.86). An unedited draft follows the group, an
 * edited one stays on the revision it was edited on, and a saved draft is
 * rebuilt from what was stored. Writes made from this page move an edited
 * draft along with them, a write by someone else never does. */
describe('GroupDetailPage schema revision', () => {
  it('a refetch that only bumps the revision keeps the schema save on the revision the draft started from', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req, n) => (n === 1 ? stale() : schemaSaved(5, (req.body as { schema: unknown }).schema)));
    await screen.findByRole('table', { name: 'Fields' });
    const before = fieldCount();
    await addField(user);

    // Someone else saved the group (not its schema) meanwhile.
    state.current = group({ revision: 4 });
    await refetch(qc);

    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));
    expect(state.puts[0].headers.get('If-Match')).toBe('"3"');
    await reloadSchema(user);
    expect(fieldCount()).toBe(before + 1);

    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts).toHaveLength(2);
    expect(state.puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it('lets an unedited schema draft follow a refetched revision', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req) => schemaSaved(5, (req.body as { schema: unknown }).schema));
    await screen.findByRole('table', { name: 'Fields' });
    state.current = group({ revision: 4 });
    await refetch(qc);
    await addField(user);
    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[0].headers.get('If-Match')).toBe('"4"');
  });

  it('follows a schema someone else saved while the draft is unedited', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req) => schemaSaved(5, (req.body as { schema: unknown }).schema));
    await screen.findByRole('table', { name: 'Fields' });
    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    await waitFor(() => expect(fieldCount()).toBe(3));
    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[0].headers.get('If-Match')).toBe('"4"');
    expect(fieldNames(state.puts[0])).toContain('region');
  });

  it("keeps an edited draft over someone else's schema, and Reload starts it over", async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req, n) => (n === 1 ? stale() : schemaSaved(5, (req.body as { schema: unknown }).schema)));
    await screen.findByRole('table', { name: 'Fields' });
    await addField(user);
    expect(fieldCount()).toBe(3);

    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    // The page shows the new schema (the values form has its field) ...
    expect(await screen.findByLabelText('region')).toBeInTheDocument();
    // ... and the edited schema draft keeps its own field instead.
    expect(fieldCount()).toBe(3);
    expect(within(schemaRegion()).queryByText('region')).not.toBeInTheDocument();

    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));
    expect(state.puts[0].headers.get('If-Match')).toBe('"3"');
    await reloadSchema(user);
    expect(within(schemaRegion()).getByText('region')).toBeInTheDocument();

    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it('rebuilds a saved draft from the response, unedited again', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req, n, st) => {
      const saved = schemaSaved(3 + n, (req.body as { schema: unknown }).schema);
      st.current = { group: saved.group };
      return saved;
    });
    await screen.findByRole('table', { name: 'Fields' });
    await addField(user);
    await saveSchema(user);
    await screen.findByText('Schema saved');

    // Someone else renames the group: revision 5, the schema this page saved.
    state.current = group({ revision: 5, schema: state.current.group.schema });
    await refetch(qc);
    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(2));
    expect(state.puts[1].headers.get('If-Match')).toBe('"5"');
  });

  it('keeps an edit typed while a schema save was in flight, on the revision that save stored', async () => {
    const user = userEvent.setup();
    let release: (v: unknown) => void = () => undefined;
    const { state, qc } = schemaPage((req, n) =>
      n === 1 ? new Promise((resolve) => (release = resolve)) : schemaSaved(5, (req.body as { schema: unknown }).schema),
    );
    await screen.findByRole('table', { name: 'Fields' });
    const before = fieldCount();
    await addField(user);
    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));
    await addField(user);

    const stored = (state.puts[0].body as { schema: unknown }).schema;
    state.current = group({ revision: 4, schema: stored });
    release(schemaSaved(4, stored));
    await screen.findByText('Schema saved');
    expect(fieldCount()).toBe(before + 2);

    // Still an edit: it builds on revision 4, which that save stored, and
    // does not follow a later refetch.
    state.current = group({ revision: 5, schema: stored });
    await refetch(qc);
    expect(fieldCount()).toBe(before + 2);
    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(2));
    expect(state.puts[1].headers.get('If-Match')).toBe('"4"');
    expect(fieldNames(state.puts[1])).toHaveLength(before + 2);
  });

  it('saves the next schema on the revision the previous save stored', async () => {
    const user = userEvent.setup();
    const { state } = schemaPage((req, n) => schemaSaved(3 + n, (req.body as { schema: unknown }).schema));
    await screen.findByRole('table', { name: 'Fields' });
    await saveSchema(user);
    await screen.findByText('Schema saved');
    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(2));
    expect(state.puts[0].headers.get('If-Match')).toBe('"3"');
    expect(state.puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it('discard lets the draft follow the group again', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req) => schemaSaved(5, (req.body as { schema: unknown }).schema));
    await screen.findByRole('table', { name: 'Fields' });
    const before = fieldCount();
    await addField(user);
    state.current = group({ revision: 4 });
    await refetch(qc);
    await user.click(screen.getByRole('button', { name: 'Discard' }));
    expect(fieldCount()).toBe(before);
    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[0].headers.get('If-Match')).toBe('"4"');
  });

  it('drops the stale notice when the draft is discarded', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req, n) => (n === 1 ? stale() : schemaSaved(5, (req.body as { schema: unknown }).schema)));
    await screen.findByRole('table', { name: 'Fields' });
    await addField(user);
    state.current = group({ revision: 4 });
    await refetch(qc);
    await saveSchema(user);
    expect(await within(schemaRegion()).findByRole('button', { name: 'Reload' })).toBeInTheDocument();

    // The discarded draft follows the group again: the notice was about the
    // edit, which is gone.
    await user.click(screen.getByRole('button', { name: 'Discard' }));
    expect(within(schemaRegion()).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it("clears the problems of a refused save when someone else's schema replaces the draft", async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage(
      () =>
        new Response(JSON.stringify({ status: 422, errors: [{ location: 'body.schema.fields[0].name', message: 'is taken' }] }), {
          status: 422,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    await screen.findByRole('table', { name: 'Fields' });
    await saveSchema(user);
    expect(await within(schemaRegion()).findByText(/The schema has 1 problem/)).toBeInTheDocument();

    // The draft is unedited, so someone else's schema replaces it, and the
    // problems of the draft that is gone go with it.
    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    expect(await within(schemaRegion()).findByText('region')).toBeInTheDocument();
    expect(within(schemaRegion()).queryByText(/The schema has/)).not.toBeInTheDocument();
  });

  // Ruling 3: a write made from this page that sent If-Match "r" and got
  // revision r' back is the page's own step; nobody else wrote in between,
  // so an edited draft pinned at r may save on r'.
  it("follows the page's own values save with an edited schema draft", async () => {
    const user = userEvent.setup();
    const varPuts: MockRequest[] = [];
    const { state } = schemaPage(
      (req) => schemaSaved(5, (req.body as { schema: unknown }).schema),
      (st) => ({
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          varPuts.push(req);
          st.current = group({ revision: 4, vars: (req.body as { vars: Record<string, unknown> }).vars });
          return st.current;
        },
      }),
    );
    await screen.findByRole('table', { name: 'Fields' });
    await addField(user);
    await typeEnv(user, 'staging');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(varPuts[0].headers.get('If-Match')).toBe('"3"');

    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[0].headers.get('If-Match')).toBe('"4"');
  });

  it("follows the page's own rename with an edited schema draft", async () => {
    const user = userEvent.setup();
    const patches: MockRequest[] = [];
    const { state } = schemaPage(
      (req) => schemaSaved(5, (req.body as { schema: unknown }).schema),
      (st) => ({
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          patches.push(req);
          st.current = group({ revision: 4, name: (req.body as { name: string }).name });
          return st.current;
        },
      }),
    );
    await screen.findByRole('table', { name: 'Fields' });
    await addField(user);

    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });
    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'web_fe' })).toBeInTheDocument();
    expect(patches[0].headers.get('If-Match')).toBe('"3"');

    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[0].headers.get('If-Match')).toBe('"4"');
  });

  it("does not follow an own save that came after someone else's", async () => {
    const user = userEvent.setup();
    const varPuts: MockRequest[] = [];
    const { state, qc } = schemaPage(
      () => stale(),
      (st) => ({
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          varPuts.push(req);
          st.current = group({ revision: 5, vars: (req.body as { vars: Record<string, unknown> }).vars });
          return st.current;
        },
      }),
    );
    await screen.findByRole('table', { name: 'Fields' });
    await addField(user);

    // Someone else saved the group: revision 4.
    state.current = group({ revision: 4 });
    await refetch(qc);

    // The unedited values follow revision 4; their save is the page's own
    // step from 4 to 5, which the schema draft (pinned at 3) does not take.
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(varPuts[0].headers.get('If-Match')).toBe('"4"');

    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));
    expect(state.puts[0].headers.get('If-Match')).toBe('"3"');
    expect(await within(schemaRegion()).findByRole('button', { name: 'Reload' })).toBeInTheDocument();
  });
});
