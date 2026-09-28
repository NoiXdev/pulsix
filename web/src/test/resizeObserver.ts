/**
 * ResizeObserver for jsdom, which ships none.
 *
 * ui/MetricChart.tsx follows its container's width with one (backlog
 * 3.26b): a panel that opens beside the chart, or a sidebar collapsing,
 * resizes it without any window resize event to listen for. Without a
 * constructor the component throws on mount, so the stub is installed for
 * the whole suite from src/test/setup.ts rather than per test file.
 *
 * It never fires on its own — jsdom has no layout, so there is nothing to
 * observe. A test drives it with `resizeTo`, which is the only way an
 * assertion about re-measuring can exist at all.
 */
type Watcher = { element: Element; callback: ResizeObserverCallback; observer: ResizeObserver };

const watchers: Watcher[] = [];

class StubResizeObserver implements ResizeObserver {
  constructor(private readonly callback: ResizeObserverCallback) {}

  observe(element: Element) {
    watchers.push({ element, callback: this.callback, observer: this });
  }

  unobserve(element: Element) {
    for (let i = watchers.length - 1; i >= 0; i--) {
      if (watchers[i].observer === this && watchers[i].element === element) watchers.splice(i, 1);
    }
  }

  disconnect() {
    for (let i = watchers.length - 1; i >= 0; i--) {
      if (watchers[i].observer === this) watchers.splice(i, 1);
    }
  }
}

(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = StubResizeObserver;

/**
 * resizeTo gives `element` a `clientWidth` — jsdom answers 0 for every
 * layout box — and notifies whoever is observing it, exactly as a browser
 * would after a layout that changed its size and no other event.
 *
 * It throws when nobody observes the element: a test asserting that a
 * resize was noticed should fail loudly when the component stopped
 * observing, not pass because the notification went nowhere.
 */
export function resizeTo(element: HTMLElement, width: number) {
  Object.defineProperty(element, 'clientWidth', { value: width, configurable: true });
  const observing = watchers.filter((w) => w.element === element);
  if (observing.length === 0) throw new Error('resizeTo: nothing observes this element');
  const entry = {
    target: element,
    contentRect: { width, height: element.clientHeight, top: 0, left: 0, bottom: 0, right: 0, x: 0, y: 0 },
  } as unknown as ResizeObserverEntry;
  for (const w of observing) w.callback([entry], w.observer);
}
