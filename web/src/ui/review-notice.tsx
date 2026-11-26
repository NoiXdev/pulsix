import type { ReactNode } from 'react';

/**
 * ReviewNotice says that something which runs unattended (a schedule, a
 * webhook trigger) is paused because an assistant changed what it runs,
 * and links the review request a person approves to resume it (spec
 * 2026-11-25 C2). Presentational: the caller passes the words and the
 * request's path; without one (a lookup that failed) the link is left out.
 * A caller with a router link of its own passes it as `link` instead.
 */
export function ReviewNotice({ title, hint, linkLabel, href, link }: { title: string; hint: string; linkLabel?: string; href?: string; link?: ReactNode }) {
  return (
    <div role="note" aria-label={title} className="flex flex-col items-start gap-1 rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs">
      <p className="font-semibold text-[var(--px-warn)]">{title}</p>
      <p className="text-muted-foreground">{hint}</p>
      {link ??
        (href && (
          <a href={href} className="font-medium hover:text-primary">
            {linkLabel}
          </a>
        ))}
    </div>
  );
}
