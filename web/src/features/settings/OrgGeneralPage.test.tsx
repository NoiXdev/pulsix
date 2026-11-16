import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { OrgGeneralPage } from './OrgGeneralPage.tsx';

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const generalRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/settings/general',
    component: OrgGeneralPage,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([generalRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/general'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <ConfirmProvider>
          <RouterProvider router={router} />
        </ConfirmProvider>
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const org = (role: string, name = 'Acme Hosting') => ({
  org: { id: 'o1', name, slug: 'acme', role, created_at: '2026-01-01T00:00:00Z' },
});

describe('OrgGeneralPage', () => {
  it('saves a changed name through PATCH and shows the new name', async () => {
    const user = userEvent.setup();
    let name = 'Acme Hosting';
    let patched: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': () => org('owner', name),
      'PATCH /api/v1/orgs/acme': (req: MockRequest) => {
        patched = req.body;
        name = (req.body as { name: string }).name;
        return org('owner', name);
      },
    });
    renderPage();
    const input = await screen.findByLabelText('Organization name');
    expect(input).toHaveValue('Acme Hosting');
    await user.clear(input);
    await user.type(input, 'Acme Cloud');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patched).toEqual({ name: 'Acme Cloud' }));
    await waitFor(() => expect(screen.getByLabelText('Organization name')).toHaveValue('Acme Cloud'));
  });

  // The save resolves only once the refetched organization is in the cache:
  // clearing the draft earlier would show the old name until the refetch
  // lands.
  it('never shows the old name again between a save and the refetched name', async () => {
    const user = userEvent.setup();
    let name = 'Acme Hosting';
    mockApi({
      'GET /api/v1/orgs/acme': async () => {
        const answer = org('owner', name);
        if (name !== 'Acme Hosting') await new Promise((r) => setTimeout(r, 150));
        return answer;
      },
      'PATCH /api/v1/orgs/acme': (req: MockRequest) => {
        name = (req.body as { name: string }).name;
        return org('owner', name);
      },
    });
    renderPage();
    const input = await screen.findByLabelText('Organization name');
    await user.clear(input);
    await user.type(input, 'Acme Cloud');

    const seen: string[] = [];
    const observer = new MutationObserver(() => {
      const field = screen.queryByLabelText<HTMLInputElement>('Organization name');
      if (field) seen.push(field.value);
    });
    observer.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true });
    await user.click(screen.getByRole('button', { name: 'Save' }));
    // The toast is raised right after the save resolves.
    await screen.findByText('Organization saved');
    expect((screen.getByLabelText('Organization name') as HTMLInputElement).value).toBe('Acme Cloud');
    await waitFor(() => expect(screen.getByLabelText('Organization name')).toHaveValue('Acme Cloud'));
    observer.disconnect();
    expect(seen).not.toContain('Acme Hosting');
  });

  it('shows the slug read-only', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org('owner') });
    renderPage();
    const slug = await screen.findByLabelText('Slug');
    expect(slug).toHaveValue('acme');
    expect(slug).toHaveAttribute('readonly');
  });

  it('shows the name read-only, with no Save button, to a role without org.manage', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org('operator') });
    renderPage();
    const input = await screen.findByLabelText('Organization name');
    expect(input).toHaveAttribute('readonly');
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
  });
  // Backlog 3.57: the organization's monitoring setting, beside its name.
  // The absent warning for client hosts fires after this many days without
  // contact; 0 switches it off; the server takes 0..365.
  describe('monitoring', () => {
    const LABEL = 'Warn after N days without contact (0 = off)';
    function routes(role: string, days = 14, onPut?: (req: MockRequest) => void) {
      let current = days;
      return {
        'GET /api/v1/orgs/acme': org(role),
        // The assistant card beside it loads too; its own tests are below.
        'GET /api/v1/orgs/acme/settings/assistant': { settings: { assistant_enabled: false } },
        'GET /api/v1/orgs/acme/settings/monitoring': () => ({ settings: { client_absent_days: current } }),
        'PUT /api/v1/orgs/acme/settings/monitoring': (req: MockRequest) => {
          onPut?.(req);
          current = (req.body as { client_absent_days: number }).client_absent_days;
          return { settings: { client_absent_days: current } };
        },
      };
    }

    it('shows the stored value and saves a new one', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', 14, (req) => puts.push(req.body)));
      renderPage();

      const input = await screen.findByLabelText(LABEL);
      await waitFor(() => expect(input).toHaveValue(14));
      await user.clear(input);
      await user.type(input, '0');
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));

      // The retention select was not touched, so retention_days is left
      // out and the server keeps the stored choice.
      await waitFor(() => expect(puts).toEqual([{ client_absent_days: 0 }]));
      expect(puts[0]).not.toHaveProperty('retention_days');
      expect(await screen.findByText('Monitoring settings saved')).toBeInTheDocument();
    });

    it.each([['366'], ['-1'], ['1.5'], ['']])('refuses %j without asking the server', async (typed) => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', 14, (req) => puts.push(req.body)));
      renderPage();

      const input = await screen.findByLabelText(LABEL);
      await waitFor(() => expect(input).toHaveValue(14));
      await user.clear(input);
      if (typed !== '') await user.type(input, typed);
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));

      // Announced, and tied to the field.
      expect(await screen.findByRole('alert')).toHaveTextContent('Enter a whole number of days from 0 to 365.');
      expect(screen.getByLabelText(LABEL)).toHaveAccessibleDescription(/Enter a whole number of days from 0 to 365\./);
      expect(puts).toEqual([]);
    });

    it('accepts both ends of the range', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('owner', 14, (req) => puts.push(req.body)));
      renderPage();

      const input = await screen.findByLabelText(LABEL);
      await waitFor(() => expect(input).toHaveValue(14));
      await user.clear(input);
      await user.type(input, '365');
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));
      await waitFor(() => expect(puts).toEqual([{ client_absent_days: 365 }]));
      expect(puts[0]).not.toHaveProperty('retention_days');
    });

    it('is read-only below admin', async () => {
      mockApi(routes('operator', 7));
      renderPage();

      const input = await screen.findByLabelText(LABEL);
      await waitFor(() => expect(input).toHaveValue(7));
      expect(input).toHaveAttribute('readonly');
      expect(screen.queryByRole('button', { name: 'Save monitoring settings' })).not.toBeInTheDocument();
      expect(screen.getByText('Only owners and admins can change the monitoring settings.')).toBeInTheDocument();
    });
  });

  // Backlog 3.15: how long this organization keeps measurements, capped by
  // the instance maximum the operator set.
  describe('measurement retention', () => {
    const SELECT = 'Keep measurements for';
    type Stored = { retention_days: number | null; retention_max_days: number; retention_effective_days: number };
    function routes(role: string, stored: Stored, puts: unknown[] = []) {
      let current = { client_absent_days: 14, ...stored };
      return {
        'GET /api/v1/orgs/acme': org(role),
        'GET /api/v1/orgs/acme/settings/monitoring': () => ({ settings: current }),
        'PUT /api/v1/orgs/acme/settings/monitoring': (req: MockRequest) => {
          puts.push(req.body);
          const b = req.body as { client_absent_days: number; retention_days?: number | null };
          // The server keeps the stored choice when retention_days is left out.
          const choice = 'retention_days' in b ? (b.retention_days ?? null) : current.retention_days;
          const eff = Math.min(choice ?? current.retention_max_days, current.retention_max_days);
          current = { ...current, client_absent_days: b.client_absent_days, retention_days: choice, retention_effective_days: eff };
          return { settings: current };
        },
      };
    }
    const DEFAULT90: Stored = { retention_days: null, retention_max_days: 90, retention_effective_days: 90 };

    it('offers the instance default and the three choices, disabling those above the maximum', async () => {
      const user = userEvent.setup();
      mockApi(routes('admin', DEFAULT90));
      renderPage();

      const box = await screen.findByRole('combobox', { name: SELECT });
      await waitFor(() => expect(box).toHaveValue('Instance default (90 days)'));
      await user.click(box);
      const options = screen.getAllByRole('option');
      expect(options.map((o) => o.textContent)).toEqual([
        expect.stringContaining('Instance default (90 days)'),
        expect.stringContaining('30 days'),
        expect.stringContaining('90 days'),
        expect.stringContaining('365 days'),
      ]);
      expect(options[1]).not.toHaveAttribute('aria-disabled', 'true');
      expect(options[2]).not.toHaveAttribute('aria-disabled', 'true');
      expect(options[3]).toHaveAttribute('aria-disabled', 'true');
      expect(options[3]).toHaveTextContent('The instance keeps at most 90 days');
    });

    it('leaves the stored choice alone when only the absent days change', async () => {
      // Another admin may have changed the retention since this page was
      // loaded; a save that did not touch the select must not send it back.
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', { retention_days: 30, retention_max_days: 365, retention_effective_days: 30 }, puts));
      renderPage();

      const input = await screen.findByLabelText('Warn after N days without contact (0 = off)');
      await waitFor(() => expect(input).toHaveValue(14));
      await user.clear(input);
      await user.type(input, '7');
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));

      await waitFor(() => expect(puts).toHaveLength(1));
      expect(puts[0]).toEqual({ client_absent_days: 7 });
      expect(puts[0]).not.toHaveProperty('retention_days');
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
    });

    it('saves a choice together with the absent days and sends null for the default', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', { retention_days: 90, retention_max_days: 365, retention_effective_days: 90 }, puts));
      renderPage();

      const box = await screen.findByRole('combobox', { name: SELECT });
      await waitFor(() => expect(box).toHaveValue('90 days'));
      await user.click(box);
      await user.click(screen.getByRole('option', { name: /^365 days/ }));
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));
      await waitFor(() => expect(puts).toEqual([{ client_absent_days: 14, retention_days: 365 }]));

      await user.click(await screen.findByRole('combobox', { name: SELECT }));
      await user.click(screen.getByRole('option', { name: /Instance default/ }));
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));
      await waitFor(() => expect(puts[1]).toEqual({ client_absent_days: 14, retention_days: null }));
    });

    it('warns before lowering the effective retention and saves only after confirming', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', { retention_days: 365, retention_max_days: 365, retention_effective_days: 365 }, puts));
      renderPage();

      const box = await screen.findByRole('combobox', { name: SELECT });
      await waitFor(() => expect(box).toHaveValue('365 days'));
      await user.click(box);
      await user.click(screen.getByRole('option', { name: /^30 days/ }));
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));

      expect(await screen.findByText('Measurements and state history older than 30 days will be deleted within the hour.')).toBeInTheDocument();
      expect(puts).toEqual([]);
      await user.click(screen.getByRole('button', { name: 'Confirm' }));
      await waitFor(() => expect(puts).toEqual([{ client_absent_days: 14, retention_days: 30 }]));
    });

    it('warns when lowering from the instance default to a shorter choice', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', { retention_days: null, retention_max_days: 365, retention_effective_days: 365 }, puts));
      renderPage();

      const box = await screen.findByRole('combobox', { name: SELECT });
      await waitFor(() => expect(box).toHaveValue('Instance default (365 days)'));
      await user.click(box);
      await user.click(screen.getByRole('option', { name: /^30 days/ }));
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));

      expect(await screen.findByText('Measurements and state history older than 30 days will be deleted within the hour.')).toBeInTheDocument();
      expect(puts).toEqual([]);
      await user.click(screen.getByRole('button', { name: 'Confirm' }));
      await waitFor(() => expect(puts).toEqual([{ client_absent_days: 14, retention_days: 30 }]));
    });

    it('saves nothing when the warning is cancelled', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', { retention_days: 365, retention_max_days: 365, retention_effective_days: 365 }, puts));
      renderPage();

      const box = await screen.findByRole('combobox', { name: SELECT });
      await waitFor(() => expect(box).toHaveValue('365 days'));
      await user.click(box);
      await user.click(screen.getByRole('option', { name: /^30 days/ }));
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));
      await user.click(await screen.findByRole('button', { name: 'Cancel' }));
      expect(puts).toEqual([]);
    });

    it('does not warn when the effective value stays or grows', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      // Choosing 365 under a 90-day maximum is not offered; choosing the
      // default while the choice was 30 grows 30 to 90.
      mockApi(routes('admin', { retention_days: 30, retention_max_days: 90, retention_effective_days: 30 }, puts));
      renderPage();

      const box = await screen.findByRole('combobox', { name: SELECT });
      await waitFor(() => expect(box).toHaveValue('30 days'));
      await user.click(box);
      await user.click(screen.getByRole('option', { name: /Instance default/ }));
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));
      await waitFor(() => expect(puts).toEqual([{ client_absent_days: 14, retention_days: null }]));
    });

    it('is read-only below admin', async () => {
      mockApi(routes('operator', DEFAULT90));
      renderPage();
      const box = await screen.findByRole('combobox', { name: SELECT });
      await waitFor(() => expect(box).toHaveValue('Instance default (90 days)'));
      expect(box).toBeDisabled();
    });
  });

  // Assistant access (spec 2026-11-16): the per-organization switch that
  // lets assistant tokens work at all; off by default, admins change it.
  describe('assistant access', () => {
    function routes(role: string, enabled: boolean, onPut?: (req: MockRequest) => void) {
      let current = enabled;
      let followUps = false;
      return {
        'GET /api/v1/orgs/acme': org(role),
        'GET /api/v1/orgs/acme/settings/assistant': () => ({
          settings: { assistant_enabled: current, approved_runs_start_follow_ups: followUps },
        }),
        'PUT /api/v1/orgs/acme/settings/assistant': (req: MockRequest) => {
          onPut?.(req);
          const body = req.body as { assistant_enabled: boolean; approved_runs_start_follow_ups?: boolean };
          current = body.assistant_enabled;
          followUps = body.approved_runs_start_follow_ups ?? followUps;
          return { settings: { assistant_enabled: current, approved_runs_start_follow_ups: followUps } };
        },
      };
    }

    it('turns assistant access on for an admin', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', false, (req) => puts.push(req.body)));
      renderPage();
      const toggle = await screen.findByRole('switch', { name: 'Assistant access' });
      await waitFor(() => expect(toggle).toBeEnabled());
      expect(toggle).toHaveAttribute('aria-checked', 'false');
      expect(screen.getByText(/Assistant tokens of this organization can work within their areas/)).toBeInTheDocument();
      await user.click(toggle);
      await waitFor(() => expect(puts).toEqual([{ assistant_enabled: true }]));
      await waitFor(() => expect(screen.getByRole('switch', { name: 'Assistant access' })).toHaveAttribute('aria-checked', 'true'));
    });

    it('switches follow-ups for approved runs, keeping the access value', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', true, (req) => puts.push(req.body)));
      renderPage();
      const toggle = await screen.findByRole('switch', { name: 'Approved runs start follow-ups' });
      await waitFor(() => expect(toggle).toBeEnabled());
      expect(toggle).toHaveAttribute('aria-checked', 'false');
      expect(screen.getByText(/When on, the approval page shows the template's follow-ups/)).toBeInTheDocument();
      await user.click(toggle);
      await waitFor(() => expect(puts).toEqual([{ assistant_enabled: true, approved_runs_start_follow_ups: true }]));
      await waitFor(() =>
        expect(screen.getByRole('switch', { name: 'Approved runs start follow-ups' })).toHaveAttribute('aria-checked', 'true'),
      );
    });

    it('is read-only below admin', async () => {
      mockApi(routes('operator', true));
      renderPage();
      expect(await screen.findByRole('switch', { name: 'Approved runs start follow-ups' })).toBeDisabled();
      const toggle = await screen.findByRole('switch', { name: 'Assistant access' });
      await waitFor(() => expect(toggle).toHaveAttribute('aria-checked', 'true'));
      expect(toggle).toBeDisabled();
      expect(screen.getByText('Only owners and admins can change assistant access.')).toBeInTheDocument();
    });
  });
});
