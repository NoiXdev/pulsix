import { screen, waitFor, type BoundFunctions, type queries } from '@testing-library/react';
import { EditorView } from '@codemirror/view';

type Queries = BoundFunctions<typeof queries>;

/**
 * findCodeEditor waits for the lazily loaded CodeMirror editor named
 * `name` (ui/code-editor.tsx) and returns its content element, the
 * textbox a user types into. Until the chunk has loaded the same name
 * belongs to the plain textarea fallback; a test that types before the
 * swap would race it, so every editor test starts here.
 */
export async function findCodeEditor(name: string | RegExp, scope: Queries = screen): Promise<HTMLElement> {
  return waitFor(() => {
    const box = scope.getByRole('textbox', { name });
    if (!box.classList.contains('cm-content')) throw new Error(`code editor "${String(name)}" is still loading`);
    return box;
  });
}

function viewOf(content: HTMLElement): EditorView {
  const view = EditorView.findFromDOM(content);
  if (!view) throw new Error('not a CodeMirror editor');
  return view;
}

/** codeOf reads the editor's document (its DOM's text drops the line breaks). */
export function codeOf(content: HTMLElement): string {
  return viewOf(content).state.doc.toString();
}

/**
 * replaceCode replaces the whole document as one user edit, the way a
 * paste over a select-all does. jsdom has no layout and no real
 * contenteditable input, so typing key by key into CodeMirror is not
 * something it can model; the editor's own key bindings are tested on
 * their own in ui/code-editor.test.tsx.
 */
export function replaceCode(content: HTMLElement, text: string): void {
  const view = viewOf(content);
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, userEvent: 'input.paste' });
}
