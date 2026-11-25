import type { RunPicture } from '../../api/hooks/useRunRequests.ts';
import { useT } from '../../i18n/useT.ts';
import { reviewHref } from '../../ui/reviewHref.ts';

type OpenReview = NonNullable<NonNullable<RunPicture['schedule']>['open_change_reviews']>[number];

/**
 * OpenChangeReviewsNote tells the approver of a schedule or trigger request
 * that the template (or repository) it would run carries an assistant's
 * changes no person has reviewed yet (spec 2026-11-25 C2, final review 5),
 * and links each open review by its item's name. Nothing without one.
 */
export function OpenChangeReviewsNote({ org, reviews }: { org: string; reviews?: OpenReview[] | null }) {
  const t = useT();
  const n = t.runs.requests.detail.openChangeReviews;
  if (!reviews || reviews.length === 0) return null;
  return (
    <div role="note" aria-label={n.title} className="flex flex-col items-start gap-1 rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs">
      <p className="font-semibold text-[var(--px-warn)]">{n.title}</p>
      <p className="text-muted-foreground">{n.body(reviews.length)}</p>
      {reviews.map((r) => (
        <a key={r.request_id} href={reviewHref(org, r.request_id)} className="font-medium hover:text-primary">
          {n.link(r.item, r.name)}
        </a>
      ))}
    </div>
  );
}
