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
  await dialog.getByLabel('Metric').selectOption({ label: 'swap.used_pct' });
  await dialog.getByLabel('Severity').selectOption({ label: 'Warning' });
  // "Applies to" defaults to "The whole organization" — no host to pick,
  // which is the whole point of testing the editor here rather than in
  // fleet.spec.ts.
  await expect(dialog.getByLabel('Applies to')).toHaveValue('org');
  await dialog.getByLabel('Operator').selectOption('>');
  await dialog.getByLabel('Value').fill('50');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);

  // --- See it listed ---------------------------------------------------
  // The list shows name, severity, scope and duration, never the
  // threshold itself (backlog: RuleDTO's own row has no value column) —
  // "Edit …" and "Delete …" name the rule, so they double as the listing's
  // own proof without a brittle row-by-CSS-class lookup.
  await expect(page.getByText(RULE_NAME)).toBeVisible();
  const editButton = page.getByRole('button', { name: `Edit ${RULE_NAME}` });
  const deleteButton = page.getByRole('button', { name: `Delete ${RULE_NAME}` });
  await expect(editButton).toBeVisible();
  await expect(deleteButton).toBeVisible();

  // --- Edit its threshold ------------------------------------------------
  await editButton.click();
  const editDialog = page.getByRole('dialog');
  await expect(editDialog.getByLabel('Value')).toHaveValue('50');
  await editDialog.getByLabel('Value').fill('75');
  await editDialog.getByRole('button', { name: 'Save' }).click();
  await expect(editDialog).toHaveCount(0);

  // The list itself renders no threshold column, so the edit is verified
  // the only way the GUI ever shows it back: reopening the same rule.
  await page.getByRole('button', { name: `Edit ${RULE_NAME}` }).click();
  await expect(page.getByRole('dialog').getByLabel('Value')).toHaveValue('75');
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);

  // --- Delete --------------------------------------------------------
  page.once('dialog', (d) => void d.accept());
  await page.getByRole('button', { name: `Delete ${RULE_NAME}` }).click();
  await expect(page.getByRole('button', { name: `Edit ${RULE_NAME}` })).toHaveCount(0);
  await expect(page.getByRole('button', { name: `Delete ${RULE_NAME}` })).toHaveCount(0);
  await expect(page.getByText(RULE_NAME)).toHaveCount(0);
});
