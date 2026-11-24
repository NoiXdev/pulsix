import { createRef, useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { CodeEditor, type CodeEditorHandle } from './code-editor.tsx';
import { EditorView } from '@codemirror/view';
import { codeOf, cursorLineOf, findCodeEditor, replaceCode } from '../test/codeEditor.ts';

function Controlled({ initial = '', onValue, maxLength, autoFocus }: { initial?: string; onValue?: (v: string) => void; maxLength?: number; autoFocus?: boolean }) {
  const [value, setValue] = useState(initial);
  return (
    <CodeEditor
      label="Playbook YAML"
      language="yaml"
      maxLength={maxLength}
      autoFocus={autoFocus}
      value={value}
      onChange={(v) => {
        setValue(v);
        onValue?.(v);
      }}
    />
  );
}

describe('CodeEditor', () => {
  // First in the file: the editor's chunk is loaded once per file, so only
  // the first render sees the stand-in.
  it('offers a plain textarea with the same name while the editor loads, and it edits the value', () => {
    const onValue = vi.fn();
    render(<Controlled initial="- hosts: all" onValue={onValue} autoFocus />);
    const fallback = screen.getByRole('textbox', { name: 'Playbook YAML' });
    expect(fallback.tagName).toBe('TEXTAREA');
    expect(fallback).toHaveFocus();
    expect(fallback).toHaveValue('- hosts: all');
    // Tab moves the focus in the textarea, so the "Tab indents" hint is not shown.
    expect(screen.queryByText(/Tab indents/)).not.toBeInTheDocument();
    fireEvent.change(fallback, { target: { value: '- hosts: web' } });
    expect(onValue).toHaveBeenLastCalledWith('- hosts: web');
  });

  it('loads the editor as a textbox named by the label, holding the value', async () => {
    render(<Controlled initial={'- hosts: all\n  tasks: []'} />);
    const editor = await findCodeEditor('Playbook YAML');
    expect(codeOf(editor)).toBe('- hosts: all\n  tasks: []');
    expect(editor).toHaveAttribute('aria-multiline', 'true');
    expect(screen.queryByRole('textbox', { name: 'Playbook YAML' })?.tagName).not.toBe('TEXTAREA');
  });

  it('is named by another element through labelledBy', async () => {
    render(
      <>
        <span id="yaml-label">Extra vars</span>
        <CodeEditor labelledBy="yaml-label" language="json" value="{}" onChange={() => {}} />
      </>,
    );
    expect(await findCodeEditor('Extra vars')).toHaveAttribute('aria-labelledby', 'yaml-label');
  });

  it('reports what is typed through onChange', async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    render(<Controlled onValue={onValue} />);
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(editor);
    await user.type(editor, '- hosts: db');
    expect(onValue).toHaveBeenLastCalledWith('- hosts: db');
  });

  it('keeps the indentation of the block on Enter', async () => {
    const user = userEvent.setup();
    render(<Controlled initial={'- hosts: all\n  tasks: []'} />);
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(editor);
    await user.keyboard('{Control>}{End}{/Control}{Enter}vars: {{}');
    expect(codeOf(editor)).toBe('- hosts: all\n  tasks: []\n  vars: {}');
  });

  it('follows a value changed from outside without reporting it back', async () => {
    const onChange = vi.fn();
    const { rerender } = render(<CodeEditor label="Code" language="text" value="one" onChange={onChange} />);
    const editor = await findCodeEditor('Code');
    rerender(<CodeEditor label="Code" language="text" value="two" onChange={onChange} />);
    expect(codeOf(editor)).toBe('two');
    expect(onChange).not.toHaveBeenCalled();
  });

  it('indents with two spaces on Tab and outdents on Shift-Tab, never a tab character', async () => {
    const onValue = vi.fn();
    render(<Controlled initial="- hosts: all" onValue={onValue} />);
    const editor = await findCodeEditor('Playbook YAML');
    fireEvent.keyDown(editor, { key: 'Tab', keyCode: 9 });
    expect(codeOf(editor)).toBe('  - hosts: all');
    fireEvent.keyDown(editor, { key: 'Tab', keyCode: 9, shiftKey: true });
    expect(codeOf(editor)).toBe('- hosts: all');
    expect(onValue.mock.calls.flat().join('')).not.toContain('\t');
  });

  it('takes the first Escape to release Tab (Escape, then Tab leaves) and lets a second one through', async () => {
    render(<Controlled initial="x" />);
    const editor = await findCodeEditor('Playbook YAML');
    expect(fireEvent.keyDown(editor, { key: 'Escape', keyCode: 27 })).toBe(false);
    expect(fireEvent.keyDown(editor, { key: 'Escape', keyCode: 27 })).toBe(true);
    // Tab now moves the focus instead of indenting.
    expect(fireEvent.keyDown(editor, { key: 'Escape', keyCode: 27 })).toBe(false);
    fireEvent.keyDown(editor, { key: 'Tab', keyCode: 9 });
    expect(codeOf(editor)).toBe('x');
  });

  it('says how to leave the editor with the keyboard', async () => {
    render(<Controlled initial="x" />);
    const editor = await findCodeEditor('Playbook YAML');
    expect(editor).toHaveAccessibleDescription(/Escape, then Tab/);
  });

  it('is read-only on request: no edits, no indent, no hint', async () => {
    render(<CodeEditor label="Code" language="json" value="{}" readOnly onChange={() => {}} />);
    const editor = await findCodeEditor('Code');
    expect(editor).toHaveAttribute('aria-readonly', 'true');
    expect(editor).toHaveAttribute('contenteditable', 'false');
    fireEvent.keyDown(editor, { key: 'Tab', keyCode: 9 });
    expect(codeOf(editor)).toBe('{}');
    expect(editor).not.toHaveAccessibleDescription(/Escape/);
    // Nothing to release: Escape is left to whatever holds the editor.
    expect(fireEvent.keyDown(editor, { key: 'Escape', keyCode: 27 })).toBe(true);
  });

  it('truncates an edit that would make the text longer than maxLength, like a textarea', async () => {
    const onValue = vi.fn();
    render(<Controlled initial="abc" maxLength={5} onValue={onValue} />);
    const editor = await findCodeEditor('Playbook YAML');
    replaceCode(editor, 'abcdef');
    expect(codeOf(editor)).toBe('abcde');
    expect(onValue).toHaveBeenLastCalledWith('abcde');
    replaceCode(editor, 'abcde');
    expect(codeOf(editor)).toBe('abcde');
  });

  it('truncates an oversize paste at the cursor to the room left', async () => {
    const onValue = vi.fn();
    render(<Controlled initial="abcd" maxLength={6} onValue={onValue} />);
    const editor = await findCodeEditor('Playbook YAML');
    const view = EditorView.findFromDOM(editor)!;
    view.dispatch({ changes: { from: 2, insert: 'XYZW' }, userEvent: 'input.paste' });
    expect(codeOf(editor)).toBe('abXYcd');
    // No room at all: the insert is dropped, a delete still goes through.
    view.dispatch({ changes: { from: 0, insert: 'Q' }, userEvent: 'input.paste' });
    expect(codeOf(editor)).toBe('abXYcd');
    view.dispatch({ changes: { from: 0, to: 2 }, userEvent: 'delete' });
    expect(codeOf(editor)).toBe('XYcd');
  });

  it('leaves the caret after a cut paste at the cursor, as a textarea does', async () => {
    render(<Controlled initial="abcd" maxLength={6} />);
    const editor = await findCodeEditor('Playbook YAML');
    const view = EditorView.findFromDOM(editor)!;
    view.dispatch({ selection: { anchor: 2 } });
    // The paste as CodeMirror sends it: the caret after the whole insert.
    view.dispatch({ changes: { from: 2, insert: 'XYZW' }, selection: { anchor: 6 }, userEvent: 'input.paste' });
    expect(codeOf(editor)).toBe('abXYcd');
    const { main } = view.state.selection;
    expect(main.empty).toBe(true);
    expect(main.head).toBe(4);
  });

  it('leaves no selection over a cut paste that replaced one', async () => {
    render(<Controlled initial="abcd" maxLength={6} />);
    const editor = await findCodeEditor('Playbook YAML');
    const view = EditorView.findFromDOM(editor)!;
    view.dispatch({ selection: { anchor: 1, head: 3 } });
    view.dispatch({ changes: { from: 1, to: 3, insert: 'XYZWVU' }, selection: { anchor: 7 }, userEvent: 'input.paste' });
    expect(codeOf(editor)).toBe('aXYZWd');
    const { main } = view.state.selection;
    expect(main.empty).toBe(true);
    expect(main.head).toBe(5);
  });

  it('marks the line a diagnostic names, and clears the mark once it is gone', async () => {
    const { rerender, container } = render(
      <CodeEditor label="Code" language="yaml" value={'- hosts: all\n\tbad: 1'} onChange={() => {}} diagnostics={[{ line: 2, message: 'tab indentation' }]} />,
    );
    await findCodeEditor('Code');
    await waitFor(() => expect(container.querySelector('.cm-lintRange-error')).not.toBeNull());
    expect(container.querySelector('.cm-lintRange-error')?.textContent).toBe('\tbad: 1');
    rerender(<CodeEditor label="Code" language="yaml" value={'- hosts: all\n  bad: 1'} onChange={() => {}} diagnostics={[]} />);
    await waitFor(() => expect(container.querySelector('.cm-lintRange-error')).toBeNull());
  });

  it('ignores a diagnostic without a line or past the end (its message is the caller’s to show)', async () => {
    const { container } = render(
      <CodeEditor label="Code" language="yaml" value="- hosts: all" onChange={() => {}} diagnostics={[{ message: 'whole document' }, { line: 9, message: 'gone' }]} />,
    );
    await findCodeEditor('Code');
    expect(container.querySelector('.cm-lintRange-error')).toBeNull();
  });

  it('applies a value the parent resets to after typing', async () => {
    const user = userEvent.setup();
    function Resettable() {
      const [value, setValue] = useState('- hosts: all');
      return (
        <>
          <CodeEditor label="Code" language="yaml" value={value} onChange={setValue} />
          <button type="button" onClick={() => setValue('- hosts: all')}>
            Reset
          </button>
        </>
      );
    }
    render(<Resettable />);
    const editor = await findCodeEditor('Code');
    await user.click(editor);
    await user.keyboard('{Control>}{End}{/Control}x');
    expect(codeOf(editor)).toBe('- hosts: allx');
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    expect(codeOf(editor)).toBe('- hosts: all');
  });

  it('keeps a value set from outside out of the undo history', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const { rerender } = render(<CodeEditor label="Code" language="text" value="one" onChange={onChange} />);
    const editor = await findCodeEditor('Code');
    rerender(<CodeEditor label="Code" language="text" value="two" onChange={onChange} />);
    await user.click(editor);
    await user.keyboard('{Control>}z{/Control}');
    expect(codeOf(editor)).toBe('two');
    expect(onChange).not.toHaveBeenCalled();
  });

  it('moves the cursor to a line and focuses the editor on revealLine', async () => {
    const ref = createRef<CodeEditorHandle>();
    render(<CodeEditor ref={ref} label="Code" language="yaml" value={'- hosts: all\n  tasks:\n    - name: one\n    - name: two'} onChange={() => {}} />);
    const editor = await findCodeEditor('Code');
    expect(editor).not.toHaveFocus();
    act(() => ref.current?.revealLine(3));
    expect(cursorLineOf(editor)).toBe(3);
    await waitFor(() => expect(editor).toHaveFocus());
    // A line past the end goes to the last one.
    act(() => ref.current?.revealLine(99));
    expect(cursorLineOf(editor)).toBe(4);
  });

  it('focuses the editor on focus()', async () => {
    const ref = createRef<CodeEditorHandle>();
    render(<CodeEditor ref={ref} label="Code" language="yaml" value="- hosts: all" onChange={() => {}} />);
    const editor = await findCodeEditor('Code');
    expect(editor).not.toHaveFocus();
    act(() => ref.current?.focus());
    await waitFor(() => expect(editor).toHaveFocus());
  });

  it('takes the focus on request, once loaded', async () => {
    render(<CodeEditor label="Code" language="yaml" value="- hosts: all" onChange={() => {}} autoFocus />);
    const editor = await findCodeEditor('Code');
    await waitFor(() => expect(editor).toHaveFocus());
  });

  it('paints panel buttons, lint marks and the error marker from the app’s tokens, not CodeMirror’s fixed colours', async () => {
    render(<CodeEditor label="Code" language="yaml" value="x" onChange={() => {}} diagnostics={[]} />);
    await findCodeEditor('Code');
    const css = [
      ...Array.from(document.querySelectorAll('style'), (el) => el.textContent ?? ''),
      ...(document.adoptedStyleSheets ?? []).flatMap((sheet) => Array.from(sheet.cssRules, (r) => r.cssText)),
    ].join('\n');
    const rule = (selector: RegExp) => css.split('}').filter((r) => selector.test(r.split('{')[0] ?? ''));
    expect(rule(/\.cm-button/).some((r) => /background-image:\s*none/.test(r))).toBe(true);
    expect(rule(/\.cm-lintRange-active/).some((r) => /var\(--/.test(r))).toBe(true);
    expect(rule(/\.cm-lint-marker-error/).some((r) => /var\(--px-crit\)/.test(r))).toBe(true);
    expect(rule(/\.cm-lintPoint/).some((r) => /var\(--px-crit\)/.test(r))).toBe(true);
  });
});
