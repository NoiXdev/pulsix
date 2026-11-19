import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { CodeEditor } from './code-editor.tsx';
import { codeOf, findCodeEditor, replaceCode } from '../test/codeEditor.ts';

function Controlled({ initial = '', onValue, maxLength }: { initial?: string; onValue?: (v: string) => void; maxLength?: number }) {
  const [value, setValue] = useState(initial);
  return (
    <CodeEditor
      label="Playbook YAML"
      language="yaml"
      maxLength={maxLength}
      value={value}
      onChange={(v) => {
        setValue(v);
        onValue?.(v);
      }}
    />
  );
}

describe('CodeEditor', () => {
  it('offers a plain textarea with the same name while the editor loads, and it edits the value', () => {
    const onValue = vi.fn();
    render(<Controlled initial="- hosts: all" onValue={onValue} />);
    const fallback = screen.getByRole('textbox', { name: 'Playbook YAML' });
    expect(fallback.tagName).toBe('TEXTAREA');
    expect(fallback).toHaveValue('- hosts: all');
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
  });

  it('refuses an edit that would make the text longer than maxLength', async () => {
    const onValue = vi.fn();
    render(<Controlled initial="abc" maxLength={5} onValue={onValue} />);
    const editor = await findCodeEditor('Playbook YAML');
    replaceCode(editor, 'abcdef');
    expect(codeOf(editor)).toBe('abc');
    expect(onValue).not.toHaveBeenCalled();
    replaceCode(editor, 'abcde');
    expect(onValue).toHaveBeenLastCalledWith('abcde');
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
});
