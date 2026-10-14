import { expect, type Locator, type Page } from '@playwright/test';

/**
 * Escapes a value for embedding inside a double-quoted CSS attribute
 * selector (`[attr="..."]`). Backslash first, so a value that already
 * contains one does not have its own escaping doubled up by the quote
 * escape that follows.
 */
function cssAttrValue(raw: string): string {
  return raw.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

/**
 * The Playwright half of src/test/pickOption.ts (v0.4g): choose an option
 * from a ui/Select. Replaces `locator.selectOption`, which only drives a
 * native select element.
 *
 * `scope` is a page or a dialog locator, exactly like the getByLabel calls
 * this replaced. `option` is the visible label, or `{ value }` for the
 * eight call sites that passed an option value and for lists where two
 * options legitimately share a label.
 *
 * Both forms match against a `data-*` attribute rather than Playwright's
 * own role-name matching: Select.tsx renders a row's hint as a second
 * <span> inside the same <li> as the label, so the row's accessible name
 * (name-from-content) is the label and the hint concatenated — searching
 * for the bare label with `getByRole('option', { name, exact: true })`
 * would find nothing on any hinted row, which is precisely the row a hint
 * exists to disambiguate. `data-label` carries the label alone. The value
 * form was already an attribute selector and needed no change.
 */
export async function pickOption(
  scope: Page | Locator,
  label: string,
  option: string | { value: string },
): Promise<void> {
  await scope.getByLabel(label, { exact: true }).click();
  const row =
    typeof option === 'string'
      ? scope.locator(`[role="option"][data-label="${cssAttrValue(option)}"]`)
      : scope.locator(`[role="option"][data-value="${option.value}"]`);
  await expect(row).toBeVisible();
  await row.click();
}
