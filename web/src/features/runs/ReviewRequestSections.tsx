import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import type { RunPicture } from '../../api/hooks/useRunRequests.ts';
import { useT } from '../../i18n/useT.ts';
import { varText } from './pictureVars.ts';

export type PictureReview = NonNullable<RunPicture['review']>;

const TABLE = 'w-full text-xs';
const TH = 'px-4 py-2 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase';
const TD = 'border-t border-border px-4 py-2 align-top';

/**
 * ReviewRequestSections is the picture of a review (spec 2026-11-25 C2): an
 * assistant changed what a schedule or webhook trigger runs, which paused
 * (disabled) it. It shows the item as it is now and, per change, the job
 * template or repository with each field's value before and after — a
 * secret only as "hidden" — and the token that made it. It says when the
 * item is no longer stopped for the review and when it is gone, and who had
 * paused it before the change (approving then leaves it paused).
 */
export function ReviewRequestSections({ review, tokenName, tokenId, personName }: {
  review: PictureReview;
  /** The requesting token's name and id: the change made with another
   * token names that one by id. */
  tokenName?: string;
  tokenId?: string;
  /** The display name of a member by user id ('' when unknown). */
  personName: (userId: string) => string;
}) {
  const t = useT();
  const r = t.runs.requests.detail.review;
  const tr = t.triggers.request;
  const secret = t.runs.requests.detail.secret;
  const { schedule, trigger } = review;
  const value = (v: unknown) => (v === null || v === undefined ? r.none : Array.isArray(v) ? v.map((e) => varText(e, secret)).join('\n') || r.none : varText(v, secret));

  const facts: { label: string; value: string }[] = [];
  if (schedule) {
    facts.push(
      { label: r.item.schedule, value: schedule.name },
      { label: r.timetable, value: [schedule.cron, schedule.description].filter(Boolean).join(' · ') },
      { label: r.state, value: schedule.paused ? r.paused : r.active },
    );
  }
  if (trigger) {
    facts.push(
      { label: r.item.trigger, value: trigger.name },
      {
        label: r.target,
        value:
          trigger.kind === 'repo_sync'
            ? tr.syncs(trigger.repo_name || trigger.repo_id || '', trigger.repo_url ?? '', trigger.repo_branch ?? '')
            : tr.runs(trigger.template_name || trigger.template_id || ''),
      },
      { label: r.state, value: trigger.enabled ? t.triggers.enabled : t.triggers.disabled },
    );
  }

  return (
    <Section id="review" title={r.section}>
      <div className="flex flex-col gap-3">
        {review.gone && (
          <p role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            {r.gone}
          </p>
        )}
        <p className="text-xs text-muted-foreground">{r.intro[review.item]}</p>
        {review.paused_before && (
          <p role="note" className="rounded-md border border-border px-3 py-2 text-xs">
            {r.pausedBefore[review.item](review.paused_before.by_user_id ? personName(review.paused_before.by_user_id) : '')}
          </p>
        )}
        {!review.gone && !review.paused_for_review && (
          <p role="note" className="rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs text-[var(--px-warn)]">
            {r.notPaused[review.item]}
          </p>
        )}
        {facts.length > 0 && (
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-xs">
            {facts.map((f) => (
              <div key={f.label} className="contents">
                <dt className="text-muted-foreground">{f.label}</dt>
                <dd className="font-mono break-all">{f.value}</dd>
              </div>
            ))}
          </dl>
        )}
        <h3 className="text-xs font-semibold">{r.changes}</h3>
        {(review.changes ?? []).map((c, i) => {
          const caption = `${r.subject[c.subject] ?? c.subject} ${c.name}`;
          const by = c.token_id && tokenId && c.token_id !== tokenId ? r.otherToken(c.token_id) : r.changedBy(tokenName || c.token_id || '');
          return (
            <Card key={`${c.id}-${i}`}>
              <table className={TABLE} aria-label={caption}>
                <caption className="px-4 pt-2 text-left text-xs">
                  <span className="font-semibold">{caption}</span>
                  <span className="ml-2 text-muted-foreground">{by}</span>
                </caption>
                <thead>
                  <tr>
                    <th className={TH}>{r.field}</th>
                    <th className={TH}>{r.before}</th>
                    <th className={TH}>{r.after}</th>
                  </tr>
                </thead>
                <tbody>
                  {(c.fields ?? []).map((f) => (
                    <tr key={f.field}>
                      <th scope="row" className={`${TD} text-left font-mono font-normal text-muted-foreground`}>
                        {f.field}
                      </th>
                      <td className={`${TD} font-mono whitespace-pre-line break-all`}>{value(f.old)}</td>
                      <td className={`${TD} font-mono whitespace-pre-line break-all`}>{value(f.new)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>
          );
        })}
      </div>
    </Section>
  );
}
