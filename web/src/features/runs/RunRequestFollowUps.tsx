import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { useT } from '../../i18n/useT.ts';
import { shortId } from './status.ts';
import { varText } from './pictureVars.ts';
import type { PictureFollowUp as FollowUp, PictureFollowUps } from './followUps.ts';

/** nodeName is the follow-up's name, or the short id of a template that is
 * gone and left no name. */
function nodeName(f: FollowUp): string {
  return f.template_name || shortId(f.template_id);
}

function Tree({ nodes }: { nodes: FollowUp[] }) {
  const t = useT();
  const d = t.runs.requests.detail.followUps;
  return (
    <ul className="ml-4 flex flex-col gap-1 border-l border-border pl-3">
      {nodes.map((f) => (
        <li key={`${f.template_id}-${f.stop ?? ''}`} className="flex flex-col gap-1">
          <span className="text-xs">
            <span className="font-mono">{nodeName(f)}</span>
            {f.template_revision ? <span className="text-muted-foreground"> · {d.revision(f.template_revision)}</span> : null}
            {f.stop && <span className="text-muted-foreground"> · {stopLabel(f.stop, d.stop)}</span>}
            <CommitNote f={f} />
          </span>
          <Bound f={f} />
          {(f.follow_ups ?? []).length > 0 && <Tree nodes={f.follow_ups ?? []} />}
        </li>
      ))}
    </ul>
  );
}

/** Bound shows what approving binds of a follow-up (backlog 2.143): the hosts
 * it can run on and its extra vars, secrets masked. Nothing for a follow-up
 * the picture does not bind (no vars digest). */
function Bound({ f }: { f: FollowUp }) {
  const t = useT();
  const b = t.runs.requests.detail.followUps.bound;
  if (!f.vars_digest) return null;
  const hosts = (f.targets ?? []).map((h) => h.name || shortId(h.id));
  const vars = Object.entries(f.extra_vars ?? {});
  return (
    <div role="group" aria-label={b.label(nodeName(f))} className="ml-6 flex flex-col text-[11px] text-muted-foreground">
      <span>{hosts.length > 0 ? b.hosts(hosts.join(', ')) : b.noHosts}</span>
      {vars.length > 0 && (
        <span className="break-all">
          {b.vars(vars.map(([name, value]) => `${name} = ${varText(value, t.runs.requests.detail.secret)}`).join(', '))}
        </span>
      )}
    </div>
  );
}

/** CommitNote says what a follow-up of a pinned run runs (spec 2026-11-25 A):
 * the same commit, its repository's current state, or its editor playbook. */
function CommitNote({ f }: { f: FollowUp }) {
  const c = useT().runs.requests.detail.followUps.commit;
  const text = f.commit === 'pinned' ? c.pinned : f.commit === 'current' ? c.current(f.repo ?? '') : f.commit === 'editor' ? c.editor : undefined;
  return text ? <span className="text-muted-foreground"> · {text}</span> : null;
}

function stopLabel(stop: string, labels: Record<string, string>): string {
  return Object.hasOwn(labels, stop) ? labels[stop] : stop;
}

/**
 * RunRequestFollowUps is the approval page's "Follow-ups" section (spec
 * 2026-11-16, approved run follow-ups). With the organization setting off it
 * names the follow-ups an approval leaves out; with it on, every startable
 * direct follow-up has a checkbox (checked unless its id is in `deselected`)
 * and its own follow-ups follow, indented and read-only. The parent keeps the
 * selection; this component only shows it.
 */
export function RunRequestFollowUps({
  followUps,
  deselected,
  onToggle,
  disabled,
}: {
  followUps: PictureFollowUps;
  deselected: ReadonlySet<string>;
  onToggle: (templateId: string) => void;
  disabled: boolean;
}) {
  const t = useT();
  const d = t.runs.requests.detail.followUps;
  const templates = followUps.templates ?? [];

  return (
    <Section id="follow-ups" title={t.runs.requests.detail.section.followUps}>
      <Card>
        <div className="flex flex-col gap-3 p-4">
          {!followUps.start && <p className="text-xs text-muted-foreground">{d.offNote}</p>}
          {followUps.start && followUps.truncated && (
            <p role="status" className="text-xs text-muted-foreground">
              {d.truncated}
            </p>
          )}
          <ul className="flex flex-col gap-2">
            {templates.map((f) => {
              const checkable = followUps.start && !followUps.truncated && !f.stop;
              const name = nodeName(f);
              return (
                <li key={`${f.template_id}-${f.stop ?? ''}`} className="flex flex-col gap-1">
                  {checkable ? (
                    <label className="flex items-center gap-2 text-xs">
                      <input
                        type="checkbox"
                        checked={!deselected.has(f.template_id)}
                        disabled={disabled}
                        onChange={() => onToggle(f.template_id)}
                        aria-label={name}
                      />
                      <span className="font-mono">{name}</span>
                      {f.template_revision ? <span className="text-muted-foreground">· {d.revision(f.template_revision)}</span> : null}
                      <CommitNote f={f} />
                    </label>
                  ) : (
                    <span className="text-xs">
                      <span className="font-mono">{name}</span>
                      {followUps.start && f.template_revision ? (
                        <span className="text-muted-foreground"> · {d.revision(f.template_revision)}</span>
                      ) : null}
                      {f.stop && <span className="text-muted-foreground"> · {stopLabel(f.stop, d.stop)}</span>}
                      <CommitNote f={f} />
                    </span>
                  )}
                  <Bound f={f} />
                  {followUps.start && (f.follow_ups ?? []).length > 0 && <Tree nodes={f.follow_ups ?? []} />}
                </li>
              );
            })}
          </ul>
          {followUps.start && <span className="text-[11px] text-muted-foreground">{d.hint}</span>}
        </div>
      </Card>
    </Section>
  );
}
