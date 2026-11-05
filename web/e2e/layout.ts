import { expect, type Page } from '@playwright/test';

/**
 * expectNoClippedCards asserts that nothing on a detail page overflows its box
 * sideways at the viewport the test runs in (Playwright's default 1280x720).
 * It checks every `<Section>` and every `Card` (`data-card`): a card clips its
 * content (`overflow-clip`), and a section lets overflow through, so a
 * `scrollWidth` beyond the `clientWidth` means something is cut off or pokes
 * out. A box that scrolls on purpose (`overflow-x-auto`, a `<pre>`) keeps its
 * overflow to itself and does not trip this. jsdom has no layout, so only a
 * real browser can answer it.
 */
export async function expectNoClippedCards(page: Page): Promise<void> {
  const clipped = await page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>('section[id], [data-card]')]
      .filter((el) => el.scrollWidth > el.clientWidth + 1)
      .map((el) => `${el.tagName.toLowerCase()}${el.id ? `#${el.id}` : ''} ${el.scrollWidth}>${el.clientWidth}: ${(el.textContent ?? '').trim().slice(0, 40)}`),
  );
  expect(clipped).toEqual([]);
}
