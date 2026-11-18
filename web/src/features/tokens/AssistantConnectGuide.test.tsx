import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
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
    await user.click(screen.getAllByRole('button', { name: 'Copy command' })[0]!);
    expect(copyText).toHaveBeenCalledWith(DOWNLOAD);
    await user.click(screen.getAllByRole('button', { name: 'Copy command' })[1]!);
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
});
