import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import type { Preferences } from '../types.ts';
import { DEFAULT_PREFERENCES, preferencesQueryKey, usePreferences, useSavePreferences } from './usePreferences.ts';

const SAVED: Preferences = { density: 'compact', page_size: 50, lists: { runs: { columns: ['run', 'status'] } } };

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const { result } = renderHook(() => ({ read: usePreferences(), save: useSavePreferences() }), { wrapper });
  return { qc, result };
}

const failure = () => new Response(JSON.stringify({ detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } });

describe('usePreferences', () => {
  it('answers the defaults until the GET resolves', async () => {
    const get = deferred<unknown>();
    mockApi({ 'GET /api/v1/auth/me/preferences': () => get.promise });
    const { result } = setup();

    expect(result.current.read).toEqual({ prefs: DEFAULT_PREFERENCES, loaded: false, ready: false });
    expect(DEFAULT_PREFERENCES).toEqual({ density: 'standard', page_size: 25, lists: {} });

    get.resolve(SAVED);
    await waitFor(() => expect(result.current.read.loaded).toBe(true));
    expect(result.current.read.prefs).toEqual(SAVED);
    expect(result.current.read.ready).toBe(true);
  });

  it('is ready with the defaults when the GET fails', async () => {
    mockApi({ 'GET /api/v1/auth/me/preferences': failure });
    const { result } = setup();

    await waitFor(() => expect(result.current.read.ready).toBe(true));
    expect(result.current.read).toEqual({ prefs: DEFAULT_PREFERENCES, loaded: false, ready: true });
  });
});

/** Only the save hook, over a cache seeded with SAVED: no query observer
 * that would bring a removed query back. */
function setupSaveOnly() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(preferencesQueryKey, SAVED);
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const { result } = renderHook(() => useSavePreferences(), { wrapper });
  return { qc, save: result.current };
}

describe('useSavePreferences after the cache was emptied (a lock, a sign-out)', () => {
  it('fails instead of saving the defaults with the change', async () => {
    let puts = 0;
    mockApi({
      'PUT /api/v1/auth/me/preferences': (req: MockRequest) => {
        puts++;
        return req.body;
      },
    });
    const { qc, save } = setupSaveOnly();

    let outcome: unknown;
    await act(async () => {
      const saving = save((p) => ({ ...p, page_size: 10 })).catch((e: unknown) => e);
      qc.removeQueries({ queryKey: preferencesQueryKey });
      outcome = await saving;
    });

    expect(outcome).toBeInstanceOf(Error);
    expect(puts).toBe(0);
    expect(qc.getQueryState(preferencesQueryKey)).toBeUndefined();
  });

  it('does not bring the document back when a PUT fails afterwards', async () => {
    const put = deferred<Response>();
    let sent = false;
    mockApi({
      'PUT /api/v1/auth/me/preferences': () => {
        sent = true;
        return put.promise;
      },
    });
    const { qc, save } = setupSaveOnly();

    let saving!: Promise<unknown>;
    act(() => {
      saving = save((p) => ({ ...p, page_size: 10 })).catch((e: unknown) => e);
    });
    await waitFor(() => expect(sent).toBe(true));
    qc.removeQueries({ queryKey: preferencesQueryKey });
    put.resolve(failure());
    await act(async () => {
      await saving;
    });

    expect(qc.getQueryState(preferencesQueryKey)).toBeUndefined();
  });
});

describe('useSavePreferences', () => {
  it('updates the cache before the PUT resolves', async () => {
    const put = deferred<unknown>();
    const bodies: unknown[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': SAVED,
      'PUT /api/v1/auth/me/preferences': (req: MockRequest) => {
        bodies.push(req.body);
        return put.promise;
      },
    });
    const { result } = setup();
    await waitFor(() => expect(result.current.read.loaded).toBe(true));

    let saved!: Promise<Preferences>;
    act(() => {
      saved = result.current.save((p) => ({ ...p, density: 'comfortable' }));
    });

    await waitFor(() => expect(result.current.read.prefs.density).toBe('comfortable'));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toEqual({ ...SAVED, density: 'comfortable' });

    put.resolve({ ...SAVED, density: 'comfortable' });
    await act(async () => {
      await saved;
    });
    expect(result.current.read.prefs.density).toBe('comfortable');
  });

  it('rolls back a failed PUT', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': SAVED,
      'PUT /api/v1/auth/me/preferences': failure,
    });
    const { result } = setup();
    await waitFor(() => expect(result.current.read.loaded).toBe(true));

    let outcome: unknown;
    await act(async () => {
      outcome = await result.current.save((p) => ({ ...p, density: 'standard' })).catch((e: unknown) => e);
    });

    expect(outcome).toBeInstanceOf(Error);
    await waitFor(() => expect(result.current.read.prefs).toEqual(SAVED));
  });

  it('sends one PUT at a time, each with the latest document', async () => {
    const first = deferred<unknown>();
    const bodies: Preferences[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': SAVED,
      'PUT /api/v1/auth/me/preferences': (req: MockRequest) => {
        bodies.push(req.body as Preferences);
        return bodies.length === 1 ? first.promise : req.body;
      },
    });
    const { result } = setup();
    await waitFor(() => expect(result.current.read.loaded).toBe(true));

    let a!: Promise<Preferences>;
    let b!: Promise<Preferences>;
    act(() => {
      a = result.current.save((p) => ({ ...p, density: 'standard' }));
      b = result.current.save((p) => ({ ...p, page_size: 100 }));
    });
    await waitFor(() => expect(bodies).toHaveLength(1));
    // The second waits for the first to answer.
    await new Promise((r) => setTimeout(r, 20));
    expect(bodies).toHaveLength(1);

    first.resolve(bodies[0]);
    await act(async () => {
      await a;
      await b;
    });
    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toEqual({ ...SAVED, density: 'standard', page_size: 100 });
    await waitFor(() => expect(result.current.read.prefs).toEqual({ ...SAVED, density: 'standard', page_size: 100 }));
  });

  it('keeps a later change when an earlier PUT fails', async () => {
    const first = deferred<Response>();
    const bodies: Preferences[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': SAVED,
      'PUT /api/v1/auth/me/preferences': (req: MockRequest) => {
        bodies.push(req.body as Preferences);
        return bodies.length === 1 ? first.promise : req.body;
      },
    });
    const { result } = setup();
    await waitFor(() => expect(result.current.read.loaded).toBe(true));

    let a!: Promise<unknown>;
    let b!: Promise<Preferences>;
    act(() => {
      a = result.current.save((p) => ({ ...p, density: 'standard' })).catch((e: unknown) => e);
      b = result.current.save((p) => ({ ...p, page_size: 10 }));
    });
    await waitFor(() => expect(bodies).toHaveLength(1));
    first.resolve(failure());
    await act(async () => {
      await a;
      await b;
    });

    expect(bodies[1]).toEqual({ ...SAVED, page_size: 10 });
    await waitFor(() => expect(result.current.read.prefs).toEqual({ ...SAVED, page_size: 10 }));
  });

  it('waits for the saved document before a first save, so it never overwrites it with defaults', async () => {
    const get = deferred<unknown>();
    const bodies: Preferences[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': () => get.promise,
      'PUT /api/v1/auth/me/preferences': (req: MockRequest) => {
        bodies.push(req.body as Preferences);
        return req.body;
      },
    });
    const { qc, result } = setup();

    let saved!: Promise<Preferences>;
    act(() => {
      saved = result.current.save((p) => ({ ...p, lists: { ...p.lists, hosts: { columns: ['name'] } } }));
    });
    await new Promise((r) => setTimeout(r, 20));
    expect(bodies).toHaveLength(0);

    get.resolve(SAVED);
    await act(async () => {
      await saved;
    });
    expect(bodies).toEqual([{ ...SAVED, lists: { runs: { columns: ['run', 'status'] }, hosts: { columns: ['name'] } } }]);
    expect(qc.getQueryData(preferencesQueryKey)).toEqual(bodies[0]);
  });

  it('does not send the defaults when the saved document cannot be read', async () => {
    let puts = 0;
    mockApi({
      'GET /api/v1/auth/me/preferences': failure,
      'PUT /api/v1/auth/me/preferences': (req: MockRequest) => {
        puts++;
        return req.body;
      },
    });
    const { result } = setup();
    await waitFor(() => expect(result.current.read.loaded).toBe(false));

    let outcome: unknown;
    await act(async () => {
      outcome = await result.current.save((p) => ({ ...p, page_size: 10 })).catch((e: unknown) => e);
    });
    expect(outcome).toBeInstanceOf(Error);
    expect(puts).toBe(0);
  });
});

describe('preferences across tabs (backlog 3.104)', () => {
  it("refetches on window focus, so a save after another tab's change keeps both", async () => {
    // What the server holds; the "other tab" changes it behind our back.
    let server: Preferences = SAVED;
    const bodies: Preferences[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': () => server,
      'PUT /api/v1/auth/me/preferences': (req: MockRequest) => {
        bodies.push(req.body as Preferences);
        return req.body;
      },
    });
    const { result } = setup();
    await waitFor(() => expect(result.current.read.loaded).toBe(true));

    server = { ...SAVED, density: 'comfortable' };
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() => expect(result.current.read.prefs.density).toBe('comfortable'));

    await act(async () => {
      await result.current.save((p) => ({ ...p, page_size: 100 }));
    });
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toEqual({ ...SAVED, density: 'comfortable', page_size: 100 });
  });
});
