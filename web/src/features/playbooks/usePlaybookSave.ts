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
  /** Reads the playbook again (the page's own query); it rejects when the
   * read fails. */
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
 * refusal is the server's message (`serverError`), and so is a failing
 * re-read (no conflict is shown without the text to compare).
 *
 * `pending` covers the read after a 412 as well, so nothing is sent on the
 * old revision before the current one is in. `savedAgain` counts the times
 * Save mine anyway, sent on the revision just read, was refused too:
 * someone saved once more in between.
 */
export function usePlaybookSave({ org, playbook, draft, revision, dirty, markSaved, reload, reread }: PlaybookSaveOptions) {
  const t = useT();
  const toast = useToast();
  const update = useUpdatePlaybook(org, playbook.id);
  const [serverError, setServerError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [savedAgain, setSavedAgain] = useState(0);
  const [rereading, setRereading] = useState(false);
  const clear = () => {
    setConflict(false);
    setSavedAgain(0);
  };
  // A draft made clean again (by hand, or by Reload) has nothing to save.
  if (conflict && !dirty) clear();

  async function send(onRevision: number, anyway: boolean) {
    const sent = draft;
    setServerError(null);
    try {
      const res = await update.mutateAsync({ inline: sent, revision: onRevision });
      markSaved(sent, res.playbook.revision);
      clear();
      toast.show(t.playbooks.editor.saved);
    } catch (err) {
      if (errorStatus(err) !== 412) {
        setServerError(err instanceof Error ? err.message : t.playbooks.editor.saveFailed);
        return;
      }
      setRereading(true);
      try {
        await reread();
      } catch (readErr) {
        // Without the current text there is nothing to compare: the draft
        // stays, and the failed read is the error to show (Save tries again).
        setServerError(readErr instanceof Error ? readErr.message : t.playbooks.editor.saveFailed);
        return;
      } finally {
        setRereading(false);
      }
      setConflict(true);
      if (anyway) setSavedAgain((n) => n + 1);
    }
  }

  return {
    pending: update.isPending || rereading,
    serverError,
    conflict,
    savedAgain,
    save: () => send(revision, false),
    saveAnyway: () => send(playbook.revision, true),
    reload: () => {
      reload();
      clear();
      setServerError(null);
    },
  };
}
