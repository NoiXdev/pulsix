import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi } from '../../test/mockApi.ts';
import type { Template } from '../../api/types.ts';
import { existingOf } from './existing.ts';
import { TemplateForm } from './TemplateForm.tsx';
import { pageRoutes, template } from './templatePageTestKit.tsx';

function renderForm({ variant, canEnrolAgents, readOnly = false }: { variant: 'dialog' | 'page'; canEnrolAgents: boolean; readOnly?: boolean }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({
    component: () => (
      <TemplateForm
        org="acme"
        existing={existingOf(template() as unknown as Template)}
        playbooks={[]}
        credentials={[]}
        vaultCredentials={[]}
        canCreateCredential={false}
        canEnrolAgents={canEnrolAgents}
        variant={variant}
        readOnly={readOnly}
        onCancel={() => undefined}
        onDone={() => undefined}
      />
    ),
  });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ['/'] }) });
  render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

describe('TemplateForm variants', () => {
  it('leaves Delete and the variables link to the page in the page variant', async () => {
    mockApi(pageRoutes());
    renderForm({ variant: 'page', canEnrolAgents: true });
    const form = within(await screen.findByRole('form', { name: 'Template settings' }));
    expect(form.getByRole('button', { name: 'Save template' })).toBeInTheDocument();
    expect(form.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    expect(form.queryByRole('link', { name: 'Variables' })).not.toBeInTheDocument();
  });

  it('offers Delete and the variables link in the dialog variant', async () => {
    mockApi(pageRoutes());
    renderForm({ variant: 'dialog', canEnrolAgents: true });
    expect(await screen.findByRole('button', { name: 'Delete' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Variables' })).toBeInTheDocument();
  });

  it.each([
    [true, false, true],
    [false, false, false],
    [true, true, false],
  ])('enables the enrol switch in the page variant only with agents.write and while editing (agents.write %s, read-only %s)', async (canEnrolAgents, readOnly, enabled) => {
    mockApi(pageRoutes());
    renderForm({ variant: 'page', canEnrolAgents, readOnly });
    const toggle = await screen.findByRole('switch', { name: 'Enrol agents' });
    if (enabled) expect(toggle).toBeEnabled();
    else expect(toggle).toBeDisabled();
  });
});
