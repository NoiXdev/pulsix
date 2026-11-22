import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { usePlaybookOutline } from './usePlaybookOutline.ts';

const outline = (name: string) => ({ plays: [{ name, hosts: 'web', become: false, line: 1, tags: [], entries: [] }], tags: [], counts: { plays: 1, tasks: 0, handlers: 0 }, truncated: false });

const invalid = () => new Response(JSON.stringify({ status: 422, detail: 'malformed YAML' }), { status: 422, headers: { 'Content-Type': 'application/json' } });

function setup() {
  const sent: string[] = [];
  mockApi({
    'POST /api/v1/orgs/acme/playbooks/outline': (req: MockRequest) => {
      const content = (req.body as { content: string }).content;
      sent.push(content);
      return content.startsWith('bad') ? invalid() : outline(content);
    },
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return { sent, wrapper };
}

describe('usePlaybookOutline', () => {
  it('reads the outline of the first text at once', async () => {
    const { sent, wrapper } = setup();
    const { result } = renderHook(() => usePlaybookOutline('acme', 'one', true), { wrapper });
    await waitFor(() => expect(result.current.outline?.plays?.[0].name).toBe('one'));
    expect(sent).toEqual(['one']);
  });

  it('waits for a pause in typing and asks once for the last text', async () => {
    const { sent, wrapper } = setup();
    const { result, rerender } = renderHook(({ text }) => usePlaybookOutline('acme', text, true), { wrapper, initialProps: { text: 'one' } });
    await waitFor(() => expect(result.current.outline?.plays?.[0].name).toBe('one'));
    rerender({ text: 'two' });
    rerender({ text: 'three' });
    // The outline of the previous text stays while the next one is due.
    expect(result.current.outline?.plays?.[0].name).toBe('one');
    await waitFor(() => expect(result.current.outline?.plays?.[0].name).toBe('three'));
    expect(sent).toEqual(['one', 'three']);
  });

  it('keeps the last good outline while the text does not parse', async () => {
    const { sent, wrapper } = setup();
    const { result, rerender } = renderHook(({ text }) => usePlaybookOutline('acme', text, true), { wrapper, initialProps: { text: 'good' } });
    await waitFor(() => expect(result.current.outline?.plays?.[0].name).toBe('good'));
    rerender({ text: 'bad: [' });
    await waitFor(() => expect(sent).toContain('bad: ['));
    await waitFor(() => expect(result.current.invalid).toBe(true));
    expect(result.current.outline?.plays?.[0].name).toBe('good');
  });

  it('asks nothing when it is off (a repository playbook)', async () => {
    const { sent, wrapper } = setup();
    const { result } = renderHook(() => usePlaybookOutline('acme', '', false), { wrapper });
    await new Promise((r) => setTimeout(r, 450));
    expect(sent).toEqual([]);
    expect(result.current.outline).toBeNull();
  });
});
