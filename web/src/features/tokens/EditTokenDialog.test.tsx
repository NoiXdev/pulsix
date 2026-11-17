import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import type { components } from '../../api/schema.d.ts';
import { EditTokenDialog } from './EditTokenDialog.tsx';

type TokenRow = components['schemas']['TokenDTO'];

const apiToken: TokenRow = {
  id: 'tok1',
  name: 'ci-deploy',
  role: 'viewer',
  kind: 'api',
  created_at: '2026-01-01T00:00:00Z',
  expires_at: '2099-06-15T12:00:00Z',
};
const assistantToken: TokenRow = {
  id: 'tok2',
  name: 'helper',
  role: 'operator',
  kind: 'assistant',
  areas: ['inventory.read'],
  created_at: '2026-01-01T00:00:00Z',
};

function setup(token: TokenRow, patch: (req: MockRequest) => unknown) {
  const user = userEvent.setup();
  mockApi({
    'GET /api/v1/orgs/acme/tokens': { tokens: [token] },
    [`PATCH /api/v1/orgs/acme/tokens/${token.id}`]: patch,
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <EditTokenDialog org="acme" token={token} open onClose={() => {}} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return user;
}

const save = (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: 'Save' }));

describe('EditTokenDialog', () => {
  it('sends only changed fields', async () => {
    let body: unknown;
    const user = setup(apiToken, (req) => {
      body = req.body;
      return { token: { ...apiToken, name: 'renamed' } };
    });
    const name = screen.getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'renamed');
    await save(user);
    expect(await screen.findByText('Token renamed saved')).toBeInTheDocument();
    expect(body).toEqual({ name: 'renamed' });
  });

  it('sends an empty body for an untouched form and shows the server error', async () => {
    let body: unknown;
    const user = setup(apiToken, (req) => {
      body = req.body;
      return new Response(JSON.stringify({ detail: 'invalid input: nothing to change' }), { status: 422 });
    });
    await save(user);
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid input: nothing to change');
    expect(body).toEqual({});
  });

  it('clears the expiry', async () => {
    let body: unknown;
    const user = setup(apiToken, (req) => {
      body = req.body;
      return { token: { ...apiToken, expires_at: undefined } };
    });
    await user.click(screen.getByRole('checkbox', { name: 'Never expires' }));
    await save(user);
    expect(await screen.findByText('Token ci-deploy saved')).toBeInTheDocument();
    expect(body).toEqual({ never_expires: true });
  });

  it('sets a new expiry at the end of that local day', async () => {
    let body: { expires_at?: string } | undefined;
    const user = setup(apiToken, (req) => {
      body = req.body as { expires_at?: string };
      return { token: apiToken };
    });
    const date = screen.getByLabelText('Expires');
    await user.clear(date);
    await user.type(date, '2099-07-01');
    await save(user);
    await screen.findByText('Token ci-deploy saved');
    expect(body).toEqual({ expires_at: new Date(2099, 6, 1, 23, 59, 59, 999).toISOString() });
  });

  it('edits the areas of an assistant token', async () => {
    let body: unknown;
    const user = setup(assistantToken, (req) => {
      body = req.body;
      return { token: assistantToken };
    });
    expect(screen.getByRole('checkbox', { name: 'Inventory: read' })).toBeChecked();
    await user.click(screen.getByRole('checkbox', { name: 'Repositories & playbooks: read' }));
    await save(user);
    expect(await screen.findByText('Token helper saved. Reconnect the assistant to see new tools.')).toBeInTheDocument();
    expect(body).toEqual({ areas: ['inventory.read', 'repos.read'] });
  });

  it('shows no area grid for an api token', () => {
    setup(apiToken, () => ({}));
    expect(screen.queryByRole('checkbox', { name: 'Inventory: read' })).not.toBeInTheDocument();
    expect(screen.getByText('Kind and role cannot be changed; create a new token for that.')).toBeInTheDocument();
  });
});
