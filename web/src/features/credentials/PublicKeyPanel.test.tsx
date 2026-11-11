import { afterEach, describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ToastProvider } from '../../ui/toast.tsx';
import { PublicKeyPanel } from './PublicKeyPanel.tsx';

const original = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

afterEach(() => {
  if (original) Object.defineProperty(navigator, 'clipboard', original);
  else delete (navigator as { clipboard?: unknown }).clipboard;
});

describe('PublicKeyPanel copy button', () => {
  it('does not throw and says so when there is no Clipboard API (plain HTTP)', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    render(
      <ToastProvider>
        <PublicKeyPanel publicKey="ssh-ed25519 AAAA" />
      </ToastProvider>,
    );
    await user.click(screen.getByRole('button', { name: /copy/i }));
    expect(await screen.findByText(/Copying is not available here/)).toBeInTheDocument();
  });

  it('confirms with a toast when copying works', async () => {
    const user = userEvent.setup(); // installs a clipboard stub
    render(
      <ToastProvider>
        <PublicKeyPanel publicKey="ssh-ed25519 AAAA" />
      </ToastProvider>,
    );
    await user.click(screen.getByRole('button', { name: /copy/i }));
    expect(await screen.findByText('Copied')).toBeInTheDocument();
    expect(await navigator.clipboard.readText()).toBe('ssh-ed25519 AAAA');
  });
});
