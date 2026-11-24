import { useRef } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render } from '@testing-library/react';
import { useAskOnEscape } from './useAskOnEscape.ts';

/** A modal <dialog> whose content uses the hook, with a field inside that
 * consumes Escape itself (a picker closing its list) and room for a nested
 * dialog. */
function Harness({ open = true, shouldAsk, ask, nested = false }: { open?: boolean; shouldAsk: boolean; ask: () => void; nested?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useAskOnEscape(ref, open, shouldAsk, ask);
  return (
    <dialog open={open}>
      <h2>Title</h2>
      <div ref={ref}>
        <input aria-label="Plain" />
        <input
          aria-label="Picker"
          onKeyDown={(e) => {
            if (e.key === 'Escape') e.preventDefault();
          }}
        />
        {nested && (
          <dialog open>
            <input aria-label="Nested" />
          </dialog>
        )}
      </div>
    </dialog>
  );
}

const escapeOn = (el: Element) => fireEvent.keyDown(el, { key: 'Escape' });

afterEach(() => {
  vi.restoreAllMocks();
});

describe('useAskOnEscape', () => {
  it('asks and prevents the close request on an Escape with the focus on <body>', () => {
    const ask = vi.fn();
    render(<Harness shouldAsk ask={ask} />);
    expect(escapeOn(document.body)).toBe(false);
    expect(ask).toHaveBeenCalledTimes(1);
  });

  it('asks on an Escape with the focus inside the content', () => {
    const ask = vi.fn();
    const { getByLabelText } = render(<Harness shouldAsk ask={ask} />);
    expect(escapeOn(getByLabelText('Plain'))).toBe(false);
    expect(ask).toHaveBeenCalledTimes(1);
  });

  it('leaves an Escape alone while there is nothing to ask about', () => {
    const ask = vi.fn();
    render(<Harness shouldAsk={false} ask={ask} />);
    expect(escapeOn(document.body)).toBe(true);
    expect(ask).not.toHaveBeenCalled();
  });

  it('leaves an Escape that a control inside has already taken to that control', () => {
    const ask = vi.fn();
    const { getByLabelText } = render(<Harness shouldAsk ask={ask} />);
    escapeOn(getByLabelText('Picker'));
    expect(ask).not.toHaveBeenCalled();
  });

  it('leaves an Escape to a dialog open above, the question included', () => {
    const ask = vi.fn();
    const { getByLabelText } = render(<Harness shouldAsk ask={ask} nested />);
    expect(escapeOn(getByLabelText('Nested'))).toBe(true);
    expect(escapeOn(document.body)).toBe(true);

    const question = document.createElement('dialog');
    question.setAttribute('open', '');
    document.body.appendChild(question);
    try {
      const { unmount } = render(<Harness shouldAsk ask={ask} />);
      expect(escapeOn(document.body)).toBe(true);
      unmount();
    } finally {
      question.remove();
    }
    expect(ask).not.toHaveBeenCalled();
  });

  it('ignores other keys', () => {
    const ask = vi.fn();
    render(<Harness shouldAsk ask={ask} />);
    fireEvent.keyDown(document.body, { key: 'Enter' });
    expect(ask).not.toHaveBeenCalled();
  });

  it('keeps no listener while the dialog is closed or once it is unmounted', () => {
    const add = vi.spyOn(document, 'addEventListener');
    const remove = vi.spyOn(document, 'removeEventListener');
    const ask = vi.fn();
    const { rerender, unmount } = render(<Harness shouldAsk ask={ask} />);
    const keydowns = (spy: typeof add) => spy.mock.calls.filter(([type]) => type === 'keydown').map(([, fn]) => fn);
    expect(keydowns(add)).toHaveLength(1);

    rerender(<Harness open={false} shouldAsk ask={ask} />);
    expect(keydowns(remove)).toEqual(keydowns(add));
    expect(escapeOn(document.body)).toBe(true);
    expect(ask).not.toHaveBeenCalled();

    rerender(<Harness shouldAsk ask={ask} />);
    expect(keydowns(add)).toHaveLength(2);
    unmount();
    expect(keydowns(remove)).toEqual(keydowns(add));
    expect(escapeOn(document.body)).toBe(true);
    expect(ask).not.toHaveBeenCalled();
  });

  it('asks with the latest callback without listening again', () => {
    const add = vi.spyOn(document, 'addEventListener');
    const first = vi.fn();
    const second = vi.fn();
    const { rerender } = render(<Harness shouldAsk={false} ask={first} />);
    rerender(<Harness shouldAsk ask={second} />);
    escapeOn(document.body);
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
    expect(add.mock.calls.filter(([type]) => type === 'keydown')).toHaveLength(1);
  });
});
