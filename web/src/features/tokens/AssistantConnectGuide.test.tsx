import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import userEvent from '@testing-library/user-event';
import { ToastProvider } from '../../ui/toast.tsx';
import { copyText } from '../../lib/clipboard.ts';
import { AssistantConnectGuide } from './AssistantConnectGuide.tsx';
import { suggestedName } from './assistantConnect.ts';

vi.mock('../../lib/clipboard.ts', () => ({ copyText: vi.fn().mockResolvedValue(true) }));

const ORIGIN = 'https://pulsix.example.com';
const DOWNLOAD =
  'mkdir -p ~/.local/bin && curl -fsSL https://pulsix.example.com/assistant-mcp.sh -o ~/.local/bin/assistant-mcp.sh && chmod +x ~/.local/bin/assistant-mcp.sh';

function show(props: Partial<React.ComponentProps<typeof AssistantConnectGuide>> = {}) {
  return render(
    <ToastProvider>
      <AssistantConnectGuide origin={ORIGIN} orgSlugs={['acme']} personal={false} showSecretStep {...props} />
    </ToastProvider>,
  );
}

beforeEach(() => vi.mocked(copyText).mockClear());

describe('suggestedName', () => {
  it.each([
    ['https://pulsix.example.com', 'acme', 'pulsix-acme'],
    ['https://Mon_Server.example.com:8443', 'beta', 'mon-server-beta'],
    ['http://192.168.1.5:8080', 'acme', 'pulsix-acme'],
    ['http://localhost:5173', 'acme', 'pulsix-acme'],
    ['http://[::1]:8080', 'acme', 'pulsix-acme'],
  ])('%s + %s -> %s', (origin, slug, want) => {
    expect(suggestedName(origin, slug)).toBe(want);
  });
});

describe('AssistantConnectGuide', () => {
  it('shows the commands of an organization token: one add without --org', () => {
    show();
    expect(screen.getByText(DOWNLOAD)).toBeInTheDocument();
    expect(screen.getByText('~/.local/bin/assistant-mcp.sh add pulsix-acme https://pulsix.example.com')).toBeInTheDocument();
    expect(screen.getByText('~/.local/bin/assistant-mcp.sh check pulsix-acme')).toBeInTheDocument();
    expect(screen.queryByText(/--org/)).not.toBeInTheDocument();
  });

  it('shows one add with --org and one check per granted organization for a personal token', () => {
    show({ personal: true, orgSlugs: ['acme', 'beta'] });
    expect(screen.getByText('~/.local/bin/assistant-mcp.sh add pulsix-acme https://pulsix.example.com --org acme')).toBeInTheDocument();
    expect(screen.getByText('~/.local/bin/assistant-mcp.sh add pulsix-beta https://pulsix.example.com --org beta')).toBeInTheDocument();
    expect(screen.getByText('~/.local/bin/assistant-mcp.sh check pulsix-acme')).toBeInTheDocument();
    expect(screen.getByText('~/.local/bin/assistant-mcp.sh check pulsix-beta')).toBeInTheDocument();
  });

  it('numbers four steps and states the requirements', () => {
    show();
    expect(within(screen.getByRole('list')).getAllByRole('listitem')).toHaveLength(4);
    expect(screen.getByText(/Needs macOS or Linux and Claude Code/)).toBeInTheDocument();
  });

  it('copies a command with its own button', async () => {
    const user = userEvent.setup();
    show();
    await user.click(screen.getByRole('button', { name: 'Copy command: download script' }));
    expect(copyText).toHaveBeenCalledWith(DOWNLOAD);
    await user.click(screen.getByRole('button', { name: 'Copy command: register pulsix-acme' }));
    expect(copyText).toHaveBeenLastCalledWith('~/.local/bin/assistant-mcp.sh add pulsix-acme https://pulsix.example.com');
  });

  it('points step 3 at the secret above, or at the saved token without it', () => {
    const { unmount } = show();
    expect(screen.getByText(/Copy button above/)).toBeInTheDocument();
    unmount();
    show({ showSecretStep: false });
    expect(screen.queryByText(/Copy button above/)).not.toBeInTheDocument();
    expect(screen.getByText(/saved when it was created/)).toBeInTheDocument();
  });

  it('shows a note instead of the commands when the address is HTTP', () => {
    show({ origin: 'http://pulsix.example.com' });
    expect(screen.getByText('The script needs Pulsix over HTTPS; this address uses HTTP.')).toBeInTheDocument();
    expect(screen.queryByRole('list')).not.toBeInTheDocument();
    expect(screen.queryByText(/assistant-mcp/)).not.toBeInTheDocument();
  });

  it('says so when a personal token works in no organization', () => {
    show({ personal: true, orgSlugs: [] });
    expect(screen.getByText('This token works in no organization right now.')).toBeInTheDocument();
    expect(screen.queryByRole('list')).not.toBeInTheDocument();
  });

  it('names Settings → General as plain text without a link', () => {
    show();
    expect(screen.getByText(/Settings → General/)).toBeInTheDocument();
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });

  it('links Settings → General once per organization the viewer may change', async () => {
    const root = createRootRoute();
    const page = createRoute({
      getParentRoute: () => root,
      path: '/',
      component: () => (
        <ToastProvider>
          <AssistantConnectGuide origin={ORIGIN} orgSlugs={['acme', 'beta', 'gamma']} personal showSecretStep settingsOrgs={['acme', 'gamma']} />
        </ToastProvider>
      ),
    });
    const general = createRoute({ getParentRoute: () => root, path: '/o/$org/settings/general', component: () => null });
    const router = createRouter({ routeTree: root.addChildren([page, general]), history: createMemoryHistory({ initialEntries: ['/'] }) });
    render(<RouterProvider router={router} />);
    const acme = await screen.findByRole('link', { name: 'Settings → General (acme)' });
    expect(acme).toHaveAttribute('href', '/o/acme/settings/general');
    expect(screen.getByRole('link', { name: 'Settings → General (gamma)' })).toHaveAttribute('href', '/o/gamma/settings/general');
    expect(screen.queryByRole('link', { name: /beta/ })).not.toBeInTheDocument();
  });
});
