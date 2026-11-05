import { Pill } from '../../../ui/badge.tsx';
import type { TemplateVariablesReview } from '../../../api/types.ts';
import { useT } from '../../../i18n/useT.ts';

/**
 * ReviewBadges says what a linked template's variables leave for a person
 * (spec 7.75 goal 5): fields gone from the source, changes held for review,
 * required fields without a value, and source fields a limit left out. Each
 * appears only while its count is above zero; nothing renders without any.
 */
export function ReviewBadges({ review }: { review?: TemplateVariablesReview }) {
  const t = useT();
  const r = t.templates.review;
  if (!review) return null;
  const badges = [
    review.gone > 0 && <Pill key="gone" tone="warn">{r.gone(review.gone)}</Pill>,
    review.needs_review > 0 && <Pill key="review" tone="warn">{r.needsReview(review.needs_review)}</Pill>,
    review.missing_required > 0 && <Pill key="required" tone="warn">{r.missingRequired(review.missing_required)}</Pill>,
    review.left_out > 0 && <Pill key="left-out" tone="unknown">{r.leftOut(review.left_out)}</Pill>,
  ].filter(Boolean);
  if (badges.length === 0) return null;
  return <span className="flex flex-wrap items-center gap-1.5">{badges}</span>;
}
