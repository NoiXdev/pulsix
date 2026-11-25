import { useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { DetailLayout, SidebarFacts, SidebarHeading, type SectionNavItem } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import {
  RUN_REQUEST_CHANGED,
  firstJobOfRunGroup,
  useApproveRunRequest,
  useRejectRunRequest,
  useRunRequest,
} from '../../api/hooks/useRunRequests.ts';
import { useMembers } from '../../api/hooks/useMembers.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { RunPictureSections } from './RunPictureSections.tsx';
import { RunRequestFollowUps } from './RunRequestFollowUps.tsx';
import { ScheduleRequestSections } from './ScheduleRequestSections.tsx';
import { TriggerRequestSections } from './TriggerRequestSections.tsx';
import { TriggerSecretDialog } from '../triggers/TriggerSecretDialog.tsx';
import { startableFollowUps } from './followUps.ts';
import { requestTone, useNow } from './runRequestStatus.ts';
import { shortId } from './status.ts';

/**
 * RunRequestDetailPage is where a person decides on a run an assistant
 * asked for (spec 2026-11-16, "Run requests"): the sidebar holds the reason,
 * who asked and when, and — for a role with jobs.run, while the request is
 * pending — Approve and Reject; the sections show the whole run picture, what
 * the run would use if it were approved now.
 *
 * Approve sends the fingerprint of the picture this page loaded. The page
 * remembers the fingerprint the person reviewed (the first one it showed):
 * when a refetch brings another one (a template, host or credential was
 * edited in between), or the server answers an approval with "changed",
 * it shows the changed notice and holds Approve until the person accepts
 * the new version as reviewed, instead of approving anything they have not
 * seen. After an approval it opens the run the approval started.
 *
 * "Requested by" names the token that asked and, under it, its owner. A
 * token that can no longer ask for runs (revoked, expired, deleted) gets a
 * hint, not a block: the approver decides the run themselves.
 *
 * A request made with sync_first waits for its repository's sync first:
 * the page says so and links the sync, offers only Reject, and once the
 * sync failed shows its error (spec 2026-11-25 A).
 *
 * A schedule request (spec 2026-11-25 C) shows the schedule as it is and
 * as the approval leaves it instead of a run picture; approving it needs
 * templates.write as well, starts no run, and opens the schedule. A
 * webhook trigger request does the same for a trigger (repos.write for a
 * repository sync trigger); when its approval draws a secret, the page
 * shows it once in a dialog and forgets it when the dialog closes.
 */
export function RunRequestDetailPage() {
  const t = useT();
  const d = t.runs.requests.detail;
  const fmt = useFormat();
  const now = useNow();
  const toast = useToast();
  const ask = useConfirm();
  const navigate = useNavigate();
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const detail = useRunRequest(org, id);
  const orgInfo = useOrg(org);
  const members = useMembers(org);
  const approve = useApproveRunRequest(org, id);
  const reject = useRejectRunRequest(org, id);
  // The review of one request: the fingerprint of the picture the person
  // reviewed (the first one the page showed, until they accept a newer one)
  // and whether the server refused an approval as changed. It names its
  // request, as the page stays mounted when it moves on to another one, and
  // starts afresh there.
  const [review, setReview] = useState<{ id: string; fingerprint: string; refused: boolean; waiting?: boolean }>();
  // The deselected direct follow-ups belong to one loaded picture: a new
  // fingerprint (a refetch after a change) starts again from "all checked".
  const [deselection, setDeselection] = useState<{ fingerprint: string; ids: ReadonlySet<string> }>({
    fingerprint: '',
    ids: new Set(),
  });
  // A trigger's secret an approval drew: held only while its dialog is open.
  const [drawn, setDrawn] = useState<{ secret: string; url: string }>();

  const guard = useQueryGuard(detail);
  if (guard) return guard;
  if (!detail.data) return <QueryLoading />;

  const { request, picture, fingerprint } = detail.data;
  // The first picture shown is the one reviewed (state set during render,
  // React's way of deriving state from what arrives). While the request
  // waits for its sync nothing can be approved, so the review starts with
  // the picture shown once it is pending.
  const reviewHere = review?.id === id ? review : undefined;
  // Nothing can be approved while the request waits for its sync or its
  // commit is blocked, so the review starts again once it may be approved.
  const waitsNow = request.status === 'waiting_for_sync' || (picture.commit?.blocked ?? []).length > 0;
  const reviewStarts =
    !reviewHere || (reviewHere.waiting === true && (reviewHere.fingerprint !== fingerprint || !waitsNow));
  if (reviewStarts) setReview({ id, fingerprint, refused: false, waiting: waitsNow });
  const changed = !!reviewHere && (reviewHere.refused || reviewHere.fingerprint !== fingerprint);
  function acceptNewVersion() {
    setReview({ id, fingerprint, refused: false, waiting: waitsNow });
  }
  const deselected = deselection.fingerprint === fingerprint ? deselection.ids : new Set<string>();
  const startable = startableFollowUps(picture.follow_ups);
  function toggleFollowUp(templateId: string) {
    const ids = new Set(deselected);
    if (!ids.delete(templateId)) ids.add(templateId);
    setDeselection({ fingerprint, ids });
  }
  // A schedule request's picture is the schedule's (spec 2026-11-25 C).
  const schedule = request.kind === 'schedule' ? picture.schedule : undefined;
  const sd = d.schedule;
  // A trigger request's picture is the trigger's (spec 2026-11-25 C).
  const trigger = request.kind === 'trigger' ? picture.trigger : undefined;
  const tr = t.triggers.request;
  const triggerState = trigger?.new ?? trigger?.current;
  // What else approving needs: what the schedule or trigger routes need.
  const role = orgInfo.data?.org.role;
  const writes = schedule ? 'templates.write' : trigger ? (triggerState?.kind === 'repo_sync' ? 'repos.write' : 'templates.write') : undefined;
  const canRun = may(role, 'jobs.run') && (!writes || may(role, writes));
  const scheduleGone = !!schedule?.gone || !!trigger?.gone;
  const pending = request.status === 'pending';
  // A sync_first request waits for its repository's sync, then is pending,
  // or fails with the sync's error (spec 2026-11-25 A). While it waits a
  // person can reject it, not approve it.
  const waiting = request.status === 'waiting_for_sync';
  const failed = request.status === 'failed';
  // The run's commit cannot be pinned yet (spec 2026-11-25 A): the server
  // refuses the approval, so the button waits too; the playbook section
  // says why.
  const commitBlocked = (picture.commit?.blocked ?? []).length > 0;
  const busy = approve.isPending || reject.isPending || detail.isFetching;
  const owner =
    (members.data?.members ?? []).find((m) => m.user_id === request.requested_by_user_id)?.display_name ??
    shortId(request.requested_by_user_id);
  // The token's name, its owner under it; a token without a name to show
  // (deleted) leaves the owner alone.
  const requestedBy = request.requested_by_token_name ? (
    <span className="flex flex-col gap-0.5">
      <span className="font-mono">{request.requested_by_token_name}</span>
      <span className="text-xs text-muted-foreground">{d.tokenOwner(owner)}</span>
    </span>
  ) : (
    owner
  );
  // Absent when the server could not read the token: no hint then.
  const tokenState = request.token_state;
  const tokenWarning = (pending || waiting) && tokenState && tokenState !== 'active' ? d.tokenState[tokenState] : undefined;
  const syncLink = request.sync_job_id ? (
    <Link to="/o/$org/runs/$id" params={{ org, id: request.sync_job_id }} className="text-xs font-medium hover:text-primary">
      {d.openSync}
    </Link>
  ) : undefined;
  let expiry: { label: string; value: ReactNode };
  if (pending && request.expires_at) {
    expiry = {
      label: d.expiresAt,
      value: <span title={fmt.dateTime(request.expires_at)}>{t.runs.requests.expires(fmt.relative(request.expires_at, now))}</span>,
    };
  } else if (waiting) {
    expiry = { label: d.expiresAt, value: d.expiryAfterSync };
  } else if (failed) {
    expiry = { label: d.endedAt, value: <span className="font-mono">{request.expires_at ? fmt.dateTime(request.expires_at) : '—'}</span> };
  } else {
    expiry = { label: d.decidedAt, value: <span className="font-mono">{request.decided_at ? fmt.dateTime(request.decided_at) : '—'}</span> };
  }

  // An approval starts one job per zone involved, all in one run group; the
  // page opens the first of them, or the runs list while none is listed yet.
  async function openRun(runGroupId: string | undefined) {
    const job = runGroupId ? await firstJobOfRunGroup(org, runGroupId) : undefined;
    if (job) await navigate({ to: '/o/$org/runs/$id', params: { org, id: job } });
    else
      await navigate({
        to: '/o/$org/runs',
        params: { org },
        search: { status: undefined, kind: undefined, trigger: undefined, schedule: undefined, host: undefined },
      });
  }

  async function onApprove() {
    try {
      // follow_ups goes out only when the selection differs from the default
      // (every startable one), so a plain approval sends the same body as ever.
      const followUps = startable.some((f) => deselected.has(f.template_id))
        ? startable.filter((f) => !deselected.has(f.template_id)).map((f) => f.template_id)
        : undefined;
      const { request: approved, trigger_secret: secret, trigger_url: url } = await approve.mutateAsync({ fingerprint, followUps });
      if (trigger) {
        toast.show(tr.approved);
        if (secret) setDrawn({ secret, url: url ?? trigger.url });
        return;
      }
      if (schedule) {
        toast.show(sd.approved);
        await openSchedule(approved.target_id);
        return;
      }
      toast.show(d.approved);
      await openRun(approved.run_group_id);
    } catch (err) {
      const message = err instanceof Error ? err.message : '';
      if (message.includes(RUN_REQUEST_CHANGED)) {
        setReview((r) => (r?.id === id ? { ...r, refused: true } : r));
        await detail.refetch();
        return;
      }
      toast.show(message || d.approveFailed, 'destructive');
    }
  }

  async function openSchedule(scheduleId: string | undefined) {
    await navigate({ to: '/o/$org/schedules', params: { org }, search: { id: scheduleId, template: undefined } });
  }

  async function onReject() {
    const message = schedule ? sd.rejectConfirm : trigger ? tr.rejectConfirm : d.rejectConfirm;
    if (!(await ask({ message, confirmLabel: d.reject, destructive: true }))) return;
    try {
      await reject.mutateAsync();
      toast.show(d.rejected);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : d.rejectFailed, 'destructive');
    }
  }

  const s = d.section;
  const nav: SectionNavItem[] = schedule
    ? [{ id: 'schedule', label: sd.section }]
    : trigger
      ? [{ id: 'trigger', label: tr.section }]
      : [
        { id: 'template', label: s.template },
        { id: 'playbook', label: s.playbook },
        { id: 'targets', label: s.targets, count: (picture.targets ?? []).length },
        ...(picture.agent_enrollment ? [{ id: 'enrollment', label: s.enrollment }] : []),
        { id: 'credentials', label: s.credentials },
        { id: 'vars', label: s.vars },
        ...(picture.retry ? [{ id: 'retry', label: s.retry }] : []),
        ...(picture.follow_ups ? [{ id: 'follow-ups', label: s.followUps }] : []),
      ];

  const sidebar = (
    <div className="flex flex-col gap-4">
      <SidebarHeading
        title={schedule ? sd.title[schedule.action] : trigger ? tr.title[trigger.action] : d.title}
        dot={{ tone: requestTone(request.status), label: t.runs.requests.status[request.status] }}
        pill={<Pill tone={requestTone(request.status)}>{t.runs.requests.status[request.status]}</Pill>}
      >
        <span>{t.runs.requests.kind[request.kind]}</span>
        {' · '}
        <span className="font-mono">{picture.template_name || triggerState?.name}</span>
      </SidebarHeading>
      <SidebarFacts
        items={[
          { label: d.reason, value: request.reason },
          { label: d.requestedBy, value: requestedBy },
          { label: d.requestedAt, value: <span className="font-mono">{fmt.dateTime(request.created_at)}</span> },
          expiry,
        ]}
      />
      {waiting && (
        <div role="note" aria-label={d.waiting.title} className="flex flex-col items-start gap-1 rounded-md border border-border px-3 py-2 text-xs">
          <p className="font-semibold">{d.waiting.title}</p>
          <p className="text-muted-foreground">{d.waiting.body}</p>
          {syncLink}
        </div>
      )}
      {failed && (
        <div role="alert" aria-label={d.syncFailed} className="flex flex-col items-start gap-1 rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs">
          <p className="font-semibold text-[var(--px-warn)]">{d.syncFailed}</p>
          {request.sync_error && <p className="break-words font-mono text-muted-foreground">{request.sync_error}</p>}
          {syncLink}
        </div>
      )}
      {tokenWarning && (
        <p role="note" className="rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs text-[var(--px-warn)]">
          {tokenWarning}
        </p>
      )}
      {pending && changed && (
        <div className="flex flex-col items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2">
          <p role="alert" className="text-xs text-destructive">
            {d.changed}
          </p>
          {canRun && (
            <Button type="button" onClick={acceptNewVersion}>
              {d.reviewNew}
            </Button>
          )}
        </div>
      )}
      {pending ? (
        canRun ? (
          <div className="flex flex-col items-start gap-2">
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="primary"
                disabled={busy || changed || commitBlocked || scheduleGone}
                onClick={() => void onApprove()}
              >
                {d.approve}
              </Button>
              <Button type="button" variant="destructive" disabled={busy} onClick={() => void onReject()}>
                {d.reject}
              </Button>
            </div>
            {commitBlocked ? (
              <span className="text-[11px] text-[var(--px-warn)]">{d.commitBlocked.title}</span>
            ) : (
              <span className="text-[11px] text-muted-foreground">{d.approveHint}</span>
            )}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">{schedule ? sd.noPermission : trigger ? tr.noPermission : d.noPermission}</span>
        )
      ) : waiting ? (
        canRun ? (
          <Button type="button" variant="destructive" disabled={busy} onClick={() => void onReject()}>
            {d.reject}
          </Button>
        ) : (
          <span className="text-xs text-muted-foreground">{d.noPermission}</span>
        )
      ) : (
        <div className="flex flex-col items-start gap-2 text-xs text-muted-foreground">
          <span>
            {schedule
              ? sd.decided[request.status as keyof typeof sd.decided]
              : trigger
                ? tr.decided[request.status as keyof typeof tr.decided]
                : d.decided[request.status as keyof typeof d.decided]}
          </span>
          {schedule && request.status === 'approved' && (
            <Link
              to="/o/$org/schedules"
              params={{ org }}
              search={{ id: request.target_id, template: undefined }}
              className="text-xs font-medium hover:text-primary"
            >
              {sd.openSchedules}
            </Link>
          )}
          {request.status === 'approved' && request.run_group_id && (
            <Button type="button" onClick={() => void openRun(request.run_group_id as string)}>
              {d.openRun}
            </Button>
          )}
        </div>
      )}
    </div>
  );

  return (
    <DetailLayout sidebar={sidebar} nav={nav} navLabel={t.common.sections}>
      {schedule ? (
        <ScheduleRequestSections schedule={schedule} templateRevision={picture.template_revision} requestedRevision={request.template_revision} />
      ) : trigger ? (
        <TriggerRequestSections trigger={trigger} templateRevision={picture.template_revision} requestedRevision={request.template_revision} />
      ) : (
        <RunPictureSections org={org} picture={picture} status={request.status} />
      )}
      {drawn && (
        <TriggerSecretDialog
          secret={drawn.secret}
          url={drawn.url}
          onClose={() => {
            setDrawn(undefined);
            // The approval's answer held the secret too: drop it.
            approve.reset();
          }}
        />
      )}
      {!schedule && !trigger && picture.follow_ups && (
        <RunRequestFollowUps followUps={picture.follow_ups} deselected={deselected} onToggle={toggleFollowUp} disabled={!pending || !canRun || busy} />
      )}
    </DetailLayout>
  );
}
