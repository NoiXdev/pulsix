import { describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useRevisionDraft, type RevisionStep } from './useRevisionDraft.ts';

interface Content {
  v: string;
}

/** build marks what it built, so a test can tell a rebuilt draft from the
 * stored content and from an edit. */
const build = (stored: unknown) => ({ built: (stored as Content).v });

const A: Content = { v: 'a' };
const B: Content = { v: 'b' };
const C: Content = { v: 'c' };

interface Props {
  stored: unknown;
  revision: number;
  steps?: readonly RevisionStep[];
}

function setup(initial: Props) {
  return renderHook(({ stored, revision, steps }: Props) => useRevisionDraft(stored, revision, build, steps ?? []), { initialProps: initial });
}

describe('useRevisionDraft', () => {
  it('starts untouched from the stored content, on the stored revision', () => {
    const { result } = setup({ stored: A, revision: 3 });
    expect(result.current.draft).toEqual({ built: 'a' });
    expect(result.current.revision).toBe(3);
    expect(result.current.generation).toBe(0);
  });

  it('follows new content and revision while untouched', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    rerender({ stored: B, revision: 4 });
    expect(result.current.draft).toEqual({ built: 'b' });
    expect(result.current.revision).toBe(4);
    expect(result.current.generation).toBe(1);
  });

  it('pins the revision of the first edit', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    rerender({ stored: A, revision: 4 });
    expect(result.current.revision).toBe(3);
    act(() => result.current.edit({ built: 'mine again' }));
    expect(result.current.revision).toBe(3);

    rerender({ stored: B, revision: 5 });
    expect(result.current.draft).toEqual({ built: 'mine again' });
    expect(result.current.revision).toBe(3);
    expect(result.current.generation).toBe(0);
  });

  it('a save without a later edit rebuilds from the response', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    const ticket = result.current.startSave();
    act(() => result.current.saved(ticket, C, 5));
    expect(result.current.draft).toEqual({ built: 'c' });
    expect(result.current.revision).toBe(5);
    expect(result.current.generation).toBe(1);

    // Untouched again: it follows a newer revision of what it saved.
    rerender({ stored: C, revision: 6 });
    expect(result.current.revision).toBe(6);
    expect(result.current.draft).toEqual({ built: 'c' });
    expect(result.current.generation).toBe(1);
  });

  it('keeps the saved content while the group still shows what came before the save', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    const ticket = result.current.startSave();
    act(() => result.current.saved(ticket, C, 5));
    // The refetch after the save has not landed (or failed): the stale
    // content must not come back as the draft, on the saved revision.
    rerender({ stored: A, revision: 3 });
    expect(result.current.draft).toEqual({ built: 'c' });
    expect(result.current.revision).toBe(5);
  });

  it('an edit typed during the save stays, pinned to the response revision', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'first' }));
    const ticket = result.current.startSave();
    act(() => result.current.edit({ built: 'second' }));
    act(() => result.current.saved(ticket, C, 5));
    rerender({ stored: C, revision: 6 });
    expect(result.current.draft).toEqual({ built: 'second' });
    expect(result.current.revision).toBe(5);
    expect(result.current.generation).toBe(0);
  });

  it('reload with the same content keeps an edited draft on the reloaded revision', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    act(() => result.current.reloaded(A, 4));
    rerender({ stored: A, revision: 4 });
    expect(result.current.draft).toEqual({ built: 'mine' });
    expect(result.current.revision).toBe(4);
    expect(result.current.generation).toBe(0);

    // Still edited: a later revision does not move it.
    rerender({ stored: A, revision: 5 });
    expect(result.current.revision).toBe(4);
  });

  it('reload with other content starts over', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    rerender({ stored: B, revision: 4 });
    expect(result.current.draft).toEqual({ built: 'mine' });
    act(() => result.current.reloaded(B, 4));
    expect(result.current.draft).toEqual({ built: 'b' });
    expect(result.current.revision).toBe(4);
    expect(result.current.generation).toBe(1);

    // Untouched again: it follows the group.
    rerender({ stored: B, revision: 5 });
    expect(result.current.revision).toBe(5);
  });

  it('discard follows the group again', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    rerender({ stored: A, revision: 4 });
    expect(result.current.revision).toBe(3);
    act(() => result.current.discard());
    expect(result.current.draft).toEqual({ built: 'a' });
    expect(result.current.revision).toBe(4);
    expect(result.current.generation).toBe(1);

    rerender({ stored: B, revision: 5 });
    expect(result.current.draft).toEqual({ built: 'b' });
    expect(result.current.revision).toBe(5);
  });

  it('follows a chain of own steps from the pinned revision', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    rerender({
      stored: A,
      revision: 5,
      steps: [
        { from: 3, to: 4 },
        { from: 4, to: 5 },
      ],
    });
    expect(result.current.revision).toBe(5);
  });

  it('ignores own steps that do not start at the pinned revision', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    rerender({ stored: A, revision: 5, steps: [{ from: 4, to: 5 }] });
    expect(result.current.revision).toBe(3);
  });
});
