import type { ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { useRepos } from '../../api/hooks/useRepos.ts';
import type { Template } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { playbookLabel } from '../playbooks/playbookColumns.tsx';
import { useTemplateHosts } from '../playbooks/useTemplateHosts.ts';
import { CommitId } from '../runs/CommitId.tsx';
import { scopeText } from './scopeText.ts';
import { targetHosts, templateSource } from './templateDetail.ts';

const NONE = '—';

/**
 * TemplateFacts is the second line of the template header: the playbook
 * (a link to its page), where it comes from — the playbook editor, or a
 * repository and its branch — the commit the repository's last sync read,
 * and the hosts a run targets: the scope, and when the browser can resolve
 * it (useTemplateHosts) the first hosts by name. What is not known is left
 * out rather than shown as a dash.
 */
export function TemplateFacts({ org, template }: { org: string; template: Template }) {
  const t = useT();
  const f = t.templates.page.fact;
  const fmt = useFormat();
  const playbooks = usePlaybooks(org);
  const repos = useRepos(org);
  const groups = useGroups(org, { enabled: !!template.scope.group_id });
  const hosts = targetHosts(useTemplateHosts(org, template));
  const playbook = (playbooks.data?.playbooks ?? []).find((p) => p.id === template.playbook_id);
  const source = templateSource(template.playbook_id, playbook, repos.data?.repos ?? undefined);
  const groupName = (id: string) => (groups.data?.groups ?? []).find((g) => g.id === id)?.name ?? id.slice(0, 8);
  const scope = scopeText(template, groupName, t);

  const items: { label: string; value: ReactNode }[] = [
    {
      label: f.playbook,
      value:
        source.kind === 'unsynced' ? (
          <span className="text-muted-foreground">{t.templates.playbook.notSynced}</span>
        ) : (
          <Link to="/o/$org/playbooks/$id" params={{ org, id: template.playbook_id }} search={{ tab: undefined }} className="font-mono hover:text-primary">
            {playbook ? playbookLabel(playbook, t) : template.playbook_id.slice(0, 8)}
          </Link>
        ),
    },
  ];
  if (source.kind === 'editor') items.push({ label: f.source, value: f.editor });
  if (source.kind === 'repo') {
    items.push({ label: f.source, value: <span className="font-mono">{source.branch ? f.branch(source.name, source.branch) : source.name}</span> });
    if (source.commit) {
      items.push({
        label: f.commit,
        value: (
          <span className="inline-flex items-center gap-1.5">
            <CommitId sha={source.commit} />
            {source.syncedAt && <span className="text-muted-foreground">{f.synced(fmt.relative(source.syncedAt))}</span>}
          </span>
        ),
      });
    }
  }
  items.push({ label: f.targets, value: <Targets scope={scope} hosts={hosts} limit={template.limit} /> });

  return (
    <dl data-testid="template-facts" className="flex flex-wrap items-center gap-x-5 gap-y-1 text-[13px]">
      {items.map((item) => (
        <div key={item.label} className="flex min-w-0 items-center gap-1.5">
          <dt className="text-muted-foreground">{item.label}</dt>
          <dd className="min-w-0 break-words">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** The target hosts: the scope, then the first hosts by name and how many
 * more (or that the scope holds none), then the limit when there is one. */
function Targets({ scope, hosts, limit }: { scope: string; hosts: ReturnType<typeof targetHosts>; limit: string }) {
  const t = useT();
  const f = t.templates.page.fact;
  const parts: string[] = [scope];
  if (hosts.kind === 'listed') {
    if (hosts.total === 0) parts.push(f.noHosts);
    else parts.push(hosts.more > 0 ? `${hosts.names.join(', ')} ${f.more(hosts.more)}` : hosts.names.join(', '));
  }
  if (limit) parts.push(f.limit(limit));
  return <span>{parts.join(' · ') || NONE}</span>;
}
