import { expect, type Locator, type Page } from '@playwright/test';

/**
 * The Playwright half of src/test/codeEditor.ts: replace the text of a
 * ui/code-editor.tsx field named `name` inside `scope` (a page or a dialog
 * locator).
 *
 * It waits for the CodeMirror editor itself, not the plain textarea that
 * carries the same name while the editor's chunk loads, so the text never
 * lands in a fallback that is about to be swapped out. `fill` on the
 * editor's content element (contenteditable) selects what is there and
 * inserts the text as one edit, the way a paste does: no key is pressed,
 * so the editor's auto-indent on Enter does not touch the text's own
 * indentation, which a YAML playbook depends on.
 */
export async function fillCodeEditor(scope: Page | Locator, name: string, text: string): Promise<void> {
  const content = scope.locator('.cm-content').and(scope.getByRole('textbox', { name }));
  await expect(content).toBeVisible();
  await content.fill(text);
}

/**
 * createEditorPlaybook adds an editor playbook through the new playbook page
 * (backlog 3.123): "New playbook" on the list, the YAML into the page's code
 * editor, Create. It returns once the new playbook's own page is open; the
 * caller still sees the "Playbook created" toast.
 */
export async function createEditorPlaybook(page: Page, base: string, yaml: string): Promise<void> {
  await page.goto(`${base}/playbooks`);
  await page.getByRole('button', { name: 'New playbook' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'New playbook' })).toBeVisible();
  await fillCodeEditor(page, 'Playbook YAML', yaml);
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page).toHaveURL(/\/playbooks\/[0-9a-f-]{36}$/);
}
