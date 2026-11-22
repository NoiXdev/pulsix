/**
 * The playbook dry run journey (backlog 3.126): an editor playbook whose one
 * task would create a marker file on the host → a template over the
 * managed fixture host → the playbook page's Dry run tab → Start dry run →
 * the latest dry run lists the task as "would change" with its diff, and
 * the counts show one task that would change. A second dry run then says
 * the same: the first one created nothing (check mode), so the file still
 * would be created.
 *
 * It rides on what ansible.spec.ts leaves behind (the managed fixture host
 * `ansible-e2e-host`, reachable through the stack's worker), so it sits
 * after that project and skips for the same reason when no worker runs.
 */
import { test, expect, type Locator, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { workerServiceAvailable } from './worker.ts';
import { pickOption } from './pickOption.ts';
import { createEditorPlaybook } from './codeEditor.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

test.skip(!workerServiceAvailable(), 'pulsix worker service is not running in the compose test stack');

const base = `/o/${ORG.slug}`;
const HOST_NAME = 'ansible-e2e-host';
const PLAYBOOK_NAME = 'Pulsix dry run marker';
const TASK_NAME = 'Create the dry run marker';
const TEMPLATE_NAME = 'dryrun-e2e';
const MARKER_LINE = 'pulsix dry run marker';

const MARKER_PLAYBOOK = `- name: ${PLAYBOOK_NAME}
  hosts: all
  gather_facts: false
  tasks:
    - name: ${TASK_NAME}
      ansible.builtin.copy:
        dest: /tmp/pulsix-dryrun-marker
        content: "${MARKER_LINE}\\n"
        mode: "0644"
`;

async function toast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.getByText(text).first()).toBeVisible();
}

/** expectWouldChange waits for the latest dry run's result: the marker
 * task listed as "would change" with the file's new line as its diff, and
 * one task that would change in the counts. */
async function expectWouldChange(result: Locator): Promise<void> {
  const changes = result.getByRole('list', { name: 'What would change' });
  const change = changes.getByRole('listitem').filter({ has: result.page().getByRole('heading', { name: new RegExp(`^${TASK_NAME} · .+ · would change$`) }) });
  await expect(change).toHaveCount(1, { timeout: 120_000 });
  await expect(change.getByText(`+${MARKER_LINE}`, { exact: true })).toBeVisible();
  const counts = result.getByRole('list', { name: 'Counts over all hosts' });
  await expect(counts.getByRole('listitem').nth(1)).toHaveText('1would change', { timeout: 60_000 });
}

test('a dry run shows what a playbook would change, and changes nothing', async ({ page }) => {
  // A playbook, a template and two dry runs on a real worker come to more
  // than the suite's 60 s default.
  test.setTimeout(300_000);
  await createEditorPlaybook(page, base, MARKER_PLAYBOOK);
  await toast(page, 'Playbook created');
  const playbookUrl = page.url();

  await page.goto(`${base}/templates`);
  await page.getByRole('button', { name: 'Add template' }).click();
  const dialog = page.getByRole('dialog', { name: 'New job template' });
  await dialog.getByLabel('Name').fill(TEMPLATE_NAME);
  await pickOption(dialog, 'Playbook', PLAYBOOK_NAME);
  await dialog.getByRole('checkbox', { name: `Include ${HOST_NAME}` }).check();
  await dialog.getByRole('button', { name: 'Create template' }).click();
  await toast(page, `Template ${TEMPLATE_NAME} created`);

  // The Dry run tab: the one template there is counts as chosen, and the
  // differences are on by default.
  await page.goto(playbookUrl);
  await page.getByRole('tab', { name: 'Dry run' }).click();
  const form = page.getByRole('form', { name: 'Dry run' });
  await expect(form.getByLabel('Template (brings variables and credentials)')).toHaveValue(TEMPLATE_NAME);
  await expect(form.getByRole('checkbox', { name: 'Show differences (diff)' })).toBeChecked();
  await form.getByRole('button', { name: 'Start dry run' }).click();
  await toast(page, 'Dry run started');

  const result = page.getByRole('region', { name: 'Latest dry run' });
  await expectWouldChange(result);
  const open = result.getByRole('link', { name: 'Open run' });
  const first = await open.getAttribute('href');

  // Check mode created nothing: a second dry run would still create the
  // file. (The fixture host's files are not readable from the spec, so
  // this is the proof the stack offers.)
  await form.getByRole('button', { name: 'Start dry run' }).click();
  await expect(open).not.toHaveAttribute('href', first ?? '', { timeout: 30_000 });
  await expectWouldChange(result);
});
