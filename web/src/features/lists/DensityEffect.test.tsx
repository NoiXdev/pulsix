import { afterEach, describe, expect, it } from 'vitest';
import { render, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { DensityEffect } from './DensityEffect.tsx';

afterEach(() => {
  delete document.documentElement.dataset.density;
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
});
