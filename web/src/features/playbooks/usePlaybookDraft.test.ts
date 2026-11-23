import { describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { usePlaybookDraft } from './usePlaybookDraft.ts';

function setup(initial: string, revision = 1) {
  return renderHook(({ stored, rev }: { stored: string; rev: number }) => usePlaybookDraft(stored, rev), { initialProps: { stored: initial, rev: revision } });
}

describe('usePlaybookDraft', () => {
  it('follows a newer stored text and its revision while clean', () => {
    const h = setup('a');
    h.rerender({ stored: 'b', rev: 2 });
    expect(h.result.current).toMatchObject({ saved: 'b', draft: 'b', revision: 2 });
  });

  it('follows a newer revision of the same text while clean', () => {
    const h = setup('a');
    h.rerender({ stored: 'a', rev: 2 });
    expect(h.result.current).toMatchObject({ saved: 'a', draft: 'a', revision: 2 });
  });

  it('keeps a dirty draft and its base revision when the stored text changes', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    h.rerender({ stored: 'b', rev: 2 });
    expect(h.result.current).toMatchObject({ saved: 'a', draft: 'mine', revision: 1 });
  });

  it('moves the base of a dirty draft to a newer revision of the same text', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    h.rerender({ stored: 'a', rev: 2 });
    expect(h.result.current).toMatchObject({ saved: 'a', draft: 'mine', revision: 2 });
  });

  it('takes the newer stored text and revision when a dirty draft is reverted by hand', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    h.rerender({ stored: 'b', rev: 2 });
    act(() => h.result.current.setDraft('a'));
    expect(h.result.current).toMatchObject({ saved: 'b', draft: 'b', revision: 2 });
  });

  it('keeps its own saved text and revision until the stored text catches up', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    act(() => h.result.current.markSaved('mine', 2));
    expect(h.result.current).toMatchObject({ saved: 'mine', draft: 'mine', revision: 2 });
    h.rerender({ stored: 'mine', rev: 2 });
    expect(h.result.current).toMatchObject({ saved: 'mine', draft: 'mine', revision: 2 });
  });

  it('keeps its own save over a stored text seen while it was dirty', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    h.rerender({ stored: 'b', rev: 2 });
    act(() => h.result.current.markSaved('mine', 3));
    expect(h.result.current).toMatchObject({ saved: 'mine', draft: 'mine', revision: 3 });
  });

  it('reloads: drops the draft for the newest stored text and its revision', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    h.rerender({ stored: 'b', rev: 2 });
    act(() => h.result.current.reload());
    expect(h.result.current).toMatchObject({ saved: 'b', draft: 'b', revision: 2 });
  });
});
