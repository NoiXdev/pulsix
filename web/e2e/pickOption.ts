import { expect, type Locator, type Page } from '@playwright/test';

/**
 * The Playwright half of src/test/pickOption.ts (v0.4g): choose an option
 * from a ui/Select. Replaces `locator.selectOption`, which only drives a
 * native select element.
 *
 * `scope` is a page or a dialog locator, exactly like the getByLabel calls
 * this replaced. `option` is the visible label, or `{ value }` for the
 * eight call sites that passed an option value and for lists where two
 * options legitimately share a label.
 */
export async function pickOption(
  scope: Page | Locator,
  label: string,
  option: string | { value: string },
): Promise<void> {
  await scope.getByLabel(label, { exact: true }).click();
  const row =
    typeof option === 'string'
      ? scope.getByRole('option', { name: option, exact: true })
      : scope.locator(`[role="option"][data-value="${option.value}"]`);
  await expect(row).toBeVisible();
  await row.click();
}
