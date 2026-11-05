/**
 * The central variable editor (backlog 7.75), the browser half: an editor
 * playbook declares two variables (a vars_prompt and a marked play
 * variable), a job template links its variables to it through the preview,
 * and the template's variables page shows where each field comes from.
 * Switching a field inactive takes it out of the values form; editing a
 * field read from the playbook makes it "Adjusted", and "Reset to source"
 * makes it "From playbook" again. An inventory group's schema stays manual:
 * "Add field" adds a field of its own.
 *
 * An editor playbook is read when it is saved, so none of this needs a
 * worker (the Go scenario internal/e2e/variables_test.go covers the
 * repository sync and the automatic apply).
 */
import { test, expect, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { pickOption } from './pickOption.ts';
import { expectNoClippedCards } from './layout.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const base = `/o/${ORG.slug}`;
const TEMPLATE = 'e2e-vars-template';
const GROUP = 'e2e_vars_group';

const PLAYBOOK = `- name: Pulsix variables e2e
  hosts: all
  gather_facts: false
  vars_prompt:
    - name: admin_user
      prompt: "Admin user"
      private: false
  vars:
    http_port: 8080  # pulsix: required HTTP port
  tasks:
    - name: Pulsix variables e2e
      ansible.builtin.debug:
        msg: "{{ admin_user }} on {{ http_port }}"
`;

async function toast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.getByText(text).first()).toBeVisible();
}

/** fieldRow is the field list's row of a top-level field. */
function fieldRow(page: Page, name: string) {
  return page.getByRole('region', { name: 'Fields' }).getByRole('row').filter({ has: page.getByRole('switch', { name: `${name} active` }) });
}

test('a template takes its variables from the playbook, and a group keeps manual fields', async ({ page }) => {
  // --- The editor playbook and a template running it --------------------
  await page.goto(`${base}/playbooks`);
  await page.getByRole('button', { name: 'New editor playbook' }).click();
  let dialog = page.getByRole('dialog', { name: 'New editor playbook' });
  await dialog.getByLabel('Playbook YAML', { exact: false }).fill(PLAYBOOK);
  await dialog.getByRole('button', { name: 'Save playbook' }).click();
  await toast(page, /Playbook (created|saved)/);

  await page.goto(`${base}/templates`);
  await page.getByRole('button', { name: 'Add template' }).click();
  dialog = page.getByRole('dialog', { name: 'New job template' });
  await dialog.getByLabel('Name').fill(TEMPLATE);
  await pickOption(dialog, 'Playbook', 'Pulsix variables e2e');
  await dialog.getByRole('button', { name: 'Create template' }).click();
  await toast(page, `Template ${TEMPLATE} created`);

  // --- Link: Manual opens the preview with both fields added ------------
  await page.getByRole('link', { name: `Variables of ${TEMPLATE}` }).click();
  await expect(page.getByRole('heading', { name: `Variables of ${TEMPLATE}` })).toBeVisible();
  // Linking goes through the preview: the radio stays on Off until the
  // apply links the variables.
  await page.getByRole('radiogroup', { name: 'Sync mode' }).getByRole('radio', { name: 'Manual' }).click();
  const preview = page.getByRole('dialog', { name: 'Sync with the playbook' });
  const added = preview.getByRole('region', { name: 'Added' });
  await expect(added.getByRole('listitem')).toHaveCount(2);
  // The default activation: a required field without a default is active,
  // everything else (http_port has its play value as default) inactive.
  await expect(added.getByRole('listitem').filter({ hasText: 'admin_user' })).toContainText('active');
  await expect(added.getByRole('listitem').filter({ hasText: 'http_port' })).toContainText('inactive');
  // A new required field without a default asks for its value here.
  await preview.getByLabel('Admin user', { exact: false }).fill('ops');
  await preview.getByRole('button', { name: 'Apply' }).click();
  await toast(page, 'Variables synced with the playbook');
  await expect(preview).toHaveCount(0);
  await expect(page.getByRole('radio', { name: 'Manual' })).toBeChecked();

  await expect(fieldRow(page, 'admin_user')).toContainText('From playbook');
  await expect(fieldRow(page, 'http_port')).toContainText('From playbook');

  // --- The active switch decides what the values form shows -------------
  await page.getByRole('switch', { name: 'admin_user active' }).click();
  await page.getByRole('switch', { name: 'http_port active' }).click();
  const values = page.getByRole('region', { name: 'Values' });
  await expect(values.getByLabel('HTTP port', { exact: false })).toBeVisible();
  await expect(values.getByLabel('Admin user', { exact: false })).toHaveCount(0);

  // --- An edited label makes the field adjusted; reset brings it back ---
  await fieldRow(page, 'http_port').getByRole('button', { name: /^http_port/ }).click();
  let field = page.getByRole('dialog', { name: 'Field http_port' });
  await field.getByLabel('Label').fill('Web port');
  await field.getByRole('button', { name: 'Done' }).click();
  await expect(fieldRow(page, 'http_port')).toContainText('Adjusted');
  await expect(values.getByLabel('Web port', { exact: false })).toBeVisible();

  await fieldRow(page, 'http_port').getByRole('button', { name: /^http_port/ }).click();
  field = page.getByRole('dialog', { name: 'Field http_port' });
  await field.getByRole('button', { name: 'Reset to source' }).click();
  await expect(field.getByLabel('Label')).toHaveValue('HTTP port');
  await field.getByRole('button', { name: 'Done' }).click();
  await expect(fieldRow(page, 'http_port')).toContainText('From playbook');

  await page.getByRole('button', { name: 'Save variables' }).click();
  await toast(page, 'Variables saved');
  await page.reload();
  await expect(fieldRow(page, 'http_port')).toContainText('From playbook');
  await expect(page.getByRole('switch', { name: 'admin_user active' })).not.toBeChecked();
  await expect(page.getByRole('switch', { name: 'http_port active' })).toBeChecked();
  // The variables page (field list, values) fits its column at the default
  // 1280px viewport.
  await expectNoClippedCards(page);

  // --- A group's schema: manual fields through "Add field" --------------
  await page.goto(`${base}/groups`);
  await page.getByRole('button', { name: 'Add group' }).click();
  dialog = page.getByRole('dialog', { name: 'Add group' });
  await dialog.getByLabel('Name').fill(GROUP);
  await dialog.getByRole('button', { name: 'Create group' }).click();
  // The group page has no tabs: the schema is its own section.
  await page.getByRole('button', { name: 'Add field' }).click();
  field = page.getByRole('dialog', { name: 'Field field_1' });
  await field.getByLabel('Name').fill('region');
  await page.getByRole('dialog', { name: 'Field region' }).getByRole('button', { name: 'Done' }).click();
  await page.getByRole('button', { name: 'Save schema' }).click();
  await toast(page, 'Schema saved');
  await expect(page.getByRole('switch', { name: 'region active' })).toBeChecked();
});
