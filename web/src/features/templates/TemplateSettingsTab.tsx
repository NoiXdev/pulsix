import { Pencil } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import type { Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { existingOf } from './existing.ts';
import { sshCredentials } from './sshCredentials.ts';
import { TemplateForm } from './TemplateForm.tsx';

export interface TemplateSettingsTabProps {
  org: string;
  template: Template;
  role: string;
  /** Whether the form is editable; the page owns it, so the header's Edit
   * can open the tab editing. */
  editing: boolean;
  onEditingChange: (editing: boolean) => void;
  /** Reads the template again (Reload after a conflict). */
  onReload: () => Promise<Template | undefined>;
}

/**
 * TemplateSettingsTab is the template's settings form inline (the form the
 * template dialog shows, TemplateForm): read-only as saved until Edit,
 * then Cancel and Save — the same save, under If-Match, with Reload after a
 * conflict. Edit needs `templates.write`; without it the form only shows.
 *
 * The read-only form is mounted again for every new revision, so it always
 * shows the template as saved; the editing one keeps the revision it was
 * opened on as its base, whatever a background read brings meanwhile.
 */
export function TemplateSettingsTab({ org, template, role, editing, onEditingChange, onReload }: TemplateSettingsTabProps) {
  const t = useT();
  const s = t.templates.page.settings;
  const canWrite = may(role, 'templates.write');
  const canReadCredentials = may(role, 'credentials.read');
  const playbooks = usePlaybooks(org);
  const credentials = useCredentials(org, { enabled: canReadCredentials });
  const allCredentials = credentials.data?.credentials ?? [];
  const editable = editing && canWrite;

  return (
    <div className="flex flex-col gap-4">
      {!editable && (
        <div className="flex max-w-3xl flex-wrap items-center justify-between gap-3">
          <p className="text-[13px] text-muted-foreground">{canWrite ? s.readOnly : s.noAccess}</p>
          {canWrite && (
            <Button onClick={() => onEditingChange(true)}>
              <Pencil size={14} /> {s.edit}
            </Button>
          )}
        </div>
      )}
      <TemplateForm
        key={editable ? 'edit' : `view-${template.revision}`}
        org={org}
        existing={existingOf(template)}
        onReload={async () => {
          const fresh = await onReload();
          return fresh ? existingOf(fresh) : undefined;
        }}
        playbooks={playbooks.data?.playbooks ?? []}
        credentials={sshCredentials(allCredentials)}
        vaultCredentials={allCredentials}
        canCreateCredential={may(role, 'credentials.write')}
        canEnrolAgents={may(role, 'agents.write')}
        variant="page"
        readOnly={!editable}
        onCancel={() => onEditingChange(false)}
        onDone={() => onEditingChange(false)}
      />
    </div>
  );
}
