import { useState } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
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
 * Approve sends the fingerprint of the picture this page loaded. When the
 * server answers that the picture changed since (a template, host or
 * credential was edited in between), the page reloads it and asks for
 * another look instead of approving anything the person has not seen. After
 * an approval it opens the run the approval started.
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
  const [changed, setChanged] = useState(false);
  // The deselected direct follow-ups belong to one loaded picture: a new
  // fingerprint (a refetch after a change) starts again from "all checked".
  const [deselection, setDeselection] = useState<{ fingerprint: string; ids: ReadonlySet<string> }>({
    fingerprint: '',
    ids: new Set(),
  });

  const guard = useQueryGuard(detail);
  if (guard) return guard;
  if (!detail.data) return <QueryLoading />;

  const { request, picture, fingerprint } = detail.data;
  const deselected = deselection.fingerprint === fingerprint ? deselection.ids : new Set<string>();
  const startable = startableFollowUps(picture.follow_ups);
  function toggleFollowUp(templateId: string) {
    const ids = new Set(deselected);
    if (!ids.delete(templateId)) ids.add(templateId);
    setDeselection({ fingerprint, ids });
  }
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const pending = request.status === 'pending';
  const busy = approve.isPending || reject.isPending || detail.isFetching;
  const requester =
    (members.data?.members ?? []).find((m) => m.user_id === request.requested_by_user_id)?.display_name ??
    shortId(request.requested_by_user_id);

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
      const { request: approved } = await approve.mutateAsync({ fingerprint, followUps });
      setChanged(false);
      toast.show(d.approved);
      await openRun(approved.run_group_id);
    } catch (err) {
      const message = err instanceof Error ? err.message : '';
      if (message.includes(RUN_REQUEST_CHANGED)) {
        setChanged(true);
        await detail.refetch();
        return;
      }
      toast.show(message || d.approveFailed, 'destructive');
    }
  }

  async function onReject() {
    if (!(await ask({ message: d.rejectConfirm, confirmLabel: d.reject, destructive: true }))) return;
    try {
      await reject.mutateAsync();
      toast.show(d.rejected);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : d.rejectFailed, 'destructive');
    }
  }

  const s = d.section;
  const nav: SectionNavItem[] = [
    { id: 'template', label: s.template },
    { id: 'playbook', label: s.playbook },
    { id: 'targets', label: s.targets, count: (picture.targets ?? []).length },
    { id: 'credentials', label: s.credentials },
    { id: 'vars', label: s.vars },
    ...(picture.retry ? [{ id: 'retry', label: s.retry }] : []),
    ...(picture.follow_ups ? [{ id: 'follow-ups', label: s.followUps }] : []),
  ];

  const sidebar = (
    <div className="flex flex-col gap-4">
      <SidebarHeading
        title={d.title}
        dot={{ tone: requestTone(request.status), label: t.runs.requests.status[request.status] }}
        pill={<Pill tone={requestTone(request.status)}>{t.runs.requests.status[request.status]}</Pill>}
      >
        <span>{t.runs.requests.kind[request.kind]}</span>
        {' · '}
        <span className="font-mono">{picture.template_name}</span>
      </SidebarHeading>
      <SidebarFacts
        items={[
          { label: d.reason, value: request.reason },
          { label: d.requestedBy, value: requester },
          { label: d.requestedAt, value: <span className="font-mono">{fmt.dateTime(request.created_at)}</span> },
          pending
            ? {
                label: d.expiresAt,
                value: (
                  <span title={fmt.dateTime(request.expires_at)}>{t.runs.requests.expires(fmt.relative(request.expires_at, now))}</span>
                ),
              }
            : {
                label: d.decidedAt,
                value: <span className="font-mono">{request.decided_at ? fmt.dateTime(request.decided_at) : '—'}</span>,
              },
        ]}
      />
      {changed && (
        <p role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {d.changed}
        </p>
      )}
      {pending ? (
        canRun ? (
          <div className="flex flex-col items-start gap-2">
            <div className="flex flex-wrap gap-2">
              <Button type="button" variant="primary" disabled={busy} onClick={() => void onApprove()}>
                {d.approve}
              </Button>
              <Button type="button" variant="destructive" disabled={busy} onClick={() => void onReject()}>
                {d.reject}
              </Button>
            </div>
            <span className="text-[11px] text-muted-foreground">{d.approveHint}</span>
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">{d.noPermission}</span>
        )
      ) : (
        <div className="flex flex-col items-start gap-2 text-xs text-muted-foreground">
          <span>{d.decided[request.status as keyof typeof d.decided]}</span>
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
      <RunPictureSections org={org} picture={picture} />
      {picture.follow_ups && (
        <RunRequestFollowUps followUps={picture.follow_ups} deselected={deselected} onToggle={toggleFollowUp} disabled={!pending || !canRun || busy} />
      )}
    </DetailLayout>
  );
}
