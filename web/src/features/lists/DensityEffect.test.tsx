import { afterEach, describe, expect, it } from 'vitest';
import { render, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { DensityEffect } from './DensityEffect.tsx';
import { readDensityHint, writeDensityHint } from './densityHint.ts';

afterEach(() => {
  delete document.documentElement.dataset.density;
  window.localStorage.clear();
});

function renderEffect() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DensityEffect />
    </QueryClientProvider>,
  );
}

describe('DensityEffect', () => {
  it('puts the saved density on the document', async () => {
    mockApi({ 'GET /api/v1/auth/me/preferences': { density: 'compact', page_size: 25, lists: {} } });
    renderEffect();

    await waitFor(() => expect(document.documentElement).toHaveAttribute('data-density', 'compact'));
  });

  it('removes it again when it unmounts', async () => {
    mockApi({ 'GET /api/v1/auth/me/preferences': { density: 'comfortable', page_size: 25, lists: {} } });
    const { unmount } = renderEffect();
    await waitFor(() => expect(document.documentElement).toHaveAttribute('data-density', 'comfortable'));

    unmount();
    expect(document.documentElement).not.toHaveAttribute('data-density');
  });

  it('keeps the hinted density until the preferences answer, then follows them and remembers them', async () => {
    writeDensityHint('compact');
    let answer!: (v: unknown) => void;
    mockApi({ 'GET /api/v1/auth/me/preferences': () => new Promise((r) => (answer = r)) });
    renderEffect();

    expect(document.documentElement).toHaveAttribute('data-density', 'compact');
    await new Promise((r) => setTimeout(r, 20));
    expect(document.documentElement).toHaveAttribute('data-density', 'compact');

    answer({ density: 'comfortable', page_size: 25, lists: {} });
    await waitFor(() => expect(document.documentElement).toHaveAttribute('data-density', 'comfortable'));
    expect(readDensityHint()).toBe('comfortable');
  });

  it('keeps the hint when the preferences cannot be read', async () => {
    writeDensityHint('compact');
    mockApi({ 'GET /api/v1/auth/me/preferences': () => new Response('{}', { status: 500 }) });
    renderEffect();

    await new Promise((r) => setTimeout(r, 20));
    expect(document.documentElement).toHaveAttribute('data-density', 'compact');
    expect(readDensityHint()).toBe('compact');
  });
});
