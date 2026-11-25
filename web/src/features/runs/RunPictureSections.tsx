import { Link } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { Section, SidebarFacts } from '../../ui/detail-layout.tsx';
import type { RunPicture } from '../../api/hooks/useRunRequests.ts';
import { useT } from '../../i18n/useT.ts';
import { shortId } from './status.ts';
import { CommitId } from './CommitId.tsx';

const NONE = '—';

/** isMaskedSecret reports a value the server masked: a secret extra var
 * arrives as {"$secret": "set"}, never with its value. */
function isMaskedSecret(value: unknown): boolean {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    Object.keys(value).length === 1 &&
    (value as Record<string, unknown>).$secret === 'set'
  );
}

/** varText renders an extra var's value: a string as it is, anything else
 * as JSON, with every masked secret inside it replaced by `secretLabel`. */
function varText(value: unknown, secretLabel: string): string {
  if (isMaskedSecret(value)) return secretLabel;
  if (typeof value === 'string') return value;
  return JSON.stringify(value, (_key, v: unknown) => (isMaskedSecret(v) ? secretLabel : v));
}

function Mono({ children }: { children: string | number | undefined | null }) {
  return <span className="font-mono">{children === undefined || children === null || children === '' ? NONE : children}</span>;
}

const TABLE = 'w-full text-xs';
const TH = 'px-4 py-2 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase';
const TD = 'border-t border-border px-4 py-2';

/** VarsTable lists extra vars by name and value; `replaced` marks the names
 * this request sets itself. */
function VarsTable({
  vars,
  label,
  replaced,
}: {
  vars: [string, unknown][];
  label?: string;
  replaced?: Record<string, unknown>;
}) {
  const d = useT().runs.requests.detail;
  return (
    <table className={TABLE} aria-label={label}>
      <thead>
        <tr>
          <th className={TH}>{d.varName}</th>
          <th className={TH}>{d.varValue}</th>
        </tr>
      </thead>
      <tbody>
        {vars.map(([name, value]) => (
          <tr key={name}>
            <td className={`${TD} font-mono`}>{name}</td>
            <td className={`${TD} font-mono break-all ${isMaskedSecret(value) ? 'text-muted-foreground italic' : ''}`}>
              {varText(value, d.secret)}
              {replaced && Object.hasOwn(replaced, name) && (
                <span className="ml-2 font-sans text-[11px] text-[var(--px-warn)] not-italic">{d.varReplaced}</span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** DryRunBanner says, above everything else, that the run is a dry run. */
function DryRunBanner({ diff }: { diff: boolean }) {
  const d = useT().runs.requests.detail.dryRun;
  const title = d.title(diff);
  return (
    <div role="note" aria-label={title} className="mb-4 rounded-md border border-[var(--px-warn)] px-4 py-3 text-xs">
      <p className="font-semibold text-[var(--px-warn)]">{title}</p>
      <p className="mt-1 text-muted-foreground">{d.body}</p>
    </div>
  );
}

type CommitBlockedReason = NonNullable<NonNullable<RunPicture['commit']>['blocked']>[number];

/** CommitBlockedNote says why the request cannot be approved yet: the
 * repository needs a sync first, or one is running (spec 2026-11-25 A). */
function CommitBlockedNote({ reasons }: { reasons: CommitBlockedReason[] }) {
  const b = useT().runs.requests.detail.commitBlocked;
  return (
    <div role="alert" aria-label={b.title} className="rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs">
      <p className="font-semibold text-[var(--px-warn)]">{b.title}</p>
      <ul className="mt-1 flex flex-col gap-1">
        {reasons.map((r) => (
          <li key={r}>{Object.hasOwn(b.reasons, r) ? b.reasons[r] : r}</li>
        ))}
      </ul>
    </div>
  );
}

/** EnrollmentSection says which hosts the run enrols agents on (spec
 * 2026-11-25 B): live while pending, as approved afterwards; a dry run
 * enrols none. */
function EnrollmentSection({ enrollment, status }: { enrollment: NonNullable<RunPicture['agent_enrollment']>; status: string }) {
  const d = useT().runs.requests.detail;
  const e = d.enrollment;
  const hosts = enrollment.hosts ?? [];
  const pending = status === 'pending';
  let line: string;
  if (enrollment.none_reason === 'dry_run') line = e.dryRun;
  else if (hosts.length === 0) line = e.none;
  else line = pending ? e.willEnrol(hosts.join(', ')) : e.enrolled(hosts.join(', '));
  return (
    <Section id="enrollment" title={d.section.enrollment}>
      <div className="flex flex-col gap-1 text-xs">
        <p className="font-semibold">{line}</p>
        {hosts.length > 0 && <p className="text-muted-foreground">{e.how}</p>}
        {pending && hosts.length > 0 && <p className="text-muted-foreground">{e.live}</p>}
      </div>
    </Section>
  );
}

/**
 * RunPictureSections shows what a run request's run would use if it were
 * approved now, in the order a reviewer reads it: the template, the
 * playbook and its repository, the target hosts, the credentials by name
 * (never their contents) and the extra vars with secrets masked, and for a
 * retry the run it repeats and the hosts that failed there. What the request
 * changes about the template's run shows with each part: a dry run as a
 * banner above all, its limit with the template's, the hosts it narrows to
 * over the targets and its extra vars (never secret) by value.
 */
export function RunPictureSections({ org, picture, status }: { org: string; picture: RunPicture; status: string }) {
  const t = useT();
  const d = t.runs.requests.detail;
  const s = d.section;
  const targets = picture.targets ?? [];
  const vault = picture.vault_identities ?? [];
  const vars = Object.entries(picture.extra_vars ?? {});
  const ownVars = picture.extra_vars_override;
  const requested = picture.requested_host_ids ?? [];
  const gone = requested.filter((id) => !targets.some((h) => h.id === id));
  const onlyHost = picture.only_host ? (targets.find((h) => h.id === picture.only_host)?.name ?? shortId(picture.only_host)) : undefined;
  const source = picture.playbook.source;
  const sourceLabel = Object.hasOwn(d.sources, source) ? d.sources[source as keyof typeof d.sources] : source;
  const commit = picture.commit;
  // What runs, and what holds the approval, only matter while the request
  // can still be approved; an approved one ran its stored commit; a
  // rejected or expired one shows the commit alone.
  const pending = status === 'pending';
  const blocked = pending ? (commit?.blocked ?? []) : [];
  const commitNote = pending
    ? commit?.source === 'original_run'
      ? d.commitOriginal
      : d.commitLive
    : status === 'approved'
      ? d.commitPinned
      : undefined;

  return (
    <>
      {picture.check_mode && <DryRunBanner diff={!!picture.diff} />}
      <Section id="template" title={s.template}>
        <SidebarFacts
          items={[
            { label: d.templateName, value: <Mono>{picture.template_name}</Mono> },
            { label: d.revision, value: <Mono>{picture.template_revision}</Mono> },
            { label: d.limit, value: picture.limit ? <Mono>{picture.limit}</Mono> : d.wholeScope },
            ...(picture.limit_override ? [{ label: d.limitOverride, value: <Mono>{picture.limit_override}</Mono> }] : []),
            ...(onlyHost ? [{ label: d.onlyHost, value: <Mono>{onlyHost}</Mono> }] : []),
          ]}
        />
      </Section>

      <Section id="playbook" title={s.playbook}>
        <SidebarFacts
          items={[
            { label: d.playbookSource, value: sourceLabel },
            ...(picture.playbook.path ? [{ label: d.playbookPath, value: <Mono>{picture.playbook.path}</Mono> }] : []),
            ...(picture.playbook.inline_digest
              ? [{ label: d.inlineDigest, value: <Mono>{picture.playbook.inline_digest}</Mono> }]
              : []),
            ...(picture.repo
              ? [
                  { label: d.repository, value: <Mono>{picture.repo.name}</Mono> },
                  { label: d.url, value: <Mono>{picture.repo.url}</Mono> },
                  { label: d.branch, value: <Mono>{picture.repo.branch}</Mono> },
                  { label: d.subpath, value: <Mono>{picture.repo.subpath}</Mono> },
                  // The credential the repository is read with is part of the fingerprint, so it is shown too.
                  ...(picture.repo.git_credential
                    ? [{ label: d.gitCredential, value: <Mono>{picture.repo.git_credential}</Mono> }]
                    : []),
                ]
              : []),
            ...(commit
              ? [
                  {
                    label: d.commit,
                    value: (
                      <span className="flex flex-col gap-0.5">
                        {commit.sha ? <CommitId sha={commit.sha} /> : <span className="text-muted-foreground">{d.commitUnknown}</span>}
                        {commitNote && <span className="text-[11px] text-muted-foreground">{commitNote}</span>}
                      </span>
                    ),
                  },
                ]
              : []),
          ]}
        />
        {blocked.length > 0 && (
          <div className="mt-3">
            <CommitBlockedNote reasons={blocked} />
          </div>
        )}
      </Section>

      <Section id="targets" title={s.targets}>
        {requested.length > 0 && (
          <div className="mb-2 flex flex-col gap-1 text-xs">
            <p>{d.narrowedHosts(requested.length)}</p>
            {gone.length > 0 && <p className="text-[var(--px-warn)]">{d.hostsGone(gone.join(', '))}</p>}
          </div>
        )}
        <Card>
          {targets.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{d.noTargets}</p>
          ) : (
            <table className={TABLE}>
              <thead>
                <tr>
                  <th className={TH}>{d.targetColumns.host}</th>
                  <th className={TH}>{d.targetColumns.address}</th>
                  <th className={TH}>{d.targetColumns.user}</th>
                  <th className={TH}>{d.targetColumns.port}</th>
                  <th className={TH}>{d.targetColumns.jumpHost}</th>
                  <th className={TH}>{d.targetColumns.credential}</th>
                </tr>
              </thead>
              <tbody>
                {targets.map((h) => (
                  <tr key={h.id}>
                    <td className={`${TD} font-semibold`}>{h.name}</td>
                    <td className={TD}>
                      <Mono>{h.address}</Mono>
                    </td>
                    <td className={TD}>
                      <Mono>{h.ssh_user}</Mono>
                    </td>
                    <td className={TD}>
                      <Mono>{h.ssh_port}</Mono>
                    </td>
                    <td className={TD}>
                      <Mono>{h.jump_host}</Mono>
                    </td>
                    <td className={TD}>
                      <Mono>{h.credential}</Mono>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      </Section>

      {picture.agent_enrollment && <EnrollmentSection enrollment={picture.agent_enrollment} status={status} />}

      <Section id="credentials" title={s.credentials}>
        <SidebarFacts
          items={[
            { label: d.sshCredential, value: picture.ssh_credential ? <Mono>{picture.ssh_credential}</Mono> : d.noSshCredential },
            {
              label: d.vaultIdentities,
              value:
                vault.length === 0 ? (
                  d.noVaultIdentities
                ) : (
                  <ul className="flex flex-col gap-1">
                    {vault.map((v) => (
                      <li key={v.label} className="flex gap-2">
                        <Mono>{v.label}</Mono>
                        <span className="text-muted-foreground">→</span>
                        <Mono>{v.credential}</Mono>
                      </li>
                    ))}
                  </ul>
                ),
            },
          ]}
        />
      </Section>

      <Section id="vars" title={s.vars}>
        {ownVars && Object.keys(ownVars).length > 0 && (
          <div className="mb-3 flex flex-col gap-1">
            <span className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">{d.varsOverride}</span>
            <Card>
              <VarsTable vars={Object.entries(ownVars)} label={d.varsOverride} />
            </Card>
          </div>
        )}
        <Card>
          {vars.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{d.noVars}</p>
          ) : (
            <VarsTable vars={vars} replaced={ownVars} />
          )}
        </Card>
      </Section>

      {picture.retry && (
        <Section id="retry" title={s.retry}>
          <SidebarFacts
            items={[
              {
                label: d.retryJob,
                value: (
                  <Link to="/o/$org/runs/$id" params={{ org, id: picture.retry.job_id }} className="font-mono hover:text-primary">
                    {shortId(picture.retry.job_id)}
                  </Link>
                ),
              },
              {
                label: d.failedHosts,
                value: (
                  <ul className="flex flex-col gap-1">
                    {(picture.retry.failed_hosts ?? []).map((h) => (
                      <li key={h} className="font-mono">
                        {h}
                      </li>
                    ))}
                  </ul>
                ),
              },
            ]}
          />
        </Section>
      )}
    </>
  );
}
