import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { KeyRound } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { AddCredentialDialog } from './AddCredentialDialog.tsx';
import { CredentialDialog } from './CredentialDialog.tsx';
import { expiryState, kindLabel, usageLabel } from './labels.ts';

const COLUMNS = 'minmax(0, 1fr) 190px minmax(0, 1fr) 130px 110px';

export function CredentialsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const credentials = useCredentials(org);
  const [addOpen, setAddOpen] = useState(false);
  const [openId, setOpenId] = useState<string | null>(null);
  const canRotate = may(orgInfo.data?.org.role, 'credentials.write');

  const guard = useQueryGuard(credentials);
  if (guard) return guard;
  if (!credentials.data) return <QueryLoading />;
  const list = credentials.data.credentials ?? [];

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Credentials</h1>
        <Button variant="primary" onClick={() => setAddOpen(true)}>
          <KeyRound size={15} /> Add credential
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">{list.length} credentials · encrypted with the instance master key · values are never shown again</span>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Name</span>
          <span>Kind</span>
          <span>Used by</span>
          <span>Expiry</span>
          <span>Created</span>
        </GridHeader>
        {list.map((c) => {
          const expiry = expiryState(c);
          return (
            <GridRow key={c.id} columns={COLUMNS}>
              <button type="button" className="truncate text-left font-mono text-[13px] hover:text-primary" onClick={() => setOpenId(c.id)}>
                {c.name}
              </button>
              <span className="text-xs">{kindLabel(c)}</span>
              <span className="truncate text-xs text-muted-foreground">
                {c.username ? `${c.username}@ · ` : ''}
                {usageLabel(c)}
              </span>
              <span>{expiry ? <Pill tone={expiry.tone}>{expiry.text}</Pill> : <span className="text-xs text-muted-foreground">—</span>}</span>
              <span className="text-xs text-muted-foreground">{new Date(c.created_at).toLocaleDateString()}</span>
            </GridRow>
          );
        })}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No credentials yet.</p>}
      </Card>
      <AddCredentialDialog org={org} open={addOpen} onClose={() => setAddOpen(false)} />
      {openId && <CredentialDialog org={org} id={openId} canRotate={canRotate} onClose={() => setOpenId(null)} />}
    </>
  );
}
