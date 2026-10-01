import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ItemDecisionDialog } from './ItemDecisionDialog.tsx';

const CHECK_KEYS = ['disk', 'service'];

function renderDialog(props: Partial<React.ComponentProps<typeof ItemDecisionDialog>> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ItemDecisionDialog org="acme" checkKeys={CHECK_KEYS} onClose={() => {}} {...props} />
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

    await user.selectOptions(screen.getByLabelText('Match kind'), 'glob');
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
    const verdictAtOrg = screen.getByLabelText('Verdict') as HTMLSelectElement;
    expect([...verdictAtOrg.options].map((o) => o.value)).toEqual(['ignore']);
    expect(screen.getByText(/watch is not offered at organization scope/i)).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText('Applies to'), 'host');
    await user.selectOptions(await screen.findByLabelText('Host'), 'h1');

    const verdictAtHost = screen.getByLabelText('Verdict') as HTMLSelectElement;
    expect([...verdictAtHost.options].map((o) => o.value)).toEqual(['ignore', 'watch']);
    expect(screen.queryByText(/watch is not offered at organization scope/i)).not.toBeInTheDocument();
  });

  it('demands a second confirmation before saving a pattern matching more than the preview can show in full, and does not save without it', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
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

    await user.selectOptions(screen.getByLabelText('Match kind'), 'glob');
    await user.type(screen.getByLabelText('Pattern'), '/mnt/*');
    await screen.findByText('Matches 350 items');

    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(confirmSpy.mock.calls[0][0]).toContain('350');
    expect(posted).toBeUndefined();

    confirmSpy.mockReturnValue(true);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toBeDefined());
    expect(confirmSpy).toHaveBeenCalledTimes(2);

    confirmSpy.mockRestore();
  });

  it('does not ask for a second confirmation when the pattern matches fewer items than the preview can show in full', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    const confirmSpy = vi.spyOn(window, 'confirm');
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
    expect(confirmSpy).not.toHaveBeenCalled();

    confirmSpy.mockRestore();
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
    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('a decision for this exact pattern already exists at this scope');
    expect(screen.getByRole('dialog', { name: 'New item decision' })).toBeInTheDocument();
  });

  it('gives the note field room rather than a tucked-away corner', () => {
    renderDialog();
    const note = screen.getByLabelText('Note') as HTMLTextAreaElement;
    expect(note.tagName).toBe('TEXTAREA');
  });
});
