/**
 * The job chaining journey (backlog 2.121): the template editor's
 * "Follow-ups on success" picker → running the template through the
 * follow-ups dialog → the run detail's "Follow-ups" section showing the
 * follow-up as Started → the link to its run → the "Follow-up of" link back
 * to the parent in the child's sidebar.
 *
 * It rides on what ansible.spec.ts leaves behind (the managed fixture host
 * `ansible-e2e-host`, reachable through the stack's worker), so it sits
 * after that project and skips for the same reason when no worker runs.
 */
import { test, expect, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { workerServiceAvailable } from './worker.ts';
import { pickOption } from './pickOption.ts';
import { chooseRowAction } from './rowMenu.ts';
import { createEditorPlaybook } from './codeEditor.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

test.skip(!workerServiceAvailable(), 'pulsix worker service is not running in the compose test stack');

const base = `/o/${ORG.slug}`;
const HOST_NAME = 'ansible-e2e-host';
const PLAYBOOK_NAME = 'Pulsix chain link';

const CHAIN_PLAYBOOK = `- name: ${PLAYBOOK_NAME}
  hosts: all
  gather_facts: false
  tasks:
    - name: ${PLAYBOOK_NAME}
      ansible.builtin.debug:
        msg: "chained"
`;

async function toast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.getByText(text).first()).toBeVisible();
}

/** createTemplate fills the editor for a template on the fixture host;
 * `followUp` is the name of a template to chain after it. */
async function createTemplate(page: Page, name: string, followUp?: string): Promise<void> {
  await page.goto(`${base}/templates`);
  await page.getByRole('button', { name: 'Add template' }).click();
  const dialog = page.getByRole('dialog', { name: 'New job template' });
  await dialog.getByLabel('Name').fill(name);
  await pickOption(dialog, 'Playbook', PLAYBOOK_NAME);
  await dialog.getByRole('checkbox', { name: `Include ${HOST_NAME}` }).check();
  if (followUp) {
    await pickOption(dialog, 'Follow-ups on success', followUp);
    await expect(dialog.getByRole('list', { name: 'Chosen follow-ups' }).getByText(followUp)).toBeVisible();
  }
  await dialog.getByRole('button', { name: 'Create template' }).click();
  await toast(page, `Template ${name} created`);
}

/** expectRunStatus waits for the run page's status pill, scoped to the
 * page's sidebar like ansible.spec.ts does. */
async function expectRunStatus(page: Page, status: string, timeout?: number): Promise<void> {
  await expect(page.locator('main aside').getByText(status, { exact: true })).toBeVisible({ timeout });
}

test('a run starts its follow-up and the two runs link to each other', async ({ page }) => {
  // Creating a playbook and two templates, two real runs and the starter's
  // tick come to more than the suite's 60 s default.
  test.setTimeout(240_000);
  await createEditorPlaybook(page, base, CHAIN_PLAYBOOK);
  await toast(page, 'Playbook created');

  await createTemplate(page, 'chain-b');
  await createTemplate(page, 'chain-a', 'chain-b');

  // A template with follow-ups asks first; they come preselected.
  await chooseRowAction(page, 'chain-a', 'Run');
  const run = page.getByRole('dialog', { name: 'Run chain-a' });
  await expect(run.getByRole('list', { name: 'Chosen follow-ups' }).getByText('chain-b')).toBeVisible();
  await run.getByRole('button', { name: 'Run', exact: true }).click();
  await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
  const parentUrl = page.url();

  // The parent succeeds, then its follow-up is started. A failed parent
  // times out here, waiting for "succeeded", before the follow-up is
  // looked at.
  await expectRunStatus(page, 'succeeded', 60_000);
  const chain = page.locator('#chain');
  const row = chain.locator('li', { hasText: 'chain-b' });
  await expect(chain.getByRole('heading', { name: 'Follow-ups' })).toBeVisible();
  await expect(row.getByText('Started', { exact: true })).toBeVisible({ timeout: 90_000 });

  await row.getByRole('link', { name: 'View run: chain-b' }).click();
  await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
  expect(page.url()).not.toBe(parentUrl);

  // The child's sidebar names the parent and links back to it.
  const parent = page.locator('main aside').getByText('Follow-up of');
  await expect(parent).toBeVisible();
  await page.locator('main aside').getByRole('link', { name: 'chain-a' }).click();
  await expect(page).toHaveURL(parentUrl);
});
