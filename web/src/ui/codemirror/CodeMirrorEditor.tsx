import { useEffect, useImperativeHandle, useRef, type Ref } from 'react';
import { Annotation, Compartment, EditorState, Transaction, type ChangeSpec, type Extension, type TransactionSpec } from '@codemirror/state';
import {
  EditorView,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
  placeholder as placeholderText,
} from '@codemirror/view';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { bracketMatching, foldGutter, foldKeymap, indentOnInput, indentUnit } from '@codemirror/language';
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search';
import { lintGutter, setDiagnostics, type Diagnostic } from '@codemirror/lint';
import { yaml } from '@codemirror/lang-yaml';
import { json } from '@codemirror/lang-json';
import { editorHighlighting, editorTheme } from './theme.ts';
import type { CodeDiagnostic, CodeEditorHandle, CodeEditorLanguage } from '../code-editor.tsx';

export interface CodeMirrorEditorProps {
  /** Receives focus and revealLine (ui/code-editor.tsx's CodeEditorHandle). */
  handle?: Ref<CodeEditorHandle>;
  value: string;
  onChange?: (value: string) => void;
  language: CodeEditorLanguage;
  label?: string;
  labelledBy?: string;
  describedBy?: string;
  readOnly?: boolean;
  invalid?: boolean;
  placeholder?: string;
  maxLength?: number;
  diagnostics?: readonly CodeDiagnostic[];
  height?: string;
  minHeight?: string;
  maxHeight?: string;
  autoFocus?: boolean;
}

/** Puts the focus in the editor as it mounts, and again once the current
 * effects have run: a dialog opened in the same commit focuses its first
 * control from its own effect, after this one. When the editor replaced
 * its focused stand-in textarea, the focus fell back to the page and comes
 * here; it is not taken from another text field the user moved to. */
function focusOnMount(v: EditorView) {
  const take = () => {
    if (v.dom.isConnected && !typingElsewhere(v)) v.focus();
  };
  take();
  queueMicrotask(take);
}

function typingElsewhere(v: EditorView): boolean {
  const el = document.activeElement;
  if (!el || v.dom.contains(el)) return false;
  return el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement || (el instanceof HTMLElement && el.isContentEditable);
}

/** Marks a transaction that brings the editor to the `value` prop, so the
 * update listener does not report the caller's own value back to it. */
const fromProps = Annotation.define<boolean>();

/** How long a first Escape keeps Tab released, the same window CodeMirror
 * gives its own tab-focus mode after an Escape. */
const ESCAPE_WINDOW_MS = 2000;

/** How many reported texts may wait for their echo (see `unechoed`). */
const MAX_UNECHOED = 16;

/** Cuts an edit that would make the text longer than `max` down to the room
 * left, as a textarea does with a paste: the inserted text is trimmed (in
 * order, over all of the transaction's changes) and the rest of the edit,
 * deletions included, goes through. Edits that do not grow the text, and
 * the caller's own value, are left alone. */
function truncateToRoom(tr: Transaction, max: number): Transaction | TransactionSpec {
  if (!tr.docChanged || tr.annotation(fromProps)) return tr;
  if (tr.newDoc.length <= max || tr.newDoc.length <= tr.startState.doc.length) return tr;
  let room = max - tr.startState.doc.length;
  tr.changes.iterChanges((fromA, toA) => {
    room += toA - fromA;
  });
  const changes: ChangeSpec[] = [];
  tr.changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
    let text = inserted.toString();
    if (text.length > room) {
      let cut = Math.max(room, 0);
      // Never split a surrogate pair.
      const code = text.charCodeAt(cut - 1);
      if (cut > 0 && code >= 0xd800 && code <= 0xdbff) cut--;
      text = text.slice(0, cut);
    }
    room -= text.length;
    if (fromA !== toA || text) changes.push({ from: fromA, to: toA, insert: text });
  });
  if (changes.length === 0) return {};
  return { changes, userEvent: tr.annotation(Transaction.userEvent), scrollIntoView: true };
}

function languageOf(language: CodeEditorLanguage): Extension {
  if (language === 'yaml') return yaml();
  if (language === 'json') return json();
  return [];
}

/** The extensions that follow the props; everything else is fixed for the
 * editor's lifetime. */
function dynamicExtensions(p: Omit<CodeMirrorEditorProps, 'value' | 'onChange' | 'diagnostics'>): Extension {
  const attrs: Record<string, string> = { 'aria-multiline': 'true' };
  if (p.label) attrs['aria-label'] = p.label;
  if (p.labelledBy) attrs['aria-labelledby'] = p.labelledBy;
  if (p.describedBy) attrs['aria-describedby'] = p.describedBy;
  if (p.invalid) attrs['aria-invalid'] = 'true';
  if (p.readOnly) {
    attrs['aria-readonly'] = 'true';
    // Not editable is not focusable: a reader still scrolls and selects
    // with the keyboard.
    attrs.tabindex = '0';
  }
  const size: Record<string, string> = {};
  if (p.height) size.height = p.height;
  const scroller: Record<string, string> = {};
  if (p.maxHeight) scroller.maxHeight = p.maxHeight;
  const max = p.maxLength;
  return [
    EditorView.contentAttributes.of(attrs),
    EditorView.theme({ '&': size, '.cm-scroller': scroller, '.cm-content, .cm-gutter': p.minHeight ? { minHeight: p.minHeight } : {} }),
    languageOf(p.language),
    EditorState.readOnly.of(!!p.readOnly),
    EditorView.editable.of(!p.readOnly),
    // Tab indents while the text can be edited; a read-only editor leaves
    // Tab to the browser.
    p.readOnly ? [] : keymap.of([indentWithTab]),
    p.placeholder ? placeholderText(p.placeholder) : [],
    max === undefined
      ? []
      : EditorState.transactionFilter.of((tr) => truncateToRoom(tr, max)),
  ];
}

function toDiagnostics(state: EditorState, diagnostics: readonly CodeDiagnostic[] | undefined): Diagnostic[] {
  const out: Diagnostic[] = [];
  for (const d of diagnostics ?? []) {
    if (d.line === undefined || d.line < 1 || d.line > state.doc.lines) continue;
    const line = state.doc.line(d.line);
    out.push({ from: line.from, to: line.to, severity: 'error', message: d.message });
  }
  return out;
}

/**
 * CodeMirrorEditor is the CodeMirror 6 view behind ui/code-editor.tsx,
 * loaded lazily as its own chunk. It is controlled: the `value` prop is
 * the document (an outside change replaces it, marked so it is not
 * reported back), every edit goes out through `onChange`.
 *
 * Tab indents (two spaces, never a tab character). CodeMirror's own
 * escape hatch keeps that from trapping keyboard users: after Escape the
 * next Tab moves the focus instead. In a native dialog Escape would also
 * close the dialog, so the first Escape in the text is taken (prevented)
 * for the escape hatch; a second one within the same window goes through
 * and closes the dialog as everywhere else.
 */
export default function CodeMirrorEditor(props: CodeMirrorEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const dynamic = useRef(new Compartment());
  const latest = useRef(props);
  // Texts this editor reported that the caller has not echoed back as
  // `value` yet, oldest first. A render for an earlier keystroke can run
  // its effect after the user typed on: its `value` is one of these, and
  // writing it into the document would drop what came after.
  const unechoed = useRef<string[]>([]);
  useEffect(() => {
    latest.current = props;
  });

  // Created once; the props it reads at creation are the first render's.
  useEffect(() => {
    const p = latest.current;
    const state = EditorState.create({
      doc: p.value,
      extensions: [
        lineNumbers(),
        foldGutter(),
        p.diagnostics ? lintGutter() : [],
        highlightActiveLineGutter(),
        highlightActiveLine(),
        drawSelection(),
        history(),
        indentOnInput(),
        bracketMatching(),
        highlightSelectionMatches(),
        search({ top: true }),
        indentUnit.of('  '),
        EditorState.tabSize.of(2),
        editorTheme,
        editorHighlighting,
        keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, ...foldKeymap]),
        dynamic.current.of(dynamicExtensions(p)),
        EditorView.updateListener.of((u) => {
          if (!u.docChanged || u.transactions.some((tr) => tr.annotation(fromProps))) return;
          const text = u.state.doc.toString();
          // Bounded for a caller that never echoes (a value it ignores).
          unechoed.current = [...unechoed.current.slice(-(MAX_UNECHOED - 1)), text];
          latest.current.onChange?.(text);
        }),
      ],
    });
    const v = new EditorView({ state, parent: host.current! });
    view.current = v;

    if (p.autoFocus) focusOnMount(v);

    let escapedAt = 0;
    const onKeyDown = (e: KeyboardEvent) => {
      // A read-only editor binds no Tab: there is nothing to release.
      if (latest.current.readOnly) return;
      if (e.key !== 'Escape') {
        if (!['Shift', 'Control', 'Alt', 'Meta'].includes(e.key)) escapedAt = 0;
        return;
      }
      // Taken by the editor itself (a multi-cursor collapse, a fold).
      if (e.defaultPrevented) return;
      if (escapedAt && Date.now() - escapedAt <= ESCAPE_WINDOW_MS) {
        escapedAt = 0;
        return;
      }
      escapedAt = Date.now();
      e.preventDefault();
    };
    // Registered after CodeMirror's own listener on the same element, so it
    // sees whether a key binding already handled the Escape.
    v.contentDOM.addEventListener('keydown', onKeyDown);
    return () => {
      v.contentDOM.removeEventListener('keydown', onKeyDown);
      v.destroy();
      view.current = null;
    };
  }, []);

  useImperativeHandle(
    props.handle,
    () => ({
      focus() {
        view.current?.focus();
      },
      revealLine(line: number) {
        const v = view.current;
        if (!v) return;
        const n = Math.min(Math.max(1, Math.floor(line)), v.state.doc.lines);
        const at = v.state.doc.line(n).from;
        v.dispatch({ selection: { anchor: at }, effects: EditorView.scrollIntoView(at, { y: 'center' }) });
        v.focus();
      },
    }),
    [],
  );

  const { value, language, label, labelledBy, describedBy, readOnly, invalid, placeholder, maxLength, height, minHeight, maxHeight, diagnostics } = props;

  useEffect(() => {
    view.current?.dispatch({
      effects: dynamic.current.reconfigure(
        dynamicExtensions({ language, label, labelledBy, describedBy, readOnly, invalid, placeholder, maxLength, height, minHeight, maxHeight }),
      ),
    });
  }, [language, label, labelledBy, describedBy, readOnly, invalid, placeholder, maxLength, height, minHeight, maxHeight]);

  useEffect(() => {
    const v = view.current;
    if (!v) return;
    const echo = unechoed.current.lastIndexOf(value);
    if (echo >= 0) {
      unechoed.current = unechoed.current.slice(echo + 1);
      return;
    }
    unechoed.current = [];
    if (v.state.doc.toString() === value) return;
    // Not undoable: an undo would bring back what the caller replaced and
    // report it to the caller as an edit.
    v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value }, annotations: [fromProps.of(true), Transaction.addToHistory.of(false)] });
  }, [value]);

  // Once `value` is the document, every report before it has been echoed or
  // overtaken: none may hold back a later outside value. (Edits that net
  // out within one render, a type and a delete, leave `value` unchanged, so
  // the effect above never sees them.)
  useEffect(() => {
    const v = view.current;
    if (v && unechoed.current.length > 0 && v.state.doc.toString() === value) unechoed.current = [];
  });

  // By content, not identity: a caller builds the list afresh every render.
  const diagnosticsKey = JSON.stringify(diagnostics ?? []);
  useEffect(() => {
    const v = view.current;
    if (!v) return;
    v.dispatch(setDiagnostics(v.state, toDiagnostics(v.state, JSON.parse(diagnosticsKey) as CodeDiagnostic[])));
  }, [diagnosticsKey]);

  return <div ref={host} className="min-w-0" />;
}
