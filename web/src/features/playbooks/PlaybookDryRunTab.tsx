import { useId, useState, type FormEvent } from 'react';
import { Link } from '@tanstack/react-router';
import { FlaskConical, TriangleAlert } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useDryRunPlaybook } from '../../api/hooks/usePlaybooks.ts';
import type { Playbook, PlaybookDryRunInput, PlaybookOutline, Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { DryRunHosts } from './DryRunHosts.tsx';
import { DryRunResult } from './DryRunResult.tsx';
import { usePlaybookOutline } from './usePlaybookOutline.ts';

interface PlaybookDryRunTabProps {
  org: string;
  playbook: Playbook;
  role: string;
  /** The templates using the playbook; undefined while they are read. */
  templates: readonly Template[] | undefined;
  /** The editor's text and the stored one (editor playbooks), and whether
   * they differ for someone who may save. */
  draft: string;
  saved: string;
  dirty: boolean;
}

/**
 * PlaybookDryRunTab is the playbook page's Dry run tab (spec §4): what a
 * dry run is, the form that starts one through a template (hosts from the
 * template's scope, cleared when another template is chosen; tags
 * from the outline, differences, the unsaved draft) and the warning about
 * the tasks that run for real anyway; below it the latest dry run of the
 * playbook — after Start, the one just started. Without `jobs.run` a
 * notice takes the form's place; the latest dry run still shows.
 */
export function PlaybookDryRunTab({ org, playbook, role, templates, draft, saved, dirty }: PlaybookDryRunTabProps) {
  const t = useT();
  const [started, setStarted] = useState<string | null>(null);
  return (
    <div className="flex max-w-4xl flex-col gap-5">
      {may(role, 'jobs.run') ? (
        <DryRunForm org={org} playbook={playbook} role={role} templates={templates} draft={draft} saved={saved} dirty={dirty} onStarted={setStarted} />
      ) : (
        <DisabledHint title={t.playbooks.page.dryRun.title} body={t.playbooks.page.dryRun.noAccess} />
      )}
      <DryRunResult org={org} playbookId={playbook.id} startedId={started} />
    </div>
  );
}

function DryRunForm({ org, playbook, role, templates, draft, saved, dirty, onStarted }: PlaybookDryRunTabProps & { onStarted: (id: string) => void }) {
  const t = useT();
  const d = t.playbooks.page.dryRun;
  const toast = useToast();
  const start = useDryRunPlaybook(org, playbook.id);
  const tagsId = useId();
  const editorSource = playbook.source === 'editor';
  const [templateId, setTemplateId] = useState('');
  const [hostIds, setHostIds] = useState<string[]>([]);
  const [tags, setTags] = useState<string[]>([]);
  const [diff, setDiff] = useState(true);
  const [useDraft, setUseDraft] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const list = templates ?? [];
  // The one template there is counts as chosen.
  const chosen = templateId || (list.length === 1 ? list[0].id : '');
  const canDraft = editorSource && dirty && may(role, 'playbooks.write');
  const checkDraft = canDraft && useDraft;
  // The outline of the text that will run: the draft when it is checked
  // (or equal to the saved text), else the saved one.
  const { outline } = usePlaybookOutline(org, checkDraft || !dirty ? draft : saved, editorSource);
  const available = outline?.tags ?? [];

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!chosen || start.isPending) return;
    setError(null);
    const body: PlaybookDryRunInput = { template_id: chosen, diff };
    if (hostIds.length > 0) body.host_ids = hostIds;
    const chosenTags = tags.filter((tag) => available.includes(tag));
    if (chosenTags.length > 0) body.tags = chosenTags;
    if (checkDraft) body.draft = draft;
    try {
      const { jobs } = await start.mutateAsync(body);
      toast.show(d.started);
      if (jobs && jobs.length > 0) onStarted(jobs[0].id);
    } catch (err) {
      setError(err instanceof Error ? err.message : d.failed);
    }
  }

  const toggle = (values: string[], v: string) => (values.includes(v) ? values.filter((x) => x !== v) : [...values, v]);

  return (
    <form aria-label={d.title} onSubmit={(e) => void submit(e)} className="flex flex-col gap-4 rounded-[10px] border border-border bg-card px-4 py-4">
      <h2 className="text-sm font-semibold">{d.title}</h2>
      <p className="text-[13px] text-muted-foreground">{d.intro}</p>

      <div className="flex flex-col gap-1">
        <Label htmlFor="dry-run-template">{d.template}</Label>
        <Select
          id="dry-run-template"
          options={list.map((tpl) => ({ value: tpl.id, label: tpl.name }))}
          value={chosen}
          onChange={(id) => {
            // Another template has other hosts: the choice starts over.
            if (id !== chosen) setHostIds([]);
            setTemplateId(id);
          }}
          placeholder={d.chooseTemplate}
        />
        {templates && list.length === 0 && (
          <p className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
            <span>{d.noTemplates}</span>
            {may(role, 'templates.write') && (
              <Link to="/o/$org/templates" params={{ org }} search={{ create: playbook.id }} className="font-medium text-primary hover:underline">
                {d.createTemplate}
              </Link>
            )}
          </p>
        )}
      </div>

      <DryRunHosts org={org} template={list.find((tpl) => tpl.id === chosen)} selected={hostIds} onToggle={(h) => setHostIds((s) => toggle(s, h))} />

      {available.length > 0 && (
        <div className="flex flex-col gap-1">
          <span id={tagsId} className="text-xs font-medium">
            {d.tags}
          </span>
          <div role="group" aria-labelledby={tagsId} className="flex flex-wrap gap-1.5">
            {available.map((tag) => {
              const on = tags.includes(tag);
              return (
                <button
                  key={tag}
                  type="button"
                  aria-pressed={on}
                  onClick={() => setTags((s) => toggle(s, tag))}
                  className={`rounded-md border px-2 py-0.5 font-mono text-[11px] ${on ? 'border-primary bg-primary/10 text-primary' : 'border-border bg-background text-muted-foreground hover:text-foreground'}`}
                >
                  {tag}
                </button>
              );
            })}
          </div>
          <span className="text-[11px] text-muted-foreground">{d.tagsHint}</span>
        </div>
      )}

      <label className="flex items-center gap-2 text-xs">
        <input type="checkbox" checked={diff} onChange={(e) => setDiff(e.target.checked)} />
        {d.diff}
      </label>
      {canDraft && (
        <div className="flex flex-col gap-0.5">
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" checked={useDraft} onChange={(e) => setUseDraft(e.target.checked)} />
            {d.draft}
          </label>
          <span className="pl-6 text-[11px] text-muted-foreground">{d.draftHint}</span>
        </div>
      )}

      <RealTasks outline={outline} />

      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <div>
        <Button type="submit" variant="primary" disabled={!chosen || start.isPending}>
          <FlaskConical size={14} /> {d.start}
        </Button>
      </div>
    </form>
  );
}

/** The entries of the outline that run for real even in check mode. */
function RealTasks({ outline }: { outline: PlaybookOutline | null }) {
  const t = useT();
  const d = t.playbooks.page.dryRun;
  const id = useId();
  const real = (outline?.plays ?? []).flatMap((p) => p.entries ?? []).filter((e) => !e.runs_in_check_mode);
  if (real.length === 0) return null;
  return (
    <div
      className="flex flex-col gap-1 rounded-md border p-3 text-xs"
      style={{ borderColor: 'color-mix(in srgb, var(--px-warn) 33%, transparent)', background: 'color-mix(in srgb, var(--px-warn) 10%, transparent)', color: 'var(--px-warn)' }}
    >
      <span id={id} className="flex items-center gap-1.5 font-medium">
        <TriangleAlert size={14} aria-hidden /> {d.real}
      </span>
      <ul aria-labelledby={id} className="list-disc pl-6">
        {real.map((e, i) => (
          <li key={`${e.line}-${i}`}>{d.realItem(e.name || t.playbooks.page.outline.unnamed, e.line)}</li>
        ))}
      </ul>
    </div>
  );
}
