import { test, expect, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const base = `/o/${ORG.slug}`;

async function toast(page: Page, text: string | RegExp) {
  await expect(page.getByText(text).first()).toBeVisible();
}

test('build an inventory with a schema, a jump host and a generated key, assign a host, and preview what Ansible receives', async ({ page }) => {
  // A zone and a managed host of this journey's own.
  await page.goto(`${base}/hosts`);
  await page.getByRole('button', { name: 'Add zone' }).click();
  let dialog = page.getByRole('dialog', { name: 'Add zone' });
  // Exact, for the slug field's "Generate slug from the name" button —
  // see addZone in hosts.spec.ts.
  await dialog.getByLabel('Name', { exact: true }).fill('Inventory');
  await dialog.getByLabel('Slug', { exact: true }).fill('inv');
  await dialog.getByRole('button', { name: 'Create zone' }).click();
  await toast(page, 'Zone Inventory created');
  await page.getByRole('button', { name: 'Add host' }).click();
  dialog = page.getByRole('dialog', { name: 'Add host' });
  await dialog.getByLabel('Name').fill('inv-web-01');
  await dialog.getByLabel('Zone').selectOption({ label: 'Inventory' });
  await dialog.getByLabel('Managed (Ansible via SSH)').check();
  await dialog.getByRole('button', { name: 'Create host' }).click();
  await toast(page, 'Host inv-web-01 created');

  // A generated SSH key: the public key is shown once with snippets; no private key ever.
  await page.goto(`${base}/credentials`);
  await page.getByRole('button', { name: 'Add credential' }).click();
  dialog = page.getByRole('dialog', { name: 'Add credential' });
  // Not exact: AddCredentialDialog also has a "Username" field, whose
  // accessible name contains "Name" as a substring.
  await dialog.getByLabel('Name', { exact: true }).fill('inv-deploy');
  await dialog.getByLabel('Username').fill('ansible');
  await dialog.getByRole('button', { name: 'Create credential' }).click();
  const keyDialog = page.getByRole('dialog', { name: 'Install the public key' });
  // Not exact: PublicKeyPanel also has a "Copy Public key" button whose
  // accessible name contains "Public key" as a substring.
  await expect(keyDialog.getByLabel('Public key', { exact: true })).toContainText(`pulsix:${ORG.slug}:inv-deploy`);
  await expect(page.locator('body')).not.toContainText('PRIVATE KEY');
  await keyDialog.getByRole('button', { name: 'Done' }).click();

  // A jump host that uses the key.
  await page.goto(`${base}/jump-hosts`);
  await page.getByRole('button', { name: 'Add jump host' }).click();
  dialog = page.getByRole('dialog', { name: 'Add jump host' });
  // Not exact: BastionDialog also has a "Username" field.
  await dialog.getByLabel('Name', { exact: true }).fill('inv-bastion');
  await dialog.getByLabel('Address').fill('bastion.inv.example');
  await dialog.getByLabel('Port').fill('2222');
  await dialog.getByLabel('Username').fill('ops');
  await dialog.getByLabel('Credential').selectOption({ label: 'inv-deploy' });
  await dialog.getByRole('button', { name: 'Save jump host' }).click();
  await toast(page, 'Jump host inv-bastion saved');

  // Parent group with a required group field and the jump host.
  await page.goto(`${base}/groups`);
  await page.getByRole('button', { name: 'Add group' }).click();
  dialog = page.getByRole('dialog', { name: 'Add group' });
  await dialog.getByLabel('Name').fill('inv_production');
  await dialog.getByRole('button', { name: 'Create group' }).click();
  await page.getByRole('button', { name: 'Add field' }).click();
  let editor = page.getByRole('group', { name: 'Field field_1' });
  await editor.getByLabel('Name').fill('env');
  editor = page.getByRole('group', { name: 'Field env' });
  await editor.getByLabel('Required').first().check();
  await page.getByRole('button', { name: 'Save schema' }).click();
  await toast(page, 'Schema saved');
  await page.getByRole('tab', { name: 'Group values' }).click();
  await page.getByLabel('env').fill('prod');
  await page.getByRole('button', { name: 'Save values' }).click();
  await toast(page, 'Group values saved');
  await page.getByRole('tab', { name: 'Settings' }).click();
  await page.getByLabel('Jump host').selectOption({ label: 'inv-bastion' });
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await toast(page, 'Group saved');

  // Child group with a host-scoped repeater holding a secret.
  await page.getByRole('tab', { name: 'Children' }).click();
  await page.getByRole('button', { name: 'Add child group' }).click();
  dialog = page.getByRole('dialog', { name: 'Add child group' });
  await dialog.getByLabel('Name').fill('inv_web');
  await dialog.getByRole('button', { name: 'Create group' }).click();
  await page.getByRole('button', { name: 'Add field' }).click();
  editor = page.getByRole('group', { name: 'Field field_1' });
  await editor.getByLabel('Name').fill('vhosts');
  editor = page.getByRole('group', { name: 'Field vhosts' });
  await editor.getByLabel('Type').first().selectOption('repeater');
  await editor.getByLabel(/Host \(asked/).check();
  await editor.getByLabel('Required').first().check();
  const sub = page.getByRole('group', { name: 'Field value' });
  await sub.getByLabel('Name').fill('domain');
  await page.getByRole('group', { name: 'Field domain' }).getByLabel('Required').check();
  await editor.getByRole('button', { name: 'Add sub-field' }).click();
  const pw = page.getByRole('group', { name: 'Field field_2' });
  await pw.getByLabel('Name').fill('password');
  await page.getByRole('group', { name: 'Field password' }).getByLabel('Type').selectOption('secret');
  await page.getByRole('button', { name: 'Save schema' }).click();
  await toast(page, 'Schema saved');

  // Assign the host: a missing required value is rejected at its field.
  await page.getByRole('tab', { name: 'Members' }).click();
  await page.getByRole('button', { name: 'Assign host' }).click();
  dialog = page.getByRole('dialog', { name: 'Assign host to inv_web' });
  // Not exact: the "vhosts" repeater's aria-labelledby group also carries
  // "Host" as a substring of its own accessible name.
  await dialog.getByLabel('Host', { exact: true }).selectOption({ label: 'inv-web-01' });
  await dialog.getByRole('button', { name: 'Assign to inv_web' }).click();
  await expect(dialog.getByRole('alert').first()).toContainText('is required');
  await dialog.getByRole('button', { name: 'Add vhosts' }).click();
  await dialog.getByLabel('domain').fill('shop.acme.io');
  await dialog.getByLabel('password').fill('e2e-hunter2');
  await dialog.getByRole('button', { name: 'Assign to inv_web' }).click();
  await toast(page, 'Host assigned to inv_web');
  await expect(page.getByText('complete')).toBeVisible();

  // The preview renders the jump host chain and masks the secret.
  await page.getByRole('tab', { name: 'Preview inventory' }).click();
  const json = page.getByLabel('Inventory JSON');
  await expect(json).toContainText('-o ProxyJump=ops@bastion.inv.example:2222');
  await expect(json).toContainText('"password": "********"');
  await expect(json).toContainText('"env": "prod"');
  await expect(page.locator('body')).not.toContainText('e2e-hunter2');

  // The host page shows its groups, the resolved route, and the key to install.
  await page.goto(`${base}/hosts`);
  await page.getByRole('link', { name: 'inv-web-01', exact: true }).click();
  await expect(page.getByRole('link', { name: 'inv_web' })).toBeVisible();
  await page.getByLabel('Credential').selectOption({ label: 'inv-deploy' });
  await page.getByRole('button', { name: 'Save SSH settings' }).click();
  await toast(page, 'SSH settings saved');
  await expect(page.getByLabel('Effective route')).toContainText('inherited from group inv_production');
  await expect(page.getByText('Install this key on the host')).toBeVisible();
});
