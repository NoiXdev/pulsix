import { lazy, Suspense, useId, type CSSProperties } from 'react';
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

interface CodeEditorBaseProps {
  value: string;
  onChange?: (value: string) => void;
  language: CodeEditorLanguage;
  readOnly?: boolean;
  /** Marks the text invalid for assistive technology (aria-invalid). */
  invalid?: boolean;
  /** Further descriptions of the text (a field's hint), by element id. */
  describedBy?: string;
  placeholder?: string;
  /** Refuses an edit that would make the text longer (characters, a line
   * break counting one), as a textarea's maxLength does. */
  maxLength?: number;
  diagnostics?: readonly CodeDiagnostic[];
  /** A fixed height (any CSS length); the text scrolls inside it. */
  height?: string;
  /** Without a fixed height the editor grows with its text from minHeight
   * up to maxHeight, then scrolls. */
  minHeight?: string;
  maxHeight?: string;
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
 * it with the keyboard (Escape, then Tab).
 */
export function CodeEditor(props: CodeEditorProps) {
  const t = useT();
  const hintId = useId();
  const editable = !props.readOnly;
  const describedBy = [props.describedBy, editable ? hintId : undefined].filter(Boolean).join(' ') || undefined;
  const fallback = <PlainCode {...props} describedBy={props.describedBy} />;
  return (
    <div className={cn('flex min-w-0 flex-col gap-1', props.className)}>
      <ErrorBoundary fallback={fallback}>
        <Suspense fallback={fallback}>
          <div className={FRAME}>
            <CodeMirrorEditor
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
            />
          </div>
        </Suspense>
      </ErrorBoundary>
      {editable && (
        <p id={hintId} className="text-[11px] text-muted-foreground">
          {t.ui.codeEditor.keyboardHint}
        </p>
      )}
    </div>
  );
}

/** The textarea that stands in for the editor while it loads. */
function PlainCode(props: CodeEditorProps) {
  const style: CSSProperties = { height: props.height, minHeight: props.minHeight ?? '5rem', maxHeight: props.maxHeight };
  return (
    <textarea
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
