import { act } from '@testing-library/react';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunDetailPage } from './RunDetailPage.tsx';

/**
 * Shared fixtures for the run page's tests: an EventSource fake the live
 * log subscribes to, and a router with the run page and the runs list.
 */

/* A minimal EventSource fake: the hook subscribes with addEventListener and
 * the tests drive frames by hand. */
export class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static last(): FakeEventSource | undefined {
    return FakeEventSource.instances[FakeEventSource.instances.length - 1];
  }
  static reset(): void {
    FakeEventSource.instances = [];
  }
  url: string;
  closed = false;
  private listeners = new Map<string, ((e: { data: string }) => void)[]>();
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  addEventListener(type: string, fn: (e: { data: string }) => void) {
    const list = this.listeners.get(type) ?? [];
    list.push(fn);
    this.listeners.set(type, list);
  }
  emit(type: string, data: unknown) {
    for (const fn of this.listeners.get(type) ?? []) act(() => fn({ data: JSON.stringify(data) }));
  }
  close() {
    this.closed = true;
  }
}

/** Renders the run page at /o/acme/runs/<id>, next to a stub runs list. */
export function renderRunPage(id: string) {
  return renderRoutes(
    [
      { path: '/runs', component: () => <div>RUNS LIST</div> },
      { path: '/runs/$id', component: RunDetailPage },
    ],
    `/o/acme/runs/${id}`,
  );
}
