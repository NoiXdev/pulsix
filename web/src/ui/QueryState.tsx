import { useEffect } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { errorStatus } from '../lib/apiError.ts';

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
 */
export function QueryError({ error }: { error: unknown }) {
  const navigate = useNavigate();
  const status = errorStatus(error);

  useEffect(() => {
    if (status === 401) {
      void navigate({ to: '/login', search: { step: undefined } });
    }
  }, [status, navigate]);

  if (status === 401) {
    return <FullPageMessage title="Signing you out" detail="Your session has expired. Redirecting to sign in…" />;
  }
  if (status === 403) {
    return (
      <FullPageMessage
        title="You do not have access"
        detail="Ask an organization owner or admin to grant you access to this page."
      />
    );
  }
  if (status === 404) {
    return <FullPageMessage title="Not found" detail="This page does not exist, or you do not have access to it." />;
  }
  return (
    <FullPageMessage
      title="Something went wrong"
      detail={error instanceof Error ? error.message : 'An unexpected error occurred. Please try again.'}
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
  return (
    <div className="flex h-screen items-center justify-center bg-background text-muted-foreground">Loading…</div>
  );
}
