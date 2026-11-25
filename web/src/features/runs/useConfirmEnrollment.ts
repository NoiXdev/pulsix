import { useConfirm } from '../../ui/confirmContext.ts';
import { useT } from '../../i18n/useT.ts';

/** EnrollmentPreview is what a run or retry started now enrols the agent
 * on, as the enrollment-preview routes answer. */
export interface EnrollmentPreview {
  enrols: boolean;
  hosts: { id: string; name: string }[] | null;
}

/**
 * useConfirmEnrollment asks the person to confirm the hosts a run of a
 * template with agent enrollment enrols the agent on (spec 2026-11-25 B):
 * the start sends exactly the confirmed ids, and the server refuses it when
 * they are no longer the targets without an agent. It resolves the ids to
 * send, `undefined` for a template that enrols nothing (send no field), or
 * `null` when the person cancelled. A list without a host needs no
 * question: the run enrols none.
 */
export function useConfirmEnrollment(): (preview: EnrollmentPreview) => Promise<string[] | undefined | null> {
  const ask = useConfirm();
  const e = useT().runs.enrollment;
  return async (preview) => {
    if (!preview.enrols) return undefined;
    const hosts = preview.hosts ?? [];
    if (hosts.length === 0) return [];
    const ok = await ask({
      title: e.title,
      message: e.confirm(hosts.map((h) => h.name).join(', ')),
      confirmLabel: e.start,
    });
    return ok ? hosts.map((h) => h.id) : null;
  };
}
