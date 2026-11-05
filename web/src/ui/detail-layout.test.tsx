import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DetailLayout, DisabledHint, Section, SidebarFacts, SidebarHeading, type SectionNavItem } from './detail-layout.tsx';

const NAV: SectionNavItem[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'alerts', label: 'Alerts', count: 3, tone: 'crit' },
  { id: 'runs', label: 'Runs', count: 0 },
];

function renderLayout() {
  return render(
    <DetailLayout sidebar={<div>sidebar body</div>} nav={NAV} navLabel="Sections">
      <Section id="overview" title="Overview">
        overview body
      </Section>
      <Section id="alerts" title="Alerts" actions={<button type="button">Acknowledge</button>}>
        alerts body
      </Section>
      <Section id="runs" title="Runs">
        runs body
      </Section>
    </DetailLayout>,
  );
}

describe('DetailLayout', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('renders the sidebar and the sections', () => {
    renderLayout();
    expect(screen.getByText('sidebar body')).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Alerts' })).toHaveAttribute('id', 'alerts');
    expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeInTheDocument();
  });

  it('names the section nav so it is not confused with the shell nav', () => {
    renderLayout();
    expect(screen.getByRole('navigation', { name: 'Sections' })).toContainElement(screen.getByRole('link', { name: /Alerts/ }));
  });

  it('renders nav anchors with their counts', () => {
    renderLayout();
    const alerts = screen.getByRole('link', { name: /Alerts/ });
    expect(alerts).toHaveAttribute('href', '#alerts');
    expect(alerts).toHaveTextContent('3');
    expect(screen.getByRole('link', { name: /Overview/ })).toHaveAttribute('href', '#overview');
    // A zero count is shown, an absent one is not.
    expect(screen.getByRole('link', { name: /Runs/ })).toHaveTextContent('0');
    expect(screen.getByRole('link', { name: /Overview/ })).toHaveTextContent(/^Overview$/);
  });

  it('marks the first item current when IntersectionObserver is missing', () => {
    vi.stubGlobal('IntersectionObserver', undefined);
    renderLayout();
    expect(screen.getByRole('link', { name: /Overview/ })).toHaveAttribute('aria-current', 'location');
    expect(screen.getByRole('link', { name: /Alerts/ })).not.toHaveAttribute('aria-current');
  });

  it('falls back to the first item when the current one leaves the nav', () => {
    vi.stubGlobal('IntersectionObserver', undefined);
    const children = (
      <>
        <Section id="alerts" title="Alerts">a</Section>
        <Section id="runs" title="Runs">r</Section>
      </>
    );
    const { rerender } = renderLayout();
    expect(screen.getByRole('link', { name: /Overview/ })).toHaveAttribute('aria-current', 'location');
    // Sections drop out (an unmonitored host has no alerts): Overview is gone.
    rerender(<DetailLayout sidebar={<div>sidebar body</div>} nav={NAV.slice(1)} navLabel="Sections">{children}</DetailLayout>);
    expect(screen.queryByRole('link', { name: /Overview/ })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Alerts/ })).toHaveAttribute('aria-current', 'location');
    expect(screen.getByRole('link', { name: /Runs/ })).not.toHaveAttribute('aria-current');
  });

  it('falls back to the first remaining item when the observed current one is removed', () => {
    let callback: IntersectionObserverCallback = () => {};
    class FakeObserver {
      constructor(cb: IntersectionObserverCallback) {
        callback = cb;
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    vi.stubGlobal('IntersectionObserver', FakeObserver);
    const { rerender } = renderLayout();
    const runs = document.getElementById('runs') as HTMLElement;
    act(() => callback([{ target: runs, isIntersecting: true } as unknown as IntersectionObserverEntry], {} as IntersectionObserver));
    expect(screen.getByRole('link', { name: /Runs/ })).toHaveAttribute('aria-current', 'location');
    rerender(
      <DetailLayout sidebar={<div>sidebar body</div>} nav={NAV.slice(0, 2)} navLabel="Sections">
        <Section id="overview" title="Overview">o</Section>
        <Section id="alerts" title="Alerts">a</Section>
      </DetailLayout>,
    );
    expect(screen.getByRole('link', { name: /Overview/ })).toHaveAttribute('aria-current', 'location');
  });

  it('follows the section the observer reports as intersecting', () => {
    let callback: IntersectionObserverCallback = () => {};
    const observed: Element[] = [];
    const disconnect = vi.fn();
    class FakeObserver {
      constructor(cb: IntersectionObserverCallback) {
        callback = cb;
      }
      observe(el: Element) {
        observed.push(el);
      }
      unobserve() {}
      disconnect = disconnect;
    }
    vi.stubGlobal('IntersectionObserver', FakeObserver);

    const { unmount } = renderLayout();
    expect(observed.map((e) => e.id)).toEqual(['overview', 'alerts', 'runs']);

    const entry = (id: string, isIntersecting: boolean) =>
      ({ target: observed.find((e) => e.id === id), isIntersecting }) as unknown as IntersectionObserverEntry;
    act(() => callback([entry('runs', true)], {} as IntersectionObserver));
    expect(screen.getByRole('link', { name: /Runs/ })).toHaveAttribute('aria-current', 'location');
    expect(screen.getByRole('link', { name: /Overview/ })).not.toHaveAttribute('aria-current');

    // The topmost intersecting section wins when several are visible.
    act(() => callback([entry('alerts', true)], {} as IntersectionObserver));
    expect(screen.getByRole('link', { name: /Alerts/ })).toHaveAttribute('aria-current', 'location');

    unmount();
    expect(disconnect).toHaveBeenCalled();
  });
});

describe('DetailLayout hash scroll', () => {
  const original = Element.prototype.scrollIntoView;
  let scroll: Mock<(arg?: boolean | ScrollIntoViewOptions) => void>;
  let observe: (id: string) => void;

  beforeEach(() => {
    scroll = vi.fn<(arg?: boolean | ScrollIntoViewOptions) => void>();
    Element.prototype.scrollIntoView = scroll;
    // An observer the test drives: reports a section as the one in view.
    let callback: IntersectionObserverCallback = () => {};
    class FakeObserver {
      constructor(cb: IntersectionObserverCallback) {
        callback = cb;
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    vi.stubGlobal('IntersectionObserver', FakeObserver);
    observe = (id) =>
      act(() => callback([{ target: document.getElementById(id), isIntersecting: true } as unknown as IntersectionObserverEntry], {} as IntersectionObserver));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    Element.prototype.scrollIntoView = original;
    window.history.replaceState(null, '', '/');
  });

  const item = (id: string): SectionNavItem => ({ id, label: id });
  const layout = (ids: string[]) => (
    <DetailLayout sidebar={<div>s</div>} nav={ids.map(item)} navLabel="Sections">
      {ids.map((id) => (
        <Section key={id} id={id} title={id}>
          {id} body
        </Section>
      ))}
    </DetailLayout>
  );

  it('scrolls once to the section named by the hash when it appears', () => {
    window.history.replaceState(null, '', '/#b');

    const { rerender } = render(layout(['a']));
    expect(scroll).not.toHaveBeenCalled();

    rerender(layout(['a', 'b']));
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(scroll.mock.contexts[0]).toBe(document.getElementById('b'));
    expect(scroll).toHaveBeenCalledWith({ block: 'start' });
    expect(screen.getByRole('link', { name: 'b' })).toHaveAttribute('aria-current', 'location');

    rerender(layout(['a', 'b', 'c']));
    expect(scroll).toHaveBeenCalledTimes(1);
  });

  it('lets the observer take over from a deep link without scrolling again', () => {
    window.history.replaceState(null, '', '/#b');
    const { rerender } = render(layout(['a', 'b', 'c']));
    expect(scroll).toHaveBeenCalledTimes(1);

    observe('c');
    expect(screen.getByRole('link', { name: 'c' })).toHaveAttribute('aria-current', 'location');
    rerender(layout(['a', 'b', 'c']));
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('link', { name: 'c' })).toHaveAttribute('aria-current', 'location');
  });

  it('does not pull the page back to a section the user clicked in the nav', async () => {
    const user = userEvent.setup();
    render(layout(['a', 'b', 'c']));
    await user.click(screen.getByRole('link', { name: 'c' }));
    expect(window.location.hash).toBe('#c');

    // Scrolling on reports another section; the clicked hash must not win.
    observe('b');
    expect(scroll).not.toHaveBeenCalled();
    expect(screen.getByRole('link', { name: 'b' })).toHaveAttribute('aria-current', 'location');
    expect(screen.getByRole('link', { name: 'c' })).not.toHaveAttribute('aria-current');
  });

  it('does not scroll for a hash change the browser made itself', () => {
    render(layout(['a', 'b', 'c']));
    act(() => {
      window.location.hash = '#c';
    });
    observe('a');
    expect(scroll).not.toHaveBeenCalled();
    expect(screen.getByRole('link', { name: 'a' })).toHaveAttribute('aria-current', 'location');
  });
});

describe('SidebarHeading', () => {
  it('renders the title as the page heading with dot, pill and sub-line', () => {
    render(
      <SidebarHeading title="web-04" mono dot={{ tone: 'warn', label: '2 warnings' }} pill={<span>Warnings pill</span>}>
        deploy · web
      </SidebarHeading>,
    );
    const h1 = screen.getByRole('heading', { level: 1, name: 'web-04' });
    expect(h1.className).toContain('font-mono');
    expect(screen.getByTitle('2 warnings')).toHaveStyle({ background: 'var(--px-warn)' });
    expect(screen.getByText('Warnings pill')).toBeInTheDocument();
    expect(screen.getByText('deploy · web')).toBeInTheDocument();
  });

  it('is not monospaced by default and shows no dot without one', () => {
    render(<SidebarHeading title="Run 1" />);
    expect(screen.getByRole('heading', { level: 1 }).className).not.toContain('font-mono');
    expect(document.querySelector('[title]')).toBeNull();
  });
});

describe('SidebarFacts', () => {
  it('renders each label with its value', () => {
    render(<SidebarFacts items={[{ label: 'Address', value: '10.0.0.1' }, { label: 'OS', value: <b>Debian</b> }]} />);
    expect(screen.getByText('Address')).toBeInTheDocument();
    expect(screen.getByText('10.0.0.1')).toBeInTheDocument();
    expect(screen.getByText('Debian')).toBeInTheDocument();
  });
});

describe('DisabledHint', () => {
  it('renders title, body and action', () => {
    render(<DisabledHint title="No agent" body={<span>Install one first.</span>} action={<button type="button">Install</button>} />);
    expect(screen.getByText('No agent')).toBeInTheDocument();
    expect(screen.getByText('Install one first.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Install' })).toBeInTheDocument();
  });

  it('works without an action', () => {
    render(<DisabledHint title="No agent" body="Install one first." />);
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });
});
