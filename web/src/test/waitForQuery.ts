import { expect } from 'vitest';
import { waitFor } from '@testing-library/react';
import type { QueryClient, QueryKey } from '@tanstack/react-query';

/**
 * Resolves once the query under `key` holds its answer. A test that asserts
 * something is absent because a gating query said so waits for this first:
 * the absence then comes from the answer, not from the answer being late.
 */
export async function waitForQuery(qc: QueryClient, key: QueryKey): Promise<void> {
  await waitFor(() => expect(qc.getQueryState(key)?.status).toBe('success'));
}
