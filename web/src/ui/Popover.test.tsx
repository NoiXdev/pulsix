import { useRef } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { Popover } from './Popover.tsx';
import { resizeTo } from '../test/resizeObserver.ts';

function Harness({ onClose }: { onClose: () => void }) {
  const anchor = useRef<HTMLButtonElement>(null);
  return (
    <div tabIndex={-1} data-testid="ancestor">
      <button ref={anchor} type="button">
        Field
      </button>
      <Popover anchor={anchor} onClose={onClose} label="Calendar">
        <button type="button">Inside</button>
      </Popover>
      <button type="button">Elsewhere</button>
    </div>
  );
}

const rect = (r: Partial<DOMRect>) => ({ top: 0, bottom: 0, left: 0, right: 0, width: 0, height: 0, x: 0, y: 0, toJSON: () => ({}), ...r }) as DOMRect;

const viewport = { width: window.innerWidth, height: window.innerHeight };
afterEach(() => {
  window.innerWidth = viewport.width;
  window.innerHeight = viewport.height;
});

describe('Popover', () => {
  it('closes when focus moves to another control on the page', () => {
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    fireEvent.blur(screen.getByRole('button', { name: 'Inside' }), { relatedTarget: screen.getByRole('button', { name: 'Elsewhere' }) });
    expect(onClose).toHaveBeenCalledWith('outside');
  });

  // Final review #5: a click on a focusable ancestor (a dialog's body)
  // moves focus there, but the press itself may have been inside the
  // popover — the mousedown listener decides, not the blur.
  it('stays open when focus moves to an ancestor of it', () => {
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    fireEvent.blur(screen.getByRole('button', { name: 'Inside' }), { relatedTarget: screen.getByTestId('ancestor') });
    expect(onClose).not.toHaveBeenCalled();
  });

  // Final review #7: the popover's own size can change with nothing else
  // happening (a six-week month), and it is placed again then.
  it('places itself again when its content changes size', () => {
    render(<Harness onClose={() => {}} />);
    const popover = screen.getByRole('dialog', { name: 'Calendar' });
    const field = screen.getByRole('button', { name: 'Field' });
    // A field near the bottom of a 600px viewport.
    vi.spyOn(field, 'getBoundingClientRect').mockReturnValue(rect({ top: 500, bottom: 532, left: 20, width: 200, height: 32 }));
    vi.spyOn(popover, 'getBoundingClientRect').mockReturnValue(rect({ width: 250, height: 300 }));
    window.innerHeight = 600;
    window.innerWidth = 1000;

    act(() => resizeTo(popover, 250));
    // No room below for 300px: it flips above the field.
    expect(popover.style.top).toBe('196px');
  });
});
