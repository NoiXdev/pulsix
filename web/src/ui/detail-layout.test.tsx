import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { DetailLayout, DisabledHint, Section, SidebarFacts, type SectionNavItem } from './detail-layout.tsx';

const NAV: SectionNavItem[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'alerts', label: 'Alerts', count: 3, tone: 'crit' },
  { id: 'runs', label: 'Runs', count: 0 },
];

function renderLayout() {
  return render(
    <DetailLayout sidebar={<div>sidebar body</div>} nav={NAV}>
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
