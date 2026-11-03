import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { Radio } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useAgentTokens, useAgents, useRevokeAgent, useWithdrawAgentToken } from '../../api/hooks/useAgents.ts';
import { may } from '../../lib/roles.ts';
import { useFormat } from '../../i18n/format.ts';
import { VersionSkewBadge } from '../workers/VersionSkewBadge.tsx';
import { useT } from '../../i18n/useT.ts';
import type { Agent, AgentToken } from '../../api/types.ts';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from '../workers/expiry.ts';
import { AddAgentDialog } from './AddAgentDialog.tsx';

// The trailing 130px track is the Actions column, only rendered for
// canWrite — a read-only viewer gets no permanently blank column.
const COLUMNS_BASE = 'minmax(0, 0.9fr) minmax(0, 0.8fr) 90px 110px minmax(0, 0.9fr) 110px';
const COLUMNS_WITH_ACTIONS = `${COLUMNS_BASE} 130px`;
const TOKEN_COLUMNS = 'minmax(0, 1fr) 120px 110px 160px 110px';

/** AgentsPage lists the organization's host agents (v0.3a, spec §8):
 * which host each one runs on, whether it is still reporting (online =
 * a heartbeat within three heartbeat intervals), its version and its
 * certificate expiry — plus the enrollment tokens waiting to be redeemed.
 * Viewers read; operators enrol and revoke. */
export function AgentsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'agents.read');
  const canWrite = may(role, 'agents.write');
  const list = useAgents(org, !!orgInfo.data && canRead);
  const pending = useAgentTokens(org, !!orgInfo.data && canRead);
  const revoke = useRevokeAgent(org);
  const withdraw = useWithdrawAgentToken(org);
  const [adding, setAdding] = useState(false);

  const orgGuard = useQueryGuard(orgInfo);
  if (orgGuard) return orgGuard;
  if (!orgInfo.data) return <QueryLoading />;
  if (!canRead) {
    return <p className="text-sm text-muted-foreground">{t.agents.noAccess}</p>;
  }
  if (!list.data) return <QueryLoading />;
  const agents = list.data.agents ?? [];
  const tokens = pending.data?.tokens ?? [];
  const now = new Date();
  const columns = canWrite ? COLUMNS_WITH_ACTIONS : COLUMNS_BASE;
  const statusLabel = (status: string) => (status in t.agents.status ? t.agents.status[status as keyof typeof t.agents.status] : status);

  async function onRevoke(a: Agent) {
    if (!confirm(t.agents.revokeConfirm(a.host_name))) return;
    try {
      await revoke.mutateAsync(a.host_id);
      toast.show(t.agents.revoked(a.host_name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.agents.revokeFailed, 'destructive');
    }
  }

  async function onWithdraw(tok: AgentToken) {
    try {
      await withdraw.mutateAsync(tok.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.agents.withdrawFailed, 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.agents.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.agents.summary(agents.filter((a) => a.status === 'online').length, agents.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Radio size={15} /> {t.agents.enrol}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={columns}>
          <span>{t.agents.columns.host}</span>
          <span>{t.agents.columns.agent}</span>
          <span>{t.agents.columns.status}</span>
          <span>{t.agents.columns.lastSeen}</span>
          <span>{t.agents.columns.version}</span>
          <span>{t.agents.columns.certificate}</span>
          {canWrite && <span>{t.agents.columns.actions}</span>}
        </GridHeader>
        {agents.map((a) => {
          const expiring = !!a.cert_expires_at && daysLeft(a.cert_expires_at, now) < EXPIRY_WARN_DAYS;
          return (
            <GridRow key={a.host_id} columns={columns}>
              <span className="truncate text-[13px]" data-row>
                <Link to="/o/$org/hosts/$id" params={{ org, id: a.host_id }} className="hover:text-primary">
                  {a.host_name}
                </Link>
              </span>
              <span className="truncate font-mono text-xs">{a.agent_name}</span>
              <span>
                <Pill tone={a.status === 'online' ? 'ok' : 'warn'}>{statusLabel(a.status)}</Pill>
              </span>
              <span className={`text-xs ${a.status === 'online' ? 'text-muted-foreground' : 'text-[var(--px-warn)]'}`}>{seenLabel(a.last_seen, now, t)}</span>
              <span className="flex min-w-0 items-center gap-2 text-xs">
                <span className="truncate font-mono">{a.version || '—'}</span>
                <VersionSkewBadge status={a.version_status} />
              </span>
              <span className={`text-xs ${expiring ? 'font-semibold text-[var(--px-warn)]' : 'text-muted-foreground'}`} data-expiring={expiring || undefined}>
                {expiryLabel(a.cert_expires_at, now, t)}
              </span>
              {canWrite && (
                <span className="flex gap-1">
                  <Button type="button" variant="destructive" aria-label={t.agents.revokeLabel(a.host_name)} onClick={() => void onRevoke(a)}>
                    {t.agents.revoke}
                  </Button>
                </span>
              )}
            </GridRow>
          );
        })}
        {agents.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.agents.empty}</p>}
      </Card>
      {tokens.length > 0 && (
        <Card>
          <GridHeader columns={TOKEN_COLUMNS}>
            <span>{t.agents.columns.pendingToken}</span>
            <span>{t.agents.columns.uses}</span>
            <span>{t.agents.columns.bound}</span>
            <span>{t.agents.columns.expires}</span>
            <span />
          </GridHeader>
          {tokens.map((tok) => (
            <GridRow key={tok.id} columns={TOKEN_COLUMNS}>
              <span className="truncate font-mono text-[13px]">{tok.name_hint || tok.id.slice(0, 8)}</span>
              <span className="text-xs text-muted-foreground">
                {tok.uses} / {tok.max_uses}
              </span>
              <span className="text-xs text-muted-foreground">{tok.host_id ? t.agents.boundToHost : t.agents.boundNewHost}</span>
              <span className="text-xs text-muted-foreground">{fmt.dateTime(tok.expires_at)}</span>
              {canWrite ? (
                <Button type="button" variant="ghost" aria-label={t.agents.withdrawLabel(tok.name_hint || tok.id.slice(0, 8))} onClick={() => void onWithdraw(tok)}>
                  {t.agents.withdraw}
                </Button>
              ) : (
                <span />
              )}
            </GridRow>
          ))}
        </Card>
      )}
      {adding && <AddAgentDialog org={org} onClose={() => setAdding(false)} />}
    </>
  );
}
