import { Link } from '@tanstack/react-router';
import { ReviewNotice } from '../../ui/review-notice.tsx';
import { useT } from '../../i18n/useT.ts';
import type { ReviewPause } from './templateDetail.ts';

/** The look of ReviewNotice's own link. */
const LINK = 'font-medium hover:text-primary';

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
  const link = one ? (
    <Link to="/o/$org/runs/requests/$id" params={{ org, id: paused.reviewIds[0] }} className={LINK}>
      {n.openReview}
    </Link>
  ) : (
    <Link to="/o/$org/runs/requests" params={{ org }} search={{ template: templateId, status: 'pending' }} className={LINK}>
      {n.openReviews}
    </Link>
  );
  return <ReviewNotice title={n.title(paused.count)} hint={n.hint} link={link} />;
}
