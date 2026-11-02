import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useCreatePlaybook, useDeletePlaybook, usePlaybook, useUpdatePlaybook } from '../../api/hooks/usePlaybooks.ts';
import { validateYAML } from './validate.ts';
import { useT } from '../../i18n/useT.ts';

export interface PlaybookEditorProps {
  org: string;
  /** The editor playbook to edit; absent means create. */
  playbook?: { id: string; summary: string };
  onClose: () => void;
}

/**
 * PlaybookEditor is a plain controlled textarea in JetBrains Mono with the
 * server's validation ported as a client-side preview (validate.ts — the
 * same messages), so an operator sees the problem before submitting; the
 * server stays the authority and its rejected detail shows inline too.
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
  const toast = useToast();
  const create = useCreatePlaybook(org);
  const update = useUpdatePlaybook(org, existing?.id ?? '');
  const remove = useDeletePlaybook(org);
  const [text, setText] = useState(existing?.inline ?? '');
  const [error, setError] = useState<string | null>(null);

  // The preview runs on every keystroke; a document outside the minimal
  // parser's subset yields no message (the server judges those).
  const preview = validateYAML(text, t);
  const previewOk = preview === null;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const message = validateYAML(text, t);
    if (message) return; // the preview already names it
    try {
      if (existing) await update.mutateAsync({ inline: text });
      else await create.mutateAsync({ inline: text });
      toast.show(existing ? t.playbooks.editor.saved : t.playbooks.editor.created);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.playbooks.editor.saveFailed);
    }
  }

  async function onDelete() {
    if (!confirm(t.playbooks.editor.confirmDelete(existing?.summary || t.playbooks.editor.fallbackName))) return;
    try {
      await remove.mutateAsync(existing?.id ?? '');
      toast.show(t.playbooks.editor.deleted);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.playbooks.editor.deleteFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={existing?.summary || t.playbooks.editor.newTitle}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <label htmlFor="playbook-yaml" className="text-[11px] font-medium text-muted-foreground">
            {t.playbooks.editor.yaml}
          </label>
          <textarea
            id="playbook-yaml"
            rows={14}
            spellCheck={false}
            className="rounded-md border border-border bg-background p-3 font-mono text-xs text-foreground focus-visible:border-primary focus-visible:outline-none"
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          {previewOk ? (
            <span role="status" className="text-[11px]" style={{ color: 'var(--px-ok)' }}>
              {t.playbooks.editor.ready}
            </span>
          ) : (
            <span role="alert" className="text-[11px] text-destructive">
              {preview}
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
          <div className="flex gap-2">
            <Button type="button" onClick={onClose}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary" disabled={create.isPending || update.isPending || !previewOk}>
              {t.playbooks.editor.save}
            </Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}
