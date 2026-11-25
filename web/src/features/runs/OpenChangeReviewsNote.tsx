import { useT } from '../../i18n/useT.ts';
import { reviewHref } from '../../ui/reviewHref.ts';

/**
 * OpenChangeReviewsNote tells the approver of a schedule or trigger request
 * that the template (or repository) it would run carries an assistant's
 * changes no person has reviewed yet (spec 2026-11-25 C2, final review 5),
 * and links each open review. Nothing without one.
 */
export function OpenChangeReviewsNote({ org, ids }: { org: string; ids?: string[] | null }) {
  const t = useT();
  const n = t.runs.requests.detail.openChangeReviews;
  if (!ids || ids.length === 0) return null;
  return (
    <div role="note" aria-label={n.title} className="flex flex-col items-start gap-1 rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs">
      <p className="font-semibold text-[var(--px-warn)]">{n.title}</p>
      <p className="text-muted-foreground">{n.body(ids.length)}</p>
      {ids.map((id) => (
        <a key={id} href={reviewHref(org, id)} className="font-medium hover:text-primary">
          {n.link}
        </a>
      ))}
    </div>
  );
}
