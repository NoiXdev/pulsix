/**
 * ReviewNotice says that something which runs unattended (a schedule, a
 * webhook trigger) is paused because an assistant changed what it runs,
 * and links the review request a person approves to resume it (spec
 * 2026-11-25 C2). Presentational: the caller passes the words and the
 * request's path; without one (a lookup that failed) the link is left out.
 */
export function ReviewNotice({ title, hint, linkLabel, href }: { title: string; hint: string; linkLabel: string; href?: string }) {
  return (
    <div role="note" aria-label={title} className="flex flex-col items-start gap-1 rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs">
      <p className="font-semibold text-[var(--px-warn)]">{title}</p>
      <p className="text-muted-foreground">{hint}</p>
      {href && (
        <a href={href} className="font-medium hover:text-primary">
          {linkLabel}
        </a>
      )}
    </div>
  );
}
