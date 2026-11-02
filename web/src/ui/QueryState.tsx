import { useEffect } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { errorStatus } from '../lib/apiError.ts';
import { useT } from '../i18n/useT.ts';
import { useResetSession } from '../lib/session.ts';

/**
 * QueryError is the one place that decides what a failed query renders —
 * finding "Important 7": OrgLayout, AccountSecurityLayout, HostDetailPage
 * and MembersPage each independently rendered nothing for a query error
 * (staying on "Loading…" forever, 401 included), and HostsListPage showed
 * "No hosts yet" on an error rather than reporting it. A 401 means the
 * session is gone, so this redirects to /login; a 403 is a real "you do
 * not have access" state the members-spec explicitly calls for; a 404
 * says the thing does not exist; anything else is a generic failure with
 * the server's own message where there is one.
 *
 * The 401 redirect also drops the whole query cache (lib/session.ts's
 * resetSessionCache) once /login is reached: whoever signs in next on
 * this tab must not be served the expired session's cached data.
 */
export function QueryError({ error }: { error: unknown }) {
  const navigate = useNavigate();
  const resetSession = useResetSession();
  const status = errorStatus(error);
  const t = useT();

  useEffect(() => {
    if (status === 401) {
      void navigate({ to: '/login', search: { step: undefined } }).finally(resetSession);
    }
  }, [status, navigate, resetSession]);

  if (status === 401) {
    return <FullPageMessage title={t.ui.query.signingOutTitle} detail={t.ui.query.signingOutDetail} />;
  }
  if (status === 403) {
    return (
      <FullPageMessage title={t.ui.query.forbiddenTitle} detail={t.ui.query.forbiddenDetail} />
    );
  }
  if (status === 404) {
    return <FullPageMessage title={t.ui.query.notFoundTitle} detail={t.ui.query.notFoundDetail} />;
  }
  return (
    <FullPageMessage
      title={t.ui.query.failedTitle}
      detail={error instanceof Error ? error.message : t.ui.query.failedDetail}
    />
  );
}

function FullPageMessage({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="flex h-screen flex-col items-center justify-center gap-2 bg-background px-6 text-center text-foreground">
      <h1 className="text-lg font-semibold">{title}</h1>
      <p className="max-w-sm text-sm text-muted-foreground">{detail}</p>
    </div>
  );
}

/** QueryLoading is the shared "still fetching" full-page state — kept as
 * its own component (not folded into QueryError) so a page can render it
 * for the pending case and QueryError only for the error case. */
export function QueryLoading() {
  const t = useT();
  return (
    <div className="flex h-screen items-center justify-center bg-background text-muted-foreground">
      {t.common.loading}
    </div>
  );
}
