import { ReviewNotice } from '../../ui/review-notice.tsx';
import { reviewHref } from '../../ui/reviewHref.ts';
import { useT } from '../../i18n/useT.ts';
import type { ReviewPause } from './templateDetail.ts';

/**
 * TemplateReviewNotice says, under the template page's header, that an
 * assistant's change paused some of the template's schedules or webhook
 * triggers until a person reviews it (spec 2026-11-25 C2), and links the
 * review: the one request when there is one, else the template's pending
 * requests.
 */
export function TemplateReviewNotice({ org, templateId, paused }: { org: string; templateId: string; paused: ReviewPause }) {
  const t = useT();
  const n = t.templates.page.paused;
  const one = paused.reviewIds.length === 1;
  const href = one
    ? reviewHref(org, paused.reviewIds[0])
    : `/o/${encodeURIComponent(org)}/runs/requests?template=${encodeURIComponent(templateId)}&status=pending`;
  return <ReviewNotice title={n.title(paused.count)} hint={n.hint} linkLabel={one ? n.openReview : n.openReviews} href={href} />;
}
