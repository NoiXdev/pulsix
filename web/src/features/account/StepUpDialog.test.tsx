import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { StepUpDialog } from './StepUpDialog.tsx';

vi.mock('../auth/webauthnCodec.ts', async (orig) => ({
  ...(await orig<typeof import('../auth/webauthnCodec.ts')>()),
  decodeRequestOptions: (o: unknown) => o,
  encodeAssertionCredential: () => ({ id: 'cred' }),
}));

describe('StepUpDialog passkey', () => {
  it('proves the passkey through the re-auth routes and never the login ones', async () => {
    Object.defineProperty(navigator, 'credentials', { value: { get: vi.fn().mockResolvedValue({ id: 'cred' }) }, configurable: true });
    Object.defineProperty(window, 'PublicKeyCredential', { value: class {}, configurable: true });
    const finish = vi.fn(() => ({}));
    mockApi({
      'POST /api/v1/auth/passkeys/reauth/begin': { ceremony_id: 'c1', options: { publicKey: { challenge: 'x' } } },
      'POST /api/v1/auth/passkeys/reauth/finish': finish,
    });
    const onProved = vi.fn();
    render(
      <QueryClientProvider client={new QueryClient()}>
        <StepUpDialog onClose={() => {}} onProved={onProved} />
      </QueryClientProvider>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Use a passkey' }));

    await waitFor(() => expect(onProved).toHaveBeenCalledTimes(1));
    expect(finish).toHaveBeenCalledWith(expect.objectContaining({ body: { ceremony_id: 'c1', response: { id: 'cred' } } }));
    const paths = fetchMock.mock.calls.map(([input]) => new URL((input as Request).url, 'http://localhost').pathname);
    expect(paths.some((p) => p.includes('/passkeys/login/'))).toBe(false);
    expect(paths).toContain('/api/v1/auth/passkeys/reauth/begin');
  });
});
