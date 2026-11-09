import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ToastProvider } from './toast.tsx';
import { useToast } from './toastContext.ts';

function Trigger() {
  const { show } = useToast();
  return (
    <button type="button" onClick={() => show('saved')}>
      show
    </button>
  );
}

describe('ToastProvider', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('dismisses a toast after four seconds', () => {
    render(
      <ToastProvider>
        <Trigger />
      </ToastProvider>,
    );
    act(() => screen.getByRole('button', { name: 'show' }).click());
    expect(screen.getByRole('status')).toHaveTextContent('saved');
    act(() => vi.advanceTimersByTime(3999));
    expect(screen.getByRole('status')).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(1));
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  // A dismissal timer that outlives the provider updates state after it
  // unmounted; in a test run it fired after the environment was torn down
  // ("window is not defined") and failed CI.
  it('leaves no dismissal timer behind when it unmounts', () => {
    const { unmount } = render(
      <ToastProvider>
        <Trigger />
      </ToastProvider>,
    );
    act(() => screen.getByRole('button', { name: 'show' }).click());
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
