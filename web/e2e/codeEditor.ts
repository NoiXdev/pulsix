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
