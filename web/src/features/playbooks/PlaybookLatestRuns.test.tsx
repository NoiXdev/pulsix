import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { PlaybookLatestRuns } from './PlaybookLatestRuns.tsx';

describe('PlaybookLatestRuns', () => {
  it('shows a job withdrawn at its claim as not run, and an ordinary error as error', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/jobs': {
        jobs: [
          { id: '0197c0a2-0000-7000-8000-000000000001', zone_id: 'z', kind: 'run', status: 'error', withdrawn_at_claim: true, created_at: '2026-09-15T08:00:00Z' },
          { id: '0197c0a2-0000-7000-8000-000000000002', zone_id: 'z', kind: 'run', status: 'error', created_at: '2026-09-15T08:00:00Z' },
        ],
      },
    });
    renderRoutes(
      [{ path: '/', component: () => <PlaybookLatestRuns org="acme" playbookId="p1" templates={[]} onShowAll={() => {}} /> }],
      '/o/acme/',
    );
    expect(await screen.findByText('Not run — approval withdrawn')).toBeInTheDocument();
    expect(screen.getByText('error')).toBeInTheDocument();
  });
});
