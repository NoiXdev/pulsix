import { describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useRevisionDraft } from './useRevisionDraft.ts';
import type { RevisionStep } from '../ownSteps.ts';

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

/** setup renders the draft with a hold spy whose releases it keeps, in
 * the order the holds were taken. */
function setup(initial: Props) {
  const releases: ReturnType<typeof vi.fn>[] = [];
  const hold = vi.fn((revision: number) => {
    void revision;
    const release = vi.fn();
    releases.push(release);
    return release;
  });
  const rendered = renderHook(({ stored, revision, steps }: Props) => useRevisionDraft(stored, revision, build, { steps: steps ?? [], hold }), {
    initialProps: initial,
  });
  return { ...rendered, hold, releases };
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

  it('rebuilds a saved draft from content someone else stored while the save was in flight', () => {
    const D: Content = { v: 'd' };
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    const ticket = result.current.startSave();
    // The group moves past the save before its answer lands: D at 5.
    rerender({ stored: D, revision: 5 });
    act(() => result.current.saved(ticket, C, 4));
    rerender({ stored: D, revision: 5 });
    expect(result.current.draft).toEqual({ built: 'd' });
    expect(result.current.revision).toBe(5);
  });

  it('rebuilds a saved draft when someone else stores the earlier content again', () => {
    const { result, rerender } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    const ticket = result.current.startSave();
    act(() => result.current.saved(ticket, C, 5));
    // The refetch after the save failed: the group still shows A at 3 ...
    rerender({ stored: A, revision: 3 });
    expect(result.current.draft).toEqual({ built: 'c' });
    // ... until someone else stores A again, at 6.
    rerender({ stored: A, revision: 6 });
    expect(result.current.draft).toEqual({ built: 'a' });
    expect(result.current.revision).toBe(6);
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

  it('a draft discarded during the save is rebuilt from the response', () => {
    const { result, rerender } = setup({ stored: B, revision: 3 });
    act(() => result.current.edit({ built: 'first' }));
    const ticket = result.current.startSave();
    act(() => result.current.edit({ built: 'second' }));
    // Discard while the save is in flight: rebuilt from B, untouched, on 3.
    act(() => result.current.discard());
    act(() => result.current.saved(ticket, C, 4));
    // The refetch brings what the save stored: the draft must not stay B.
    rerender({ stored: C, revision: 4 });
    expect(result.current.draft).toEqual({ built: 'c' });
    expect(result.current.revision).toBe(4);
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

  it('a save ticket names the draft it was started on', () => {
    const { result } = setup({ stored: A, revision: 3 });
    act(() => result.current.edit({ built: 'mine' }));
    expect(result.current.startSave()).toMatchObject({ generation: 0, revision: 3 });

    act(() => result.current.reloaded(B, 4));
    expect(result.current.startSave()).toMatchObject({ generation: 1, revision: 4 });
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

  it('holds its pinned revision while edited and releases it when rebuilt', () => {
    const { result, hold, releases } = setup({ stored: A, revision: 3 });
    expect(hold).not.toHaveBeenCalled();

    act(() => result.current.edit({ built: 'mine' }));
    expect(hold.mock.calls).toEqual([[3]]);

    const ticket = result.current.startSave();
    act(() => result.current.saved(ticket, C, 5));
    expect(releases[0]).toHaveBeenCalledOnce();
    expect(hold).toHaveBeenCalledOnce();
  });
});
