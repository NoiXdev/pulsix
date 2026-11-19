import { useState, type FormEvent } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { Play } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import type { Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useRunTemplate } from '../templates/useRunTemplate.tsx';

/**
 * RunPlaybookButton is the playbook page's Run…: a playbook runs through a
 * job template, which brings the hosts, credentials and variables. It
 * always opens a dialog, so one click never starts a run: the person picks
 * the template (the only one already chosen) and Run starts it through the
 * templates list's own flow (follow-ups ask first); with none the dialog
 * says so and links to creating a template with this playbook chosen.
 * While the page's draft is dirty the dialog says that the saved version
 * runs, without the unsaved changes.
 */
export function RunPlaybookButton({
  org,
  playbookId,
  name,
  templates,
  describedBy,
  dirty,
  canCreateTemplate,
}: {
  org: string;
  playbookId: string;
  name: string;
  /** Undefined while they are read (or when they cannot be): the button waits. */
  templates: readonly Template[] | undefined;
  /** The id of the line that says why the button is disabled. */
  describedBy?: string;
  /** The page's draft differs from the saved playbook. */
  dirty: boolean;
  canCreateTemplate: boolean;
}) {
  const t = useT();
  const runner = useRunTemplate(org);
  const [picking, setPicking] = useState(false);

  return (
    <>
      <Button disabled={!templates || runner.isPending} aria-describedby={describedBy} onClick={() => setPicking(true)}>
        <Play size={14} /> {t.playbooks.page.run}
      </Button>
      {picking && templates && (
        <RunDialog
          playbookId={playbookId}
          name={name}
          templates={templates}
          dirty={dirty}
          canCreateTemplate={canCreateTemplate}
          pending={runner.isPending}
          onRun={(tpl) => {
            setPicking(false);
            void runner.run(tpl);
          }}
          onClose={() => setPicking(false)}
        />
      )}
      {runner.dialog}
    </>
  );
}

function RunDialog({
  playbookId,
  name,
  templates,
  dirty,
  canCreateTemplate,
  pending,
  onRun,
  onClose,
}: {
  playbookId: string;
  name: string;
  templates: readonly Template[];
  dirty: boolean;
  canCreateTemplate: boolean;
  pending: boolean;
  onRun: (tpl: Template) => void;
  onClose: () => void;
}) {
  const t = useT();
  const d = t.playbooks.page.runDialog;
  const { org } = useParams({ strict: false }) as { org: string };
  const [id, setId] = useState(templates.length === 1 ? templates[0].id : '');

  function submit(e: FormEvent) {
    e.preventDefault();
    const tpl = templates.find((x) => x.id === id);
    if (tpl) onRun(tpl);
  }

  if (templates.length === 0) {
    return (
      <Dialog open onClose={onClose} title={d.title(name)}>
        <div className="flex flex-col gap-4">
          <p className="text-[13px] text-muted-foreground">{d.none}</p>
          <div className="flex justify-end gap-2">
            <Button onClick={onClose}>{t.common.cancel}</Button>
            {canCreateTemplate && (
              <Link to="/o/$org/templates" params={{ org }} search={{ create: playbookId }} className={buttonClass('primary')}>
                {d.create}
              </Link>
            )}
          </div>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open onClose={onClose} title={d.title(name)}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor="run-playbook-template">{d.template}</Label>
          <Select id="run-playbook-template" options={templates.map((tpl) => ({ value: tpl.id, label: tpl.name }))} value={id} onChange={setId} placeholder={d.choose} />
        </div>
        {dirty && <p className="text-[13px] text-muted-foreground">{d.savedRuns}</p>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={!id || pending}>
            {d.run}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
