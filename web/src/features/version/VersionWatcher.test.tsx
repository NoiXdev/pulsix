import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { versionWatchQueryKey } from '../../api/hooks/useSetupStatus.ts';
import { VersionWatcher } from './VersionWatcher.tsx';
import { resetVersionBaseline } from './baseline.ts';

let current: string | Response;
let qc: QueryClient;
const reload = vi.fn();

function setVersion(v: string | Response) {
  current = v;
}

async function poll() {
  await act(async () => {
    await qc.refetchQueries({ queryKey: versionWatchQueryKey });
  });
}

function renderIt() {
  mockApi({
    'GET /api/v1/setup/status': () =>
      current instanceof Response ? current.clone() : { completed: true, version: current },
  });
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <VersionWatcher />
    </QueryClientProvider>,
  );
}

async function firstRead() {
  await waitFor(() => expect(qc.getQueryData(versionWatchQueryKey)).toBeTruthy());
}

beforeEach(() => {
  resetVersionBaseline();
  sessionStorage.clear();
  reload.mockReset();
  vi.stubGlobal('location', { ...window.location, reload });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('VersionWatcher', () => {
  it('shows nothing while the version stays the same', async () => {
    setVersion('0.1.0-rc.3');
    renderIt();
    await firstRead();
    await poll();
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('offers a reload with a release-notes link when the version changed', async () => {
    setVersion('0.1.0-rc.3');
    renderIt();
    await firstRead();
    setVersion('0.1.0-rc.4');
    await poll();
    const dialog = await screen.findByRole('dialog', { name: /updated to v0\.1\.0-rc\.4/i });
    const link = screen.getByRole('link', { name: /release notes/i });
    expect(link).toHaveAttribute('href', 'https://github.com/NoiXdev/pulsix/releases/tag/v0.1.0-rc.4');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    expect(dialog).toContainElement(link);
    await userEvent.click(screen.getByRole('button', { name: 'Reload' }));
    expect(reload).toHaveBeenCalledOnce();
  });

  it('gives a non-release version no link', async () => {
    setVersion('0.1.0-rc.3');
    renderIt();
    await firstRead();
    setVersion('local-abc123');
    await poll();
    await screen.findByRole('dialog');
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('Later leaves a banner and the dialog does not return on the next poll', async () => {
    setVersion('0.1.0-rc.3');
    renderIt();
    await firstRead();
    setVersion('0.1.0-rc.4');
    await poll();
    await userEvent.click(await screen.findByRole('button', { name: 'Later' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    const banner = screen.getByRole('status');
    await poll();
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(banner).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Reload' }));
    expect(reload).toHaveBeenCalledOnce();
  });

  it('shows the banner, not the dialog, when this version was already deferred', async () => {
    sessionStorage.setItem('pulsix.version.later', '0.1.0-rc.4');
    setVersion('0.1.0-rc.3');
    renderIt();
    await firstRead();
    setVersion('0.1.0-rc.4');
    await poll();
    await screen.findByRole('status');
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('ignores a failed read and keeps the remembered version', async () => {
    setVersion('0.1.0-rc.3');
    renderIt();
    await firstRead();
    setVersion(new Response('{}', { status: 502 }));
    await poll();
    expect(screen.queryByRole('dialog')).toBeNull();
    setVersion('0.1.0-rc.3');
    await poll();
    expect(screen.queryByRole('dialog')).toBeNull();
    setVersion('0.1.0-rc.4');
    await poll();
    await screen.findByRole('dialog');
  });

  it('works when sessionStorage throws', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    setVersion('0.1.0-rc.3');
    renderIt();
    await firstRead();
    setVersion('0.1.0-rc.4');
    await poll();
    await userEvent.click(await screen.findByRole('button', { name: 'Later' }));
    expect(screen.getByRole('status')).toBeInTheDocument();
    vi.restoreAllMocks();
  });
});
