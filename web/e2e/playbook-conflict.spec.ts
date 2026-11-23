/**
 * Saving a playbook over someone else's save (backlog 3.127), against the
 * real manager: the page changes an editor playbook's text, someone else
 * saves another text through the API meanwhile (with If-Match on the
 * revision both started from, as the app sends it), and the page's Save is
 * then refused as stale. The notice keeps the draft, "Show their changes"
 * shows the other text against the one the draft started from, and
 * "Reload" takes their text into the editor.
 *
 * An editor playbook is saved by the manager alone, so this needs no
 * worker.
 */
import { test, expect, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { createEditorPlaybook, fillCodeEditor } from './codeEditor.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const base = `/o/${ORG.slug}`;
const NOTICE = 'Someone saved this playbook since you started editing.';

const START = `- name: Pulsix conflict e2e
  hosts: all
  gather_facts: false
  tasks: []
`;
const MINE = `- name: Pulsix conflict e2e
  hosts: mine
  gather_facts: false
  tasks: []
`;
const THEIRS = `- name: Pulsix conflict e2e
  hosts: theirs
  gather_facts: false
  tasks: []
`;

interface StoredPlaybook {
  playbook: { inline?: string; revision: number };
}

/** readPlaybook reads the playbook the way the API serves it. */
async function readPlaybook(page: Page, id: string): Promise<StoredPlaybook['playbook']> {
  const res = await page.request.get(`/api/v1/orgs/${ORG.slug}/playbooks/${id}`);
  expect(res.ok(), await res.text()).toBeTruthy();
  return ((await res.json()) as StoredPlaybook).playbook;
}

test('a save over a newer one shows the notice, their changes, and Reload takes theirs', async ({ page }) => {
  await createEditorPlaybook(page, base, START);
  const id = page.url().split('/').at(-1)!;
  const editor = page.locator('.cm-content').and(page.getByRole('textbox', { name: 'Playbook YAML' }));
  await expect(editor).toContainText('hosts: all');
  const { revision } = await readPlaybook(page, id);

  // --- The page edits; someone else saves through the API meanwhile -----
  await fillCodeEditor(page, 'Playbook YAML', MINE);
  await expect(page.getByText('Unsaved', { exact: true })).toBeVisible();
  const theirs = await page.request.put(`/api/v1/orgs/${ORG.slug}/playbooks/${id}`, {
    headers: { 'If-Match': `"${revision}"` },
    data: { inline: THEIRS },
  });
  expect(theirs.ok(), await theirs.text()).toBeTruthy();
  expect((await readPlaybook(page, id)).revision).toBe(revision + 1);

  // --- The page's Save is refused: the notice keeps the draft -----------
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  const notice = page.getByRole('region', { name: NOTICE });
  await expect(notice).toBeVisible();
  await expect(editor).toContainText('hosts: mine');
  await expect(page.getByText('Playbook saved')).toHaveCount(0);
  // Nothing of the page's text was saved.
  expect((await readPlaybook(page, id)).inline).toBe(THEIRS);

  // --- Their changes: the start text against theirs ----------------------
  await notice.getByRole('button', { name: 'Show their changes' }).click();
  const diff = notice.getByRole('figure', { name: 'Differences' });
  await expect(diff.getByRole('listitem').filter({ hasText: /^-\s+hosts: all$/ })).toHaveCount(1);
  await expect(diff.getByRole('listitem').filter({ hasText: /^\+\s+hosts: theirs$/ })).toHaveCount(1);
  await expect(diff.getByRole('listitem').filter({ hasText: /hosts: mine/ })).toHaveCount(0);

  // --- Reload drops the draft for their text -----------------------------
  await notice.getByRole('button', { name: 'Reload', exact: true }).click();
  await expect(notice).toHaveCount(0);
  await expect(editor).toContainText('hosts: theirs');
  await expect(editor).not.toContainText('hosts: mine');
  await expect(page.getByText('Unsaved', { exact: true })).toHaveCount(0);
});
