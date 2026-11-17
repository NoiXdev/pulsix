import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { KeyRound } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { useListView } from '../lists/useListView.ts';
import { AddCredentialDialog } from './AddCredentialDialog.tsx';
import { CredentialDialog } from './CredentialDialog.tsx';
import { credentialColumns } from './credentialColumns.tsx';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';

/** CredentialsPage lists the organization's credentials (list id
 * `credentials`): one request, sorted and paged in the browser. A
 * credential's details open in a dialog, from its name or the row menu. */
export function CredentialsPage() {
  const t = useT();
  const fmt = useFormat();
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const credentials = useCredentials(org);
  const [addOpen, setAddOpen] = useState(false);
  const [openId, setOpenId] = useState<string | null>(null);
  const canRotate = may(orgInfo.data?.org.role, 'credentials.write');

  const columns = credentialColumns({ t, fmt, open: (c) => setOpenId(c.id) });
  const view = useListView('credentials', columns, { mode: 'client', rowKey: (c) => c.id });

  const guard = useQueryGuard(credentials);
  if (guard) return guard;
  if (!credentials.data) return <QueryLoading />;
  const list = credentials.data.credentials ?? [];
  const { rows, total } = view.view(view.ready ? list : undefined);

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t.credentials.list.title}</h1>
        <Button variant="primary" onClick={() => setAddOpen(true)}>
          <KeyRound size={15} /> {t.credentials.list.add}
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">{t.credentials.list.summary(list.length)}</span>
      <DataList
        label={t.credentials.list.label}
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
        rowActions={(c) => [{ id: 'open', label: t.common.open, onSelect: () => setOpenId(c.id) }]}
        rowLabel={(c) => c.name}
        loading={!view.ready}
        empty={t.credentials.list.empty}
        {...view.listProps(total, t.credentials.list.count)}
      />
      <AddCredentialDialog
        org={org}
        open={addOpen}
        onClose={() => setAddOpen(false)}
        // This page's own list refetches via useCreateCredential's own
        // invalidation; nothing here needs the new id for itself.
        onCreated={() => undefined}
      />
      {openId && <CredentialDialog org={org} id={openId} canRotate={canRotate} onClose={() => setOpenId(null)} />}
    </>
  );
}
