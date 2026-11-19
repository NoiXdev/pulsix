import { useId, useRef, useState, type FormEvent, type KeyboardEvent } from 'react';
import { Maximize2, Minimize2 } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useCreatePlaybook, useDeletePlaybook, usePlaybook, useUpdatePlaybook } from '../../api/hooks/usePlaybooks.ts';
import { validateYAMLProblem } from './validate.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { CodeEditor } from '../../ui/code-editor.tsx';

export interface PlaybookEditorProps {
  org: string;
  /** The editor playbook to edit; absent means create. */
  playbook?: { id: string; summary: string };
  onClose: () => void;
}

/**
 * PlaybookEditor edits the YAML in the code editor (ui/code-editor.tsx,
 * about 60 % of the viewport tall, or full screen) with the server's
 * validation ported as a client-side preview (validate.ts — the same
 * messages), so an operator sees the problem before submitting, marked on
 * its line where the preview knows it; the server stays the authority and
 * its rejected detail shows inline too. Ctrl/Cmd-S saves once the preview
 * passes; closing with unsaved changes (Cancel, Escape) asks first.
 *
 * The edited document is read from the detail endpoint (the list DTO
 * carries no inline text); the loading/error guard lives here, the form is
 * a separate component taking the loaded text — mirroring
 * CredentialDialog/CredentialForm.
 */
export function PlaybookEditor({ org, playbook, onClose }: PlaybookEditorProps) {
  const detail = usePlaybook(org, playbook?.id ?? '');
  const guard = useQueryGuard(detail);

  if (playbook) {
    if (guard) {
      return (
        <Dialog open onClose={onClose} title={playbook.summary}>
          {guard}
        </Dialog>
      );
    }
    if (!detail.data?.playbook) {
      return (
        <Dialog open onClose={onClose} title={playbook.summary}>
          <QueryLoading />
        </Dialog>
      );
    }
    return (
      <PlaybookForm
        org={org}
        existing={{ id: playbook.id, summary: playbook.summary, inline: detail.data.playbook.inline ?? '' }}
        onClose={onClose}
      />
    );
  }
  return <PlaybookForm org={org} onClose={onClose} />;
}

function PlaybookForm({
  org, existing, onClose,
}: {
  org: string;
  existing?: { id: string; summary: string; inline: string };
  onClose: () => void;
}) {
  const t = useT();
  const e = t.playbooks.editor;
  const toast = useToast();
  const ask = useConfirm();
  const create = useCreatePlaybook(org);
  const update = useUpdatePlaybook(org, existing?.id ?? '');
  const remove = useDeletePlaybook(org);
  const initial = existing?.inline ?? '';
  const [text, setText] = useState(initial);
  const [error, setError] = useState<string | null>(null);
  const [fullScreen, setFullScreen] = useState(false);
  const form = useRef<HTMLFormElement>(null);
  const labelId = useId();
  const title = existing?.summary || e.newTitle;

  // The preview runs on every keystroke; a document outside the minimal
  // parser's subset yields no message (the server judges those).
  const problem = validateYAMLProblem(text, t);
  const previewOk = problem === null;
  const dirty = text !== initial;
  const saving = create.isPending || update.isPending;

  async function submit(ev: FormEvent) {
    ev.preventDefault();
    setError(null);
    if (!previewOk || saving) return; // the preview already names it
    try {
      if (existing) await update.mutateAsync({ inline: text });
      else await create.mutateAsync({ inline: text });
      toast.show(existing ? e.saved : e.created);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : e.saveFailed);
    }
  }

  async function onDelete() {
    if (!(await ask({ message: e.confirmDelete(existing?.summary || e.fallbackName), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(existing?.id ?? '');
      toast.show(e.deleted);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : e.deleteFailed);
    }
  }

  async function requestClose() {
    if (dirty && !(await ask({ title: e.discardTitle, message: e.discardBody, confirmLabel: e.discard, destructive: true }))) return;
    onClose();
  }

  // Escape (the dialog's close request) asks before unsaved changes are
  // dropped. Full screen is left on the keydown instead (onKeyDown): a
  // refused close request spends the browser's activation, and the next one
  // could then not be refused — the question would come with the dialog
  // already closing. This branch is only a fallback.
  function onCancel(): boolean {
    if (fullScreen) {
      setFullScreen(false);
      return false;
    }
    if (!dirty) return true;
    void requestClose();
    return false;
  }

  // Escape leaves full screen, or asks about unsaved changes, before it may
  // close anything: a prevented keydown makes no close request. One the editor already took (to
  // release Tab) is left alone. Ctrl/Cmd-S never reaches the browser's
  // "Save page"; it submits only what the preview passes.
  function onKeyDown(ev: KeyboardEvent<HTMLFormElement>) {
    if (ev.key === 'Escape' && fullScreen && !ev.defaultPrevented) {
      ev.preventDefault();
      setFullScreen(false);
      return;
    }
    // Ask on the keydown too: a prevented Escape makes no close request, so
    // no activation is spent however often the question is dismissed.
    if (ev.key === 'Escape' && dirty && !ev.defaultPrevented) {
      ev.preventDefault();
      void requestClose();
      return;
    }
    if (!(ev.ctrlKey || ev.metaKey) || ev.altKey || ev.key.toLowerCase() !== 's') return;
    ev.preventDefault();
    if (previewOk && !saving) form.current?.requestSubmit();
  }

  return (
    <Dialog open onClose={onClose} onCancel={onCancel} size="wide" title={title}>
      <form
        ref={form}
        aria-label={title}
        data-fullscreen={fullScreen}
        onSubmit={submit}
        onKeyDown={onKeyDown}
        className={fullScreen ? 'fixed inset-0 z-10 flex flex-col gap-3 bg-card p-5' : 'flex flex-col gap-3'}
      >
        <div className="flex flex-col gap-1">
          <div className="flex items-center justify-between gap-2">
            <span id={labelId} className="text-[11px] font-medium text-muted-foreground">
              {e.yaml}
            </span>
            <Button type="button" variant="ghost" className="h-7 px-2 text-[11px] font-medium" aria-pressed={fullScreen} onClick={() => setFullScreen((f) => !f)}>
              {fullScreen ? <Minimize2 size={13} aria-hidden="true" /> : <Maximize2 size={13} aria-hidden="true" />}
              {e.fullScreen}
            </Button>
          </div>
          <CodeEditor
            labelledBy={labelId}
            autoFocus
            language="yaml"
            value={text}
            onChange={setText}
            invalid={!previewOk}
            height={fullScreen ? 'calc(100vh - 11rem)' : '60vh'}
            diagnostics={problem ? [problem] : []}
          />
          {previewOk ? (
            <span role="status" className="text-[11px]" style={{ color: 'var(--px-ok)' }}>
              {e.ready}
            </span>
          ) : (
            <span role="alert" className="text-[11px] text-destructive">
              {problem.message}
            </span>
          )}
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex items-center justify-between gap-2">
          {existing && (
            <Button type="button" variant="destructive" disabled={remove.isPending} onClick={() => void onDelete()}>
              {t.common.delete}
            </Button>
          )}
          <div className="ml-auto flex gap-2">
            <Button type="button" onClick={() => void requestClose()}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary" title={e.saveShortcut} aria-keyshortcuts="Control+S Meta+S" disabled={saving || !previewOk}>
              {e.save}
            </Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}
