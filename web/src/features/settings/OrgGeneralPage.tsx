import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { SettingsPage } from '../../ui/settings.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useUnsavedGuard } from '../../ui/useUnsavedGuard.ts';
import { useT } from '../../i18n/useT.ts';
import { OrgNameCard } from './OrgNameCard.tsx';
import { OrgMonitoringCard } from './OrgMonitoringCard.tsx';
import { OrgAccessCard } from './OrgAccessCard.tsx';

/**
 * OrgGeneralPage is Settings → General, one column of cards: the
 * organization's name and slug (OrgNameCard), its monitoring settings
 * (OrgMonitoringCard, backlog 3.57) and the switches for assistant and
 * personal-token access (OrgAccessCard, which apply at once). The two form
 * cards report whether they are dirty, so leaving the page asks once.
 */
export function OrgGeneralPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const orgInfo = useOrg(org);
  const [nameDirty, setNameDirty] = useState(false);
  const [monitoringDirty, setMonitoringDirty] = useState(false);
  useUnsavedGuard(nameDirty || monitoringDirty);

  const guard = useQueryGuard(orgInfo);
  if (guard) return guard;
  if (!orgInfo.data) return <QueryLoading />;

  const current = orgInfo.data.org;
  const canWrite = may(current.role, 'org.settings.write');

  return (
    <SettingsPage title={t.settings.general.title} description={t.settings.general.hint}>
      <OrgNameCard org={org} name={current.name} slug={current.slug} canManage={may(current.role, 'org.manage')} onDirtyChange={setNameDirty} />
      <OrgMonitoringCard org={org} canWrite={canWrite} onDirtyChange={setMonitoringDirty} />
      <OrgAccessCard org={org} canWrite={canWrite} />
    </SettingsPage>
  );
}
