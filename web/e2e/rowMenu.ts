import type { Page } from '@playwright/test';

/** Opens the "…" menu of the list row named `rowName` and picks one entry. */
export async function chooseRowAction(page: Page, rowName: string, action: string): Promise<void> {
  await page.getByRole('button', { name: `Actions for ${rowName}`, exact: true }).click();
  await page.getByRole('menuitem', { name: action, exact: true }).click();
}
