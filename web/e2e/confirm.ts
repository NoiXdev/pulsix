import { expect, type Page } from '@playwright/test';
import { enUi } from '../src/i18n/en/ui.ts';

/**
 * The Playwright half of src/test/confirmDialog.ts: answers the app's own
 * confirmation dialog (ui/confirm.tsx), which replaced the browser's
 * confirm() — so a `page.once('dialog', …)` handler never fires any more.
 *
 * The dialog is found by its accessible name, the catalogue's default
 * title, which keeps the confirm button apart from a same-named button in
 * a dialog it was asked from (TriggersDialog's and RepoDialog's own
 * "Delete"). `confirmLabel` is the label the call site passes
 * (t.common.delete, t.agents.revoke, …). Waits for the dialog to close, so
 * the next step does not race its exit.
 */
export async function acceptConfirm(page: Page, confirmLabel: string): Promise<void> {
  const dialog = page.getByRole('dialog', { name: enUi.confirm.title, exact: true });
  await dialog.getByRole('button', { name: confirmLabel, exact: true }).click();
  await expect(dialog).toHaveCount(0);
}
