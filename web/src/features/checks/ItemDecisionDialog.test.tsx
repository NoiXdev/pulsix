import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { ItemDecisionDialog } from './ItemDecisionDialog.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { settled } from '../../test/settled.ts';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';

const CHECK_KEYS = ['disk', 'service'];

function renderDialog(props: Partial<React.ComponentProps<typeof ItemDecisionDialog>> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ItemDecisionDialog org="acme" checkKeys={CHECK_KEYS} onClose={() => {}} {...props} />
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

describe('ItemDecisionDialog', () => {
  it("renders the true total from the preview, not the length of the capped match list", async () => {
    const user = userEvent.setup();
    let previews = 0;
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () => {
        previews += 1;
        return {
          matches: Array.from({ length: 200 }, (_, i) => ({ host_id: `h${i}`, check_key: 'disk', item: `/mnt/${i}` })),
          total: 4182,
          capped: true,
        };
      },
    });
    renderDialog();

    await pickOption(screen, 'Match kind', { value: 'glob' });
    await user.type(screen.getByLabelText('Pattern'), '/mnt/*');

    // The preview debounces (300ms) rather than firing per keystroke, and
    // renders the server's own total — 4182 — never `matches.length` (200,
    // the cap). "Matches 4182 items" is the sentence this milestone exists
    // to put on screen; "Matches 200 items" would be the silent failure.
    expect(await screen.findByText('Matches 4182 items')).toBeInTheDocument();
    expect(screen.queryByText('Matches 200 items')).not.toBeInTheDocument();
    expect(previews).toBe(1);
  });

  it('offers watch at host scope but not at organization scope, and says why', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'web-1' }], next_cursor: '' },
      'POST /api/v1/orgs/acme/item-decisions/preview': () => ({ matches: [], total: 0, capped: false }),
    });
    renderDialog();

    // Organization scope is the default: no watch option, and a line
    // saying why it is absent rather than leaving it to be puzzled over.
    // Select only renders its rows while open, unlike a native <select>'s
    // always-present <option> list, so the picker is opened first; data-value
    // is read rather than the accessible name, the same reason pickOption
    // reads it instead of textContent (a hinted row's name would include it).
    await user.click(screen.getByLabelText('Verdict'));
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('data-value'))).toEqual(['ignore']);
    await user.keyboard('{Escape}');
    expect(screen.getByText(/watch is not offered at organization scope/i)).toBeInTheDocument();

    await pickOption(screen, 'Applies to', { value: 'host' });
    await pickOption(screen, 'Host', { value: 'h1' });

    await user.click(screen.getByLabelText('Verdict'));
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('data-value'))).toEqual(['ignore', 'watch']);
    await user.keyboard('{Escape}');
    expect(screen.queryByText(/watch is not offered at organization scope/i)).not.toBeInTheDocument();
  });

  it('demands a second confirmation before saving a pattern matching more than the preview can show in full, and does not save without it', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () => ({
        matches: Array.from({ length: 200 }, (_, i) => ({ host_id: 'h1', check_key: 'disk', item: `/mnt/${i}` })),
        total: 350,
        capped: true,
      }),
      'POST /api/v1/orgs/acme/item-decisions': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { decision: { id: 'd1' } };
      },
    });
    renderDialog();

    await pickOption(screen, 'Match kind', { value: 'glob' });
    await user.type(screen.getByLabelText('Pattern'), '/mnt/*');
    await screen.findByText('Matches 350 items');

    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect(await declineConfirm(user)).toContain('350');
    await settled();
    expect(posted).toBeUndefined();

    await user.click(screen.getByRole('button', { name: 'Save' }));
    await acceptConfirm(user);
    await waitFor(() => expect(posted).toBeDefined());
  });

  it('does not ask for a second confirmation when the pattern matches fewer items than the preview can show in full', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () => ({
        matches: [{ host_id: 'h1', check_key: 'disk', item: '/var' }],
        total: 1,
        capped: false,
      }),
      'POST /api/v1/orgs/acme/item-decisions': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { decision: { id: 'd1' } };
      },
    });
    renderDialog();

    await user.type(screen.getByLabelText('Pattern'), '/var');
    await screen.findByText('Matches 1 item');

    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toBeDefined());
    await settled();
    expect(screen.queryByRole('dialog', { name: 'Are you sure?' })).not.toBeInTheDocument();
  });

  it("surfaces the API's own message when an exact pattern contains a glob character", async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () =>
        new Response(
          JSON.stringify({ detail: 'invalid input: exact match pattern must not contain "*"; did you mean match kind "glob"?' }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    });
    renderDialog();

    // Match kind defaults to "exact" — a pattern with a glob character in
    // it is refused by the service, not silently accepted as a literal
    // name nobody has.
    await user.type(screen.getByLabelText('Pattern'), '/var/*');

    expect(await screen.findByText(/did you mean match kind "glob"\?/)).toBeInTheDocument();
  });

  it("shows the server's own refusal of a failed save, not a generic message", async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () => ({ matches: [], total: 0, capped: false }),
      'POST /api/v1/orgs/acme/item-decisions': () =>
        new Response(JSON.stringify({ detail: 'a decision for this exact pattern already exists at this scope' }), {
          status: 409,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
    });
    renderDialog();

    await user.type(screen.getByLabelText('Pattern'), '/var');
    // Save stays disabled until the preview for this exact pattern has
    // landed (see the dedicated staleness test below) — wait for it
    // before clicking, the same way every other test here that clicks
    // Save does.
    await screen.findByText('Matches no currently known item — it still applies the moment one appears');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('a decision for this exact pattern already exists at this scope');
    expect(screen.getByRole('dialog', { name: 'New item decision' })).toBeInTheDocument();
  });

  it('does not save a pattern before its own preview has caught up with it — the debounce window is not a way around the second confirmation', async () => {
    const user = userEvent.setup();
    let posted = false;
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () => ({
        matches: [{ host_id: 'h1', check_key: 'disk', item: '/a' }],
        total: 9999,
        capped: true,
      }),
      'POST /api/v1/orgs/acme/item-decisions': () => {
        posted = true;
        return { decision: { id: 'd1' } };
      },
    });
    renderDialog();

    await pickOption(screen, 'Match kind', { value: 'glob' });
    // Clicked immediately after typing: well inside the 300ms debounce
    // window, before a preview for "*" has even been asked for, let alone
    // answered with its real (huge) total. Save must already be disabled
    // at this point — a disabled button does not fire a click at all.
    await user.type(screen.getByLabelText('Pattern'), '*');
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await settled();
    expect(posted).toBe(false);
    expect(screen.queryByRole('dialog', { name: 'Are you sure?' })).not.toBeInTheDocument();

    // The preview catches up once the debounce settles, Save re-enables,
    // and only then does the second confirmation (correctly) fire.
    await screen.findByText('Matches 9999 items');
    expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await declineConfirm(user)).toContain('9999');
    await settled();
    expect(posted).toBe(false);
  });

  // A failed preview used to be the one way out of the gate: previewStale
  // short-circuited on isError, and `capped` is undefined on an error, so
  // Save re-enabled AND the second confirmation never fired. With
  // retry: false on the query, one network blip while `*` is typed was
  // enough to save a fleet-wide pattern with no brake at all.
  it('keeps Save disabled when the preview itself failed — the count is the only brake there is', async () => {
    const user = userEvent.setup();
    let posted = false;
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () => new Response(JSON.stringify({ detail: 'preview unavailable' }), { status: 500 }),
      'POST /api/v1/orgs/acme/item-decisions': () => {
        posted = true;
        return { decision: { id: 'd1' } };
      },
    });
    renderDialog();

    await pickOption(screen, 'Match kind', { value: 'glob' });
    await user.type(screen.getByLabelText('Pattern'), '*');

    // The failure is surfaced — it is not silent — and Save stays
    // disabled behind it rather than falling open.
    expect(await screen.findByText(/preview unavailable/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled());
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await settled();
    expect(posted).toBe(false);
    expect(screen.queryByRole('dialog', { name: 'Are you sure?' })).not.toBeInTheDocument();
  });

  it('shows an organization-scoped watch stored outside this editor truthfully, and refuses to save it as is', async () => {
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () => ({ matches: [{ host_id: 'h1', check_key: 'disk', item: '/var' }], total: 1, capped: false }),
    });
    renderDialog({
      decision: {
        id: 'd-legacy',
        scope: 'org',
        check_key: 'disk',
        match_kind: 'exact',
        pattern: '/var',
        decision: 'watch',
        created_at: '',
        updated_at: '',
      },
    });

    // The true stored value is shown — "Watch", not a silent "Ignore" —
    // and Save is disabled until the operator resolves the mismatch
    // themselves (by picking Ignore, or by scoping it to a host). Select
    // shows a row's label rather than its raw value once closed (there is
    // no native .value to read any more), so the assertion reads the
    // rendered text instead of the option value.
    const verdict = screen.getByLabelText('Verdict') as HTMLInputElement;
    expect(verdict.value).toMatch(/^Watch/);
    expect(await screen.findByRole('alert')).toHaveTextContent(/stored as an organization-wide watch/i);
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  // items.ValidPattern counts BYTES; the field's maxLength counts UTF-16
  // code units. 512 multibyte characters pass the browser and are refused
  // by the server, so the dialog measures the pattern the way the server
  // will and says so before the round trip.
  it('refuses a pattern that is within the field\'s character cap but over the server\'s byte bound', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions/preview': () => ({ matches: [], total: 0, capped: false }),
    });
    renderDialog();

    // 300 three-byte characters: 300 UTF-16 code units (well under
    // maxLength=512) but 900 UTF-8 bytes.
    await user.click(screen.getByLabelText('Pattern'));
    await user.paste('あ'.repeat(300));

    expect(await screen.findByText(/900 bytes long; the limit is 512/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  it('gives the note field room rather than a tucked-away corner', () => {
    renderDialog();
    const note = screen.getByLabelText('Note') as HTMLTextAreaElement;
    expect(note.tagName).toBe('TEXTAREA');
  });
});
