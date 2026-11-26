import { describe, expect, it } from 'vitest';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { fetchJobEvents } from './useJobs.ts';

/* The events route answers at most 1000 events a read (backlog: the job
 * log search). The run view still needs the whole log after a sequence
 * number, so fetchJobEvents reads page after page until one comes back
 * short. */

const event = (seq: number) => ({ seq, attempt: 1, at: '2026-11-26T08:00:00Z', host: 'web-01', task: 'Install', state: 'ok', payload: {} });
const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => event(from + i));

describe('fetchJobEvents', () => {
  it('pages through a log longer than one read', async () => {
    const reads: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/jobs/j1/events': (req: MockRequest) => {
        const after = Number(req.search.get('after') ?? '0');
        reads.push(`${after}/${req.search.get('limit')}`);
        const events = after === 5 ? range(6, 1005) : after === 1005 ? range(1006, 2005) : after === 2005 ? range(2006, 2008) : [];
        return { events, next_after: events.length > 0 ? events[events.length - 1].seq : after };
      },
    });
    const events = await fetchJobEvents('acme', 'j1', 5);
    expect(events).toHaveLength(2003);
    expect(events[0].seq).toBe(6);
    expect(events[events.length - 1].seq).toBe(2008);
    expect(reads).toEqual(['5/1000', '1005/1000', '2005/1000']);
  });

  it('reads once when the first page is short', async () => {
    let reads = 0;
    mockApi({
      'GET /api/v1/orgs/acme/jobs/j1/events': () => {
        reads++;
        return { events: [event(1), event(2)], next_after: 2 };
      },
    });
    expect((await fetchJobEvents('acme', 'j1', 0)).map((e) => e.seq)).toEqual([1, 2]);
    expect(reads).toBe(1);
  });
});
