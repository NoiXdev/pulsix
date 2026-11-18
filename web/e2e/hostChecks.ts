import { expect, type Page } from '@playwright/test';

/**
 * expandChecks opens every collapsed row of the host page's Checks table,
 * so a spec can reach the value rows (and their buttons) inside. The rows
 * are collapsed by default (only the first failing check opens itself), and
 * a row's open state survives the page's own refetches.
 */
export async function expandChecks(page: Page, timeout = 10_000): Promise<void> {
  const table = page.getByRole('table', { name: 'Checks of this host' });
  await expect(table).toBeVisible({ timeout });
  // The row toggles sit in each row's heading (other buttons inside an open
  // row, like Graph, carry aria-expanded too).
  const collapsed = table.locator('h3 button[aria-expanded="false"]');
  while ((await collapsed.count()) > 0) await collapsed.first().click();
}
