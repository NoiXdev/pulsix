import { useState, type FormEvent } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
import { Server, Terminal, Trash2, UserPlus } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Tag } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useDeleteHost, useHost, useSetHostCapabilities } from '../../api/hooks/useHosts.ts';
import { useHostTags, useSetHostTags } from '../../api/hooks/useTags.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { parseTagChip } from './selectorInput.ts';
import { useToast } from '../../ui/toastContext.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { HostAgentCard } from './HostAgentCard.tsx';
import { HostAlertsCard } from '../alerts/HostAlertsCard.tsx';
import { HostAlertRoutesCard } from './HostAlertRoutesCard.tsx';
import { HostChecksCard } from './HostChecksCard.tsx';
import { EffectiveRulesPanel } from './EffectiveRulesPanel.tsx';
import { HostInventoryCard } from './HostInventoryCard.tsx';
import { HostSshCard } from './HostSshCard.tsx';
import { ConnectionTestCard } from './ConnectionTestCard.tsx';
import { BootstrapDialog } from './BootstrapDialog.tsx';

/**
 * HostDetailPage follows design/mockups/build.mjs's HOST DETAIL artboard
 * for the header (status, name, zone) and facts panel. The mockup's metric
 * cards and check table arrived with v0.3b-1 and live in HostChecksCard;
 * the Ansible group panel is HostInventoryCard. The header's status dot is
 * still the one piece with no honest source — see its own comment.
 */
export function HostDetailPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const t = useT();
  const navigate = useNavigate();
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canWrite = may(role, 'inventory.write');

  const host = useHost(org, id);
  const tags = useHostTags(org, id);
  const setCapabilities = useSetHostCapabilities(org, id);
  const setTags = useSetHostTags(org, id);
  const deleteHost = useDeleteHost(org);

  const [tagInput, setTagInput] = useState('');
  const [tagError, setTagError] = useState<string | null>(null);
  const [bootstrapOpen, setBootstrapOpen] = useState(false);

  // The org query carries the role, and the role decides what several
  // cards below may ask for at all. `may(undefined, …)` is false by
  // design, so a page rendered before that response lands cannot tell
  // "not loaded yet" from "not permitted" — HostChecksCard would inform
  // an owner that monitoring is visible to members of this organization.
  // Ordered the way ChecksPage and MonitoringGroupsPage order it: the org
  // query's own failure and loading states first, the permission question
  // only once there is a role to ask about.
  const orgGuard = useQueryGuard(orgInfo);
  const hostGuard = useQueryGuard(host);
  if (orgGuard) return orgGuard;
  if (hostGuard) return hostGuard;
  if (!orgInfo.data || !host.data) return <QueryLoading />;
  const h = host.data.host;

  async function addTag(e: FormEvent) {
    e.preventDefault();
    const result = parseTagChip(tagInput, t);
    if (!result.ok) {
      setTagError(result.error);
      return;
    }
    setTagError(null);
    const current = (tags.data?.tags ?? []).map((tag) => ({ key: tag.key, value: tag.value }));
    const next = [...current.filter((tag) => tag.key !== result.tag.key), result.tag];
    await setTags.mutateAsync(next);
    setTagInput('');
  }

  async function removeTag(key: string) {
    const current = (tags.data?.tags ?? []).map((tag) => ({ key: tag.key, value: tag.value }));
    await setTags.mutateAsync(current.filter((tag) => tag.key !== key));
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          {/* Always "unknown": there is no monitoring backend yet in v0.1,
             so last_seen_at (set once on host creation, regardless of any
             real health signal) cannot honestly claim "ok" — cheap fix,
             task-15a-report.md fix round 1. */}
          <span className="h-2.5 w-2.5 rounded-full" style={{ background: 'var(--px-unknown)' }} title={t.hosts.detail.unknownTitle} />
          <h1 className="font-mono text-xl font-bold">{h.name}</h1>
          <Tag>{h.address || t.hosts.detail.noAddress}</Tag>
        </div>
        <Button
          variant="destructive"
          onClick={async () => {
            if (!confirm(t.hosts.detail.deleteConfirm(h.name))) return;
            try {
              await deleteHost.mutateAsync(h.id);
              toast.show(t.hosts.detail.deleted(h.name));
              await navigate({ to: '/o/$org/hosts', params: { org }, search: { q: undefined } });
            } catch (err) {
              toast.show(err instanceof Error ? err.message : t.hosts.detail.deleteFailed, 'destructive');
            }
          }}
        >
          <Trash2 size={14} /> {t.hosts.detail.delete}
        </Button>
      </div>
      <div className="flex justify-end">
        {h.managed && canWrite && (
          <Button type="button" onClick={() => setBootstrapOpen(true)}>
            <UserPlus size={14} /> {t.hosts.detail.bootstrap}
          </Button>
        )}
      </div>

      <div className="grid grid-cols-2 gap-4 items-start">
        <Card>
          <div className="flex flex-col gap-4 p-4">
            <h2 className="text-sm font-semibold">{t.hosts.detail.capabilities}</h2>
            <div className="flex items-center justify-between rounded-md border border-border p-3">
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <Terminal size={14} /> {t.hosts.capability.managedLong}
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={h.managed}
                aria-label={t.hosts.capability.managedLong}
                onClick={() => setCapabilities.mutate({ managed: !h.managed })}
                className={
                  h.managed
                    ? 'flex h-[18px] w-8 items-center rounded-full bg-primary px-0.5 justify-end'
                    : 'flex h-[18px] w-8 items-center rounded-full bg-border px-0.5 justify-start'
                }
              >
                <span className="h-3.5 w-3.5 rounded-full bg-background" />
              </button>
            </div>
            <div className="flex items-center justify-between rounded-md border border-border p-3">
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <Server size={14} /> {t.hosts.capability.monitored}
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={h.monitored}
                aria-label={t.hosts.capability.monitored}
                onClick={() => setCapabilities.mutate({ monitored: !h.monitored })}
                className={
                  h.monitored
                    ? 'flex h-[18px] w-8 items-center rounded-full bg-primary px-0.5 justify-end'
                    : 'flex h-[18px] w-8 items-center rounded-full bg-border px-0.5 justify-start'
                }
              >
                <span className="h-3.5 w-3.5 rounded-full bg-background" />
              </button>
            </div>
          </div>
        </Card>

        <Card>
          <div className="flex flex-col gap-3 p-4">
            <h2 className="text-sm font-semibold">{t.hosts.detail.tags}</h2>
            <div className="flex flex-wrap gap-2">
              {(tags.data?.tags ?? []).map((tag) => (
                <span
                  key={tag.key}
                  className="inline-flex items-center gap-1.5 rounded border border-border bg-background px-2 py-1 font-mono text-[11px]"
                >
                  {tag.key}={tag.value}
                  {tag.source === 'manual' && (
                    <button type="button" onClick={() => removeTag(tag.key)} aria-label={t.hosts.detail.removeTag(tag.key)}>
                      ×
                    </button>
                  )}
                </span>
              ))}
            </div>
            <form onSubmit={addTag} className="flex items-center gap-2">
              <input
                value={tagInput}
                onChange={(e) => setTagInput(e.target.value)}
                placeholder={t.hosts.detail.tagPlaceholder}
                className="h-8 flex-1 rounded-md border border-border bg-background px-2.5 font-mono text-xs outline-none"
              />
              <Button type="submit" disabled={setTags.isPending}>
                {t.hosts.detail.addTag}
              </Button>
            </form>
            {tagError && <span className="text-[11px] text-destructive">{tagError}</span>}
          </div>
        </Card>
      </div>

      <HostAgentCard org={org} hostId={h.id} canWrite={may(role, 'agents.write')} />
      <HostChecksCard org={org} hostId={h.id} canRead={may(role, 'checks.read')} canWrite={may(role, 'checks.write')} />
      <HostAlertsCard org={org} hostId={h.id} canRead={may(role, 'checks.read')} />
      <HostAlertRoutesCard org={org} hostId={h.id} canRead={may(role, 'channels.read')} />
      <EffectiveRulesPanel org={org} hostId={h.id} canRead={may(role, 'checks.read')} />
      <HostInventoryCard org={org} hostId={h.id} managed={h.managed} />
      <HostSshCard org={org} hostId={h.id} managed={h.managed} canWrite={canWrite} canCreateCredential={may(role, 'credentials.write')} />
      {h.managed && <ConnectionTestCard org={org} hostId={h.id} canWrite={canWrite} />}
      {bootstrapOpen && <BootstrapDialog org={org} hostId={h.id} hostName={h.name} onClose={() => setBootstrapOpen(false)} />}

      <Card>
        <div className="flex flex-col gap-2 p-4">
          <h2 className="text-sm font-semibold">{t.hosts.detail.facts}</h2>
          {Object.keys(h.facts ?? {}).length === 0 ? (
            <span className="text-xs text-muted-foreground">{t.hosts.detail.noFacts}</span>
          ) : (
            <div className="flex flex-col">
              {Object.entries(h.facts ?? {}).map(([k, v]) => (
                <div key={k} className="flex justify-between border-b border-border py-1.5 text-xs last:border-b-0">
                  <span className="text-muted-foreground">{k}</span>
                  <span className="font-mono text-foreground">{String(v)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </Card>
    </>
  );
}
