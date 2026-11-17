import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { FOLLOW_STORAGE_KEY, useFollowLog } from './useFollowLog.ts';

/** A scroller whose content is `height` px tall in a 100 px viewport —
 * jsdom lays nothing out, so the sizes are set by hand. */
function makeScrollable(el: HTMLElement, height: number) {
  Object.defineProperty(el, 'scrollHeight', { configurable: true, value: height });
  Object.defineProperty(el, 'clientHeight', { configurable: true, value: 100 });
}

function Harness({ live, count, height }: { live: boolean; count: number; height: number }) {
  const { follow, setFollow, logRef } = useFollowLog({ live, count });
  return (
    <div data-testid="scroller" style={{ overflowY: 'auto' }} ref={(el) => {
        if (el) makeScrollable(el, height);
      }}>
      <button type="button" onClick={() => setFollow(!follow)}>
        {follow ? 'following' : 'not following'}
      </button>
      <div ref={logRef} />
    </div>
  );
}

beforeEach(() => localStorage.clear());
afterEach(() => localStorage.clear());

describe('useFollowLog', () => {
  it('scrolls to the newest event while the run is live', () => {
    const { rerender } = render(<Harness live count={1} height={500} />);
    const scroller = screen.getByTestId('scroller');
    rerender(<Harness live count={2} height={800} />);
    expect(scroller.scrollTop).toBe(800);
  });

  it('does not jump on a finished run', () => {
    const { rerender } = render(<Harness live={false} count={1} height={500} />);
    const scroller = screen.getByTestId('scroller');
    rerender(<Harness live={false} count={2} height={800} />);
    expect(scroller.scrollTop).toBe(0);
  });

  it('stops following once the person scrolls up, and does not remember that', () => {
    const { rerender } = render(<Harness live count={1} height={500} />);
    const scroller = screen.getByTestId('scroller');
    rerender(<Harness live count={2} height={800} />);
    expect(scroller.scrollTop).toBe(800);

    act(() => {
      scroller.scrollTop = 300;
      scroller.dispatchEvent(new Event('scroll'));
    });
    expect(screen.getByRole('button', { name: 'not following' })).toBeInTheDocument();
    rerender(<Harness live count={3} height={900} />);
    expect(scroller.scrollTop).toBe(300);
    expect(localStorage.getItem(FOLLOW_STORAGE_KEY)).toBeNull();
  });

  it('remembers the switch for the next run', () => {
    const first = render(<Harness live count={1} height={500} />);
    act(() => screen.getByRole('button', { name: 'following' }).click());
    expect(localStorage.getItem(FOLLOW_STORAGE_KEY)).toBe('off');
    first.unmount();

    render(<Harness live count={1} height={500} />);
    expect(screen.getByRole('button', { name: 'not following' })).toBeInTheDocument();
  });

  it('turning the switch back on jumps to the newest event', () => {
    localStorage.setItem(FOLLOW_STORAGE_KEY, 'off');
    render(<Harness live count={2} height={800} />);
    const scroller = screen.getByTestId('scroller');
    expect(scroller.scrollTop).toBe(0);
    act(() => screen.getByRole('button', { name: 'not following' }).click());
    expect(scroller.scrollTop).toBe(800);
    expect(localStorage.getItem(FOLLOW_STORAGE_KEY)).toBe('on');
  });

  it('works when storage is unavailable', () => {
    const original = Object.getOwnPropertyDescriptor(window, 'localStorage');
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      get() {
        throw new Error('blocked');
      },
    });
    try {
      render(<Harness live count={1} height={500} />);
      act(() => screen.getByRole('button', { name: 'following' }).click());
      expect(screen.getByRole('button', { name: 'not following' })).toBeInTheDocument();
    } finally {
      if (original) Object.defineProperty(window, 'localStorage', original);
    }
  });
});
