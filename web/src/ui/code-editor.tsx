import { lazy, Suspense, useEffect, useId, useImperativeHandle, useRef, type CSSProperties, type Ref, type RefObject } from 'react';
import { cn } from '../lib/utils.ts';
import { useT } from '../i18n/useT.ts';
import { ErrorBoundary } from './ErrorBoundary.tsx';

export type CodeEditorLanguage = 'yaml' | 'json' | 'text';

/** A problem to mark in the text. `line` is 1-based; a diagnostic without
 * one (or past the end) is not marked — its message is the caller's to
 * show next to the editor, as it shows every message. */
export interface CodeDiagnostic {
  line?: number;
  message: string;
}

/** What a caller can do to the editor from outside (through `ref`). */
export interface CodeEditorHandle {
  /** Puts the focus in the editor (a visible label's click calls it: the
   * editor's text is not a form control, so `<label for>` cannot reach it). */
  focus(): void;
  /** Moves the cursor to the start of a 1-based line (past the end: the
   * last line), scrolls it into view and focuses the editor. */
  revealLine(line: number): void;
}

interface CodeEditorBaseProps {
  ref?: Ref<CodeEditorHandle>;
  value: string;
  onChange?: (value: string) => void;
  language: CodeEditorLanguage;
  readOnly?: boolean;
  /** Marks the text invalid for assistive technology (aria-invalid). */
  invalid?: boolean;
  /** Further descriptions of the text (a field's hint), by element id. */
  describedBy?: string;
  placeholder?: string;
  /** Limits the text's length (characters, a line break counting one). As
   * with a textarea's maxLength an oversize paste or insert is truncated to
   * the room left, not refused; the stand-in textarea does the same. */
  maxLength?: number;
  diagnostics?: readonly CodeDiagnostic[];
  /** A fixed height (any CSS length); the text scrolls inside it. */
  height?: string;
  /** Without a fixed height the editor grows with its text from minHeight
   * up to maxHeight, then scrolls. */
  minHeight?: string;
  maxHeight?: string;
  /** Takes the focus when it mounts (and the editor again once it has
   * loaded), also over a dialog's own first control. */
  autoFocus?: boolean;
  className?: string;
}

/** Named by a label string or by another element's id — a `<label for>`
 * cannot name the editor's text, which is not a form control. */
export type CodeEditorProps = CodeEditorBaseProps & ({ label: string; labelledBy?: never } | { labelledBy: string; label?: never });

const CodeMirrorEditor = lazy(() => import('./codemirror/CodeMirrorEditor.tsx'));

const FRAME = 'overflow-hidden rounded-md border border-border bg-background focus-within:border-primary';

/**
 * CodeEditor edits code — a playbook's YAML, JSON values, a shell command —
 * in CodeMirror 6: line numbers, syntax colours from the app's tokens,
 * bracket matching, folding, search (Mod-F), undo, and Tab indenting by
 * two spaces. The editor is its own chunk, loaded on first use so the
 * app's entry chunk does not carry it; until it is there (and should the
 * chunk fail to load, e.g. across a deploy) a plain monospace textarea
 * with the same name, value and handler stands in, so the field always
 * works and always has a textbox.
 *
 * Because Tab indents, a hint under an editable editor says how to leave
 * it with the keyboard (Escape, then Tab). It shows only with CodeMirror
 * itself: under the stand-in textarea Tab moves the focus as usual.
 */
export function CodeEditor({ ref, ...props }: CodeEditorProps) {
  const t = useT();
  const hintId = useId();
  const editable = !props.readOnly;
  const describedBy = [props.describedBy, editable ? hintId : undefined].filter(Boolean).join(' ') || undefined;
  const editor = useRef<CodeEditorHandle>(null);
  const plain = useRef<HTMLTextAreaElement>(null);
  useImperativeHandle(
    ref,
    () => ({
      focus() {
        if (editor.current) return editor.current.focus();
        plain.current?.focus();
      },
      revealLine(line: number) {
        if (editor.current) return editor.current.revealLine(line);
        const area = plain.current;
        if (!area) return;
        const starts = area.value.split('\n');
        const n = Math.min(Math.max(1, Math.floor(line)), starts.length);
        const at = starts.slice(0, n - 1).reduce((sum, l) => sum + l.length + 1, 0);
        area.focus();
        area.setSelectionRange(at, at);
      },
    }),
    [],
  );
  const fallback = <PlainCode {...props} describedBy={props.describedBy} ref={plain} />;
  return (
    <div className={cn('flex min-w-0 flex-col gap-1', props.className)}>
      <ErrorBoundary fallback={fallback}>
        <Suspense fallback={fallback}>
          <div className={FRAME}>
            <CodeMirrorEditor
              handle={editor}
              value={props.value}
              onChange={props.onChange}
              language={props.language}
              label={props.label}
              labelledBy={props.labelledBy}
              describedBy={describedBy}
              readOnly={props.readOnly}
              invalid={props.invalid}
              placeholder={props.placeholder}
              maxLength={props.maxLength}
              diagnostics={props.diagnostics}
              height={props.height}
              minHeight={props.minHeight}
              maxHeight={props.maxHeight}
              autoFocus={props.autoFocus}
            />
          </div>
          {editable && (
            <p id={hintId} className="text-[11px] text-muted-foreground">
              {t.ui.codeEditor.keyboardHint}
            </p>
          )}
        </Suspense>
      </ErrorBoundary>
    </div>
  );
}

/** The textarea that stands in for the editor while it loads. */
function PlainCode({ ref, ...props }: Omit<CodeEditorBaseProps, 'ref'> & { label?: string; labelledBy?: string; ref: RefObject<HTMLTextAreaElement | null> }) {
  const autoFocus = props.autoFocus;
  useEffect(() => {
    if (!autoFocus) return;
    // Again after the current effects: a dialog opened in the same commit
    // focuses its first control from its own, later effect.
    const take = () => ref.current?.focus();
    take();
    queueMicrotask(take);
  }, [autoFocus, ref]);
  const style: CSSProperties = { height: props.height, minHeight: props.minHeight ?? '5rem', maxHeight: props.maxHeight };
  return (
    <textarea
      ref={ref}
      aria-label={props.label}
      aria-labelledby={props.labelledBy}
      aria-describedby={props.describedBy}
      aria-invalid={props.invalid ? true : undefined}
      readOnly={props.readOnly}
      maxLength={props.maxLength}
      placeholder={props.placeholder}
      spellCheck={false}
      wrap="off"
      style={style}
      className={cn(FRAME, 'resize-y p-3 font-mono text-xs text-foreground focus-visible:outline-none')}
      value={props.value}
      onChange={(e) => props.onChange?.(e.target.value)}
    />
  );
}
