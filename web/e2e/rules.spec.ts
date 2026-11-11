/**
 * The Rules tab (v0.3b-2 spec): an operator creates a form-mode rule, sees
 * it in the list, edits its threshold, and deletes it — the same journey
 * hosts.spec.ts already exercises for host administration, applied to the
 * rule editor.
 *
 * Org-scoped, on purpose: this spec is about the rule editor itself, not
 * about resolving a rule against a host, so it needs no agent and no check
 * definition — the fleet journey (fleet.spec.ts) is where a rule actually
 * judges a reported value. swap.used_pct is the metric because it is the
 * one numeric metric the seed set (internal/rules.SeedRules) leaves alone
 * — every assertion below is about a rule this spec created and will
 * delete, never about a row every organization already carries.
 */
import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { pickOption } from './pickOption.ts';
import { acceptConfirm } from './confirm.ts';
import { enCommon } from '../src/i18n/en/common.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const RULE_NAME = 'e2e-swap-warning';

test('an operator creates a form rule, sees it listed, edits its threshold, and deletes it', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/checks/rules`);
  await expect(page.getByRole('heading', { name: 'Checks' })).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Rules' })).toHaveAttribute('aria-selected', 'true');

  // --- Create --------------------------------------------------------
  await page.getByRole('button', { name: 'New rule' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(RULE_NAME);
  await pickOption(dialog, 'Metric', 'swap.used_pct');
  await pickOption(dialog, 'Severity', 'Warning');
  // "Applies to" defaults to "The whole organization" — no host to pick,
  // which is the whole point of testing the editor here rather than in
  // fleet.spec.ts.
  await expect(dialog.getByLabel('Applies to')).toHaveValue('The whole organization');
  await pickOption(dialog, 'Operator', { value: '>' });
  await dialog.getByLabel('Value').fill('50');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);

  // --- See it listed ---------------------------------------------------
  // The list shows name, severity, scope and duration, never the
  // threshold itself (backlog: RuleDTO's own row has no value column) —
  // The row menu's "Actions for …" names the rule, so it doubles as the
  // listing's own proof without a brittle row-by-CSS-class lookup.
  await expect(page.getByText(RULE_NAME)).toBeVisible();
  const rowActions = page.getByRole('button', { name: `Actions for ${RULE_NAME}` });
  await expect(rowActions).toBeVisible();

  // --- Edit its threshold ------------------------------------------------
  await rowActions.click();
  await page.getByRole('menuitem', { name: 'Edit' }).click();
  const editDialog = page.getByRole('dialog');
  await expect(editDialog.getByLabel('Value')).toHaveValue('50');
  await editDialog.getByLabel('Value').fill('75');
  await editDialog.getByRole('button', { name: 'Save' }).click();
  await expect(editDialog).toHaveCount(0);

  // The list itself renders no threshold column, so the edit is verified
  // the only way the GUI ever shows it back: reopening the same rule.
  await rowActions.click();
  await page.getByRole('menuitem', { name: 'Edit' }).click();
  await expect(page.getByRole('dialog').getByLabel('Value')).toHaveValue('75');
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);

  // --- Delete --------------------------------------------------------
  await rowActions.click();
  await page.getByRole('menuitem', { name: 'Delete…' }).click();
  await acceptConfirm(page, enCommon.delete);
  await expect(rowActions).toHaveCount(0);
  await expect(page.getByText(RULE_NAME)).toHaveCount(0);
});
