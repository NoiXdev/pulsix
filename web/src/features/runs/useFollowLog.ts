import { useCallback, useEffect, useRef, useState } from 'react';

/** FOLLOW_STORAGE_KEY remembers the run log's follow switch in this
 * browser ("on" or "off"); without a stored value the log follows. */
export const FOLLOW_STORAGE_KEY = 'pulsix.runLog.follow';

/** How far up (px) a scroll must move to count as the person reading back
 * rather than the browser settling after the log grew. */
const SCROLL_BACK_PX = 4;

function readStoredFollow(): boolean {
  try {
    return localStorage.getItem(FOLLOW_STORAGE_KEY) !== 'off';
  } catch {
    return true;
  }
}

function storeFollow(on: boolean): void {
  try {
    localStorage.setItem(FOLLOW_STORAGE_KEY, on ? 'on' : 'off');
  } catch {
    // Remembering the switch is a convenience; the switch still works.
  }
}

/** scrollParent is the element that scrolls `el` — the app shell's main
 * area on the run page — or the document when nothing above it does. */
function scrollParent(el: HTMLElement | null): HTMLElement | null {
  for (let p = el?.parentElement ?? null; p; p = p.parentElement) {
    const overflow = getComputedStyle(p).overflowY;
    if (overflow === 'auto' || overflow === 'scroll') return p;
  }
  return (document.scrollingElement as HTMLElement | null) ?? null;
}

/**
 * useFollowLog keeps a live run's event log in view (backlog 3.96): while
 * the run is live and `follow` is on, every new event (`count` grew)
 * scrolls the page's scroller to the bottom. Scrolling up turns following
 * off for this page, so reading back is never interrupted, but is not
 * remembered; the switch (`setFollow`) is, in this browser. Turning it on
 * jumps to the newest event. A finished run never scrolls on its own.
 * `logRef` goes on an element inside the log.
 */
export function useFollowLog({ live, count }: { live: boolean; count: number }) {
  const [follow, setFollowState] = useState(readStoredFollow);
  const logRef = useRef<HTMLDivElement>(null);
  const lastTop = useRef(0);

  useEffect(() => {
    if (!live || !follow) return;
    const scroller = scrollParent(logRef.current);
    if (!scroller) return;
    scroller.scrollTop = scroller.scrollHeight;
    lastTop.current = scroller.scrollTop;
  }, [live, follow, count]);

  useEffect(() => {
    if (!live) return;
    const scroller = scrollParent(logRef.current);
    if (!scroller) return;
    // The document's scroll events fire on window, not on its element.
    const target: HTMLElement | Window = scroller === document.scrollingElement ? window : scroller;
    lastTop.current = scroller.scrollTop;
    const onScroll = () => {
      const top = scroller.scrollTop;
      if (top < lastTop.current - SCROLL_BACK_PX) setFollowState(false);
      lastTop.current = top;
    };
    target.addEventListener('scroll', onScroll, { passive: true });
    return () => target.removeEventListener('scroll', onScroll);
  }, [live]);

  const setFollow = useCallback((on: boolean) => {
    setFollowState(on);
    storeFollow(on);
  }, []);

  return { follow, setFollow, logRef };
}
