import { Dialog } from '../../ui/dialog.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useTemplate } from '../../api/hooks/useTemplates.ts';
import { useT } from '../../i18n/useT.ts';
import { existingOf } from './existing.ts';
import { TemplateForm } from './TemplateForm.tsx';
import { sshCredentials } from './sshCredentials.ts';

export interface TemplateDialogProps {
  org: string;
  /** The template to edit; absent means create. */
  template?: { id: string; name: string };
  canReadCredentials: boolean;
  /** Gates the Default SSH credential picker's own create row:
   * credentials.write is admin, stricter than templates.write (v0.4g
   * Task 7 fix round). */
  canCreateCredential: boolean;
  /** Gates the Enrol agents switch: changing it needs agents.write (spec
   * 2026-11-25 B). Without it the switch shows the stored value. */
  canEnrolAgents?: boolean;
  /** Create only: the playbook the new template starts with (the playbook
   * page's "Create template…"). */
  initialPlaybookId?: string;
  onClose: () => void;
}

/**
 * TemplateDialog creates a job template, or edits one from a list, in the
 * settings form the template page's Settings tab shows too (TemplateForm:
 * its fields, validation, If-Match save and Reload after a conflict). An
 * edit reads the template first and links its variables page.
 */
export function TemplateDialog({ org, template, canReadCredentials, canCreateCredential, canEnrolAgents = false, initialPlaybookId, onClose }: TemplateDialogProps) {
  const t = useT();
  const detail = useTemplate(org, template?.id ?? '');
  const playbooks = usePlaybooks(org);
  const credentials = useCredentials(org, { enabled: canReadCredentials });
  const guard = useQueryGuard(detail);
  const allCredentials = credentials.data?.credentials ?? [];
  const common = {
    org,
    playbooks: playbooks.data?.playbooks ?? [],
    credentials: sshCredentials(allCredentials),
    vaultCredentials: allCredentials,
    canCreateCredential,
    canEnrolAgents,
    variant: 'dialog' as const,
    onCancel: onClose,
    onDone: onClose,
  };

  if (template) {
    const tpl = detail.data?.template;
    return (
      <Dialog open onClose={onClose} size={tpl && !guard ? 'wide' : undefined} title={tpl && !guard ? tpl.name : template.name}>
        {guard ??
          (tpl ? (
            <TemplateForm
              {...common}
              existing={existingOf(tpl)}
              onReload={async () => {
                const fresh = (await detail.refetch()).data?.template;
                return fresh ? existingOf(fresh) : undefined;
              }}
            />
          ) : (
            <QueryLoading />
          ))}
      </Dialog>
    );
  }
  return (
    <Dialog open onClose={onClose} size="wide" title={t.templates.dialog.newTitle}>
      <TemplateForm {...common} initialPlaybookId={initialPlaybookId} />
    </Dialog>
  );
}
