import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { addField, closeFields, fieldCount, fieldsDialog, group, openFields, refetch, routes, renderAt, saveFields, SCHEMA, schemaSaved, stale } from './groupPageTestKit.tsx';

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

const schemaRegion = fieldsDialog;
const saveSchema = saveFields;

async function reloadSchema(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await within(schemaRegion()).findByRole('button', { name: 'Reload' }));
  await waitFor(() => expect(within(schemaRegion()).queryByRole('alert')).not.toBeInTheDocument());
}

/** The schema list's field names (the rows' select buttons). */
const listedNames = () => within(within(schemaRegion()).getByRole('list', { name: 'Fields' })).getAllByRole('listitem').map((r) => r.querySelector('.font-mono')?.textContent);

const fieldNames = (req: MockRequest) => (req.body as { schema: { fields: { name: string }[] } }).schema.fields.map((f) => f.name);

/** The revision a schema save sends as its If-Match (backlog 7.89): the
 * model of the values save (7.86). An unedited draft follows the group, an
 * edited one stays on the revision it was edited on, and a saved draft is
 * rebuilt from what was stored. Closing the Manage fields dialog discards
 * an edited draft, so the page's other writes (values, rename) can no
 * longer happen while one is edited; the own step of the schema's own save
 * still moves an edit typed while it was in flight. */
describe('GroupDetailPage schema revision', () => {
  it('a refetch that only bumps the revision keeps the schema save on the revision the draft started from', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req, n) => (n === 1 ? stale() : schemaSaved(5, (req.body as { schema: unknown }).schema)));
    await openFields(user);
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
    await openFields(user);
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
    await openFields(user);
    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    await waitFor(() => expect(fieldCount()).toBe(3));
    await addField(user);
    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[0].headers.get('If-Match')).toBe('"4"');
    expect(fieldNames(state.puts[0])).toContain('region');
  });

  it("keeps an edited draft over someone else's schema, and Reload starts it over", async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req, n) => (n === 1 ? stale() : schemaSaved(5, (req.body as { schema: unknown }).schema)));
    await openFields(user);
    await addField(user);
    expect(fieldCount()).toBe(3);

    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    // The page shows the new schema (the values form has its field) ...
    expect(await screen.findByLabelText('region')).toBeInTheDocument();
    // ... and the edited schema draft keeps its own field instead.
    expect(fieldCount()).toBe(3);
    expect(listedNames()).not.toContain('region');

    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));
    expect(state.puts[0].headers.get('If-Match')).toBe('"3"');
    await reloadSchema(user);
    expect(listedNames()).toContain('region');

    // The reload started the draft over: Save waits for an edit again.
    expect(within(schemaRegion()).getByRole('button', { name: 'Save' })).toBeDisabled();
    await addField(user);
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
    await openFields(user);
    await addField(user);
    await saveSchema(user);
    await screen.findByText('Schema saved');
    // What it stored is the draft now: the dialog closed.
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Manage fields' })).not.toBeInTheDocument());

    // Someone else renames the group: revision 5, the schema this page saved.
    state.current = group({ revision: 5, schema: state.current.group.schema });
    await refetch(qc);
    await openFields(user);
    expect(within(schemaRegion()).getByRole('button', { name: 'Save' })).toBeDisabled();
    await addField(user);
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
    await openFields(user);
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

  it('rebuilds a draft discarded while its save was in flight from what that save stored', async () => {
    const user = userEvent.setup();
    let release: (v: unknown) => void = () => undefined;
    const { state } = schemaPage((req, n) =>
      n === 1 ? new Promise((resolve) => (release = resolve)) : schemaSaved(5, (req.body as { schema: unknown }).schema),
    );
    await openFields(user);
    const before = fieldCount();
    await addField(user);
    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(1));
    await addField(user);
    await closeFields(user);
    await openFields(user);
    expect(fieldCount()).toBe(before);

    const stored = (state.puts[0].body as { schema: unknown }).schema;
    state.current = group({ revision: 4, schema: stored });
    release(schemaSaved(4, stored));
    await screen.findByText('Schema saved');
    // The page shows what the save stored, not the schema from before it.
    await waitFor(() => expect(fieldCount()).toBe(before + 1));

    await addField(user);
    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(2));
    expect(state.puts[1].headers.get('If-Match')).toBe('"4"');
    expect(fieldNames(state.puts[1])).toHaveLength(before + 2);
  });

  it('saves the next schema on the revision the previous save stored', async () => {
    const user = userEvent.setup();
    const { state } = schemaPage((req, n) => schemaSaved(3 + n, (req.body as { schema: unknown }).schema));
    await openFields(user);
    await addField(user);
    await saveSchema(user);
    await screen.findByText('Schema saved');
    await openFields(user);
    await addField(user);
    await saveSchema(user);
    await waitFor(() => expect(state.puts).toHaveLength(2));
    expect(state.puts[0].headers.get('If-Match')).toBe('"3"');
    expect(state.puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it('discard lets the draft follow the group again', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req) => schemaSaved(5, (req.body as { schema: unknown }).schema));
    await openFields(user);
    const before = fieldCount();
    await addField(user);
    state.current = group({ revision: 4 });
    await refetch(qc);
    await closeFields(user);
    await openFields(user);
    expect(fieldCount()).toBe(before);
    await addField(user);
    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[0].headers.get('If-Match')).toBe('"4"');
  });

  it('drops the stale notice when the draft is discarded', async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage((req, n) => (n === 1 ? stale() : schemaSaved(5, (req.body as { schema: unknown }).schema)));
    await openFields(user);
    await addField(user);
    state.current = group({ revision: 4 });
    await refetch(qc);
    await saveSchema(user);
    expect(await within(schemaRegion()).findByRole('button', { name: 'Reload' })).toBeInTheDocument();

    // The discarded draft follows the group again: the notice was about the
    // edit, which is gone.
    await closeFields(user);
    await openFields(user);
    expect(within(schemaRegion()).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
    await addField(user);
    await saveSchema(user);
    await screen.findByText('Schema saved');
    expect(state.puts[1].headers.get('If-Match')).toBe('"4"');
  });

  it("clears the problems of a refused save when the draft is discarded and someone else's schema lands", async () => {
    const user = userEvent.setup();
    const { state, qc } = schemaPage(
      () =>
        new Response(JSON.stringify({ status: 422, errors: [{ location: 'body.schema.fields[0].name', message: 'is taken' }] }), {
          status: 422,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    await openFields(user);
    await addField(user);
    await saveSchema(user);
    expect(await within(schemaRegion()).findByText(/The schema has 1 problem/)).toBeInTheDocument();
    // The field it is about is marked in the list.
    expect(within(schemaRegion()).getByRole('img', { name: 'has problems' })).toBeInTheDocument();

    // Discarded, the draft follows the group again, and the problems of the
    // draft that is gone go with it.
    await closeFields(user);
    state.current = group({ revision: 4, schema: OTHER });
    await refetch(qc);
    await openFields(user);
    expect(listedNames()).toContain('region');
    expect(within(schemaRegion()).queryByText(/The schema has/)).not.toBeInTheDocument();
  });
});
