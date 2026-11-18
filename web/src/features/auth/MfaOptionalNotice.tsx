import { TriangleAlert } from 'lucide-react';
import { useSetupStatus } from '../../api/hooks/useSetupStatus.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * MfaOptionalNotice is the strip every page shows while the manager runs
 * with the second factor optional (PULSIX_MFA_OPTIONAL, `mfa_optional` on
 * GET /setup/status): such an instance is for demos and tests, and everyone
 * using it should know. It cannot be dismissed. OrgLayout and AccountLayout
 * pass it in AppShell's notice slot; the sign-in page shows it above the
 * form. A failed or pending status read shows nothing.
 */
export function MfaOptionalNotice() {
  const t = useT();
  const status = useSetupStatus();
  if (status.data?.mfa_optional !== true) return null;
  return (
    <div
      role="status"
      className="flex shrink-0 items-center justify-center gap-2 border-b border-border bg-card px-4 py-2 text-sm text-foreground"
    >
      <TriangleAlert aria-hidden="true" size={16} className="shrink-0 text-[var(--px-warn)]" />
      <span>{t.auth.mfaOptionalNotice}</span>
    </div>
  );
}
