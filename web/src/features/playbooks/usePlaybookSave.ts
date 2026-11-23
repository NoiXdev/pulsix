import { useState } from 'react';
import { useUpdatePlaybook } from '../../api/hooks/usePlaybooks.ts';
import type { Playbook } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { useToast } from '../../ui/toastContext.ts';

interface PlaybookSaveOptions {
  org: string;
  /** The playbook as stored now (its text and revision). */
  playbook: Playbook;
  /** The draft, the revision its base came from, and whether it differs
   * from that base (usePlaybookDraft). */
  draft: string;
  revision: number;
  dirty: boolean;
  markSaved: (text: string, revision: number) => void;
  reload: () => void;
  /** Reads the playbook again (the page's own query). */
  reread: () => Promise<unknown>;
}

/**
 * usePlaybookSave saves an editor playbook's draft on the revision its base
 * came from (If-Match, backlog 3.127). The server refusing it as stale
 * (412: someone saved meanwhile) is a conflict, not an error: the draft
 * stays, the playbook is read again for the current text and revision
 * (the 412 carries neither), and `conflict` holds until the person reloads,
 * saves anyway (on the revision just read: a newer save still answers
 * 412), saves successfully, or makes the draft clean again. Any other
 * refusal is the server's message (`serverError`).
 */
export function usePlaybookSave({ org, playbook, draft, revision, dirty, markSaved, reload, reread }: PlaybookSaveOptions) {
  const t = useT();
  const toast = useToast();
  const update = useUpdatePlaybook(org, playbook.id);
  const [serverError, setServerError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  // A draft made clean again (by hand, or by Reload) has nothing to save.
  if (conflict && !dirty) setConflict(false);

  async function send(onRevision: number) {
    const sent = draft;
    setServerError(null);
    try {
      const res = await update.mutateAsync({ inline: sent, revision: onRevision });
      markSaved(sent, res.playbook.revision);
      setConflict(false);
      toast.show(t.playbooks.editor.saved);
    } catch (err) {
      if (errorStatus(err) !== 412) {
        setServerError(err instanceof Error ? err.message : t.playbooks.editor.saveFailed);
        return;
      }
      await reread();
      setConflict(true);
    }
  }

  return {
    pending: update.isPending,
    serverError,
    conflict,
    save: () => send(revision),
    saveAnyway: () => send(playbook.revision),
    reload: () => {
      reload();
      setConflict(false);
      setServerError(null);
    },
  };
}
