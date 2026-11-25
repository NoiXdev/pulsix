import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { TemplateDialog } from './TemplateDialog.tsx';

const playbookRow = { id: 'pb1', source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '' };

const template = (over: Record<string, unknown> = {}) => ({
  id: 'a', name: 'alpha', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, revision: 1,
  vars_mode: 'off', created_at: '', updated_at: '', follow_ups: [], agent_enrollment: false, ...over,
});

function renderDialog(canEnrolAgents: boolean, lang?: 'de') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({
    component: () => (
      <TemplateDialog
        org="acme"
        template={{ id: 'a', name: 'name' }}
        canReadCredentials={false}
        canCreateCredential={false}
        canEnrolAgents={canEnrolAgents}
        onClose={() => undefined}
      />
    ),
  });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ['/'] }) });
  const tree = (
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>
  );
  render(lang ? <I18nProvider locale={lang}>{tree}</I18nProvider> : tree);
}

function routes(current: ReturnType<typeof template>, onPut: (req: MockRequest) => unknown) {
  return {
    'GET /api/v1/orgs/acme/job-templates': { templates: [current] },
    'GET /api/v1/orgs/acme/job-templates/a': { template: current },
    'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
    'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
    'PUT /api/v1/orgs/acme/job-templates/a': onPut,
  };
}

describe('TemplateDialog agent enrollment (spec 2026-11-25 B)', () => {
  it('switches enrollment on and sends it, with help saying what Pulsix sets', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    const loaded = template();
    mockApi(routes(loaded, (req) => ((put = req), { template: loaded })));
    renderDialog(true);
    const dialog = await screen.findByRole('dialog', { name: 'alpha' });
    const toggle = await within(dialog).findByRole('switch', { name: 'Enrol agents' });
    expect(toggle).toHaveAttribute('aria-checked', 'false');
    expect(within(dialog).getByText(/pulsix_agent_enroll_token/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Agents permission/)).toBeInTheDocument();
    await user.click(toggle);
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template alpha saved');
    expect(put?.body).toMatchObject({ agent_enrollment: true });
  });

  it('shows the flag but holds the switch without the Agents permission, and leaves it out of the save', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    const loaded = template({ agent_enrollment: true });
    mockApi(routes(loaded, (req) => ((put = req), { template: loaded })));
    renderDialog(false);
    const dialog = await screen.findByRole('dialog', { name: 'alpha' });
    const toggle = await within(dialog).findByRole('switch', { name: 'Enrol agents' });
    expect(toggle).toHaveAttribute('aria-checked', 'true');
    expect(toggle).toBeDisabled();
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template alpha saved');
    expect(put?.body).not.toHaveProperty('agent_enrollment');
  });

  it('speaks German', async () => {
    mockApi(routes(template(), () => ({ template: template() })));
    renderDialog(true, 'de');
    const dialog = await screen.findByRole('dialog', { name: 'alpha' });
    expect(await within(dialog).findByRole('switch', { name: 'Agents registrieren' })).toBeInTheDocument();
    expect(within(dialog).getByText(/Agents-Berechtigung/)).toBeInTheDocument();
  });
});
