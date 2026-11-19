import { describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { usePlaybookDraft } from './usePlaybookDraft.ts';

function setup(initial: string) {
  return renderHook(({ stored }: { stored: string }) => usePlaybookDraft(stored), { initialProps: { stored: initial } });
}

describe('usePlaybookDraft', () => {
  it('follows a newer stored text while clean', () => {
    const h = setup('a');
    h.rerender({ stored: 'b' });
    expect(h.result.current).toMatchObject({ saved: 'b', draft: 'b' });
  });

  it('keeps a dirty draft when the stored text changes', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    h.rerender({ stored: 'b' });
    expect(h.result.current).toMatchObject({ saved: 'a', draft: 'mine' });
  });

  it('takes the newer stored text when a dirty draft is reverted by hand', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    h.rerender({ stored: 'b' });
    act(() => h.result.current.setDraft('a'));
    expect(h.result.current).toMatchObject({ saved: 'b', draft: 'b' });
  });

  it('keeps its own saved text until the stored text catches up', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    act(() => h.result.current.markSaved('mine'));
    expect(h.result.current).toMatchObject({ saved: 'mine', draft: 'mine' });
    h.rerender({ stored: 'mine' });
    expect(h.result.current).toMatchObject({ saved: 'mine', draft: 'mine' });
  });

  it('keeps its own save over a stored text seen while it was dirty', () => {
    const h = setup('a');
    act(() => h.result.current.setDraft('mine'));
    h.rerender({ stored: 'b' });
    act(() => h.result.current.markSaved('mine'));
    expect(h.result.current).toMatchObject({ saved: 'mine', draft: 'mine' });
  });
});
