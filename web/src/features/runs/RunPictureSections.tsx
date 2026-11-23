import { Link } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { Section, SidebarFacts } from '../../ui/detail-layout.tsx';
import type { RunPicture } from '../../api/hooks/useRunRequests.ts';
import { useT } from '../../i18n/useT.ts';
import { shortId } from './status.ts';

const NONE = '—';

/** isMaskedSecret reports a value the server masked: a secret extra var
 * arrives as {"$secret": "set"}, never with its value. */
function isMaskedSecret(value: unknown): boolean {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    Object.keys(value).length === 1 &&
    (value as Record<string, unknown>).$secret === 'set'
  );
}

/** varText renders an extra var's value: a string as it is, anything else
 * as JSON, with every masked secret inside it replaced by `secretLabel`. */
function varText(value: unknown, secretLabel: string): string {
  if (isMaskedSecret(value)) return secretLabel;
  if (typeof value === 'string') return value;
  return JSON.stringify(value, (_key, v: unknown) => (isMaskedSecret(v) ? secretLabel : v));
}

function Mono({ children }: { children: string | number | undefined | null }) {
  return <span className="font-mono">{children === undefined || children === null || children === '' ? NONE : children}</span>;
}

const TABLE = 'w-full text-xs';
const TH = 'px-4 py-2 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase';
const TD = 'border-t border-border px-4 py-2';

/**
 * RunPictureSections shows what a run request's run would use if it were
 * approved now, in the order a reviewer reads it: the template, the
 * playbook and its repository, the target hosts, the credentials by name
 * (never their contents) and the extra vars with secrets masked, and for a
 * retry the run it repeats and the hosts that failed there.
 */
export function RunPictureSections({ org, picture }: { org: string; picture: RunPicture }) {
  const t = useT();
  const d = t.runs.requests.detail;
  const s = d.section;
  const targets = picture.targets ?? [];
  const vault = picture.vault_identities ?? [];
  const vars = Object.entries(picture.extra_vars ?? {});
  const onlyHost = picture.only_host ? (targets.find((h) => h.id === picture.only_host)?.name ?? shortId(picture.only_host)) : undefined;
  const source = picture.playbook.source;
  const sourceLabel = Object.hasOwn(d.sources, source) ? d.sources[source as keyof typeof d.sources] : source;

  return (
    <>
      <Section id="template" title={s.template}>
        <SidebarFacts
          items={[
            { label: d.templateName, value: <Mono>{picture.template_name}</Mono> },
            { label: d.revision, value: <Mono>{picture.template_revision}</Mono> },
            { label: d.limit, value: picture.limit ? <Mono>{picture.limit}</Mono> : d.wholeScope },
            ...(onlyHost ? [{ label: d.onlyHost, value: <Mono>{onlyHost}</Mono> }] : []),
          ]}
        />
      </Section>

      <Section id="playbook" title={s.playbook}>
        <SidebarFacts
          items={[
            { label: d.playbookSource, value: sourceLabel },
            ...(picture.playbook.path ? [{ label: d.playbookPath, value: <Mono>{picture.playbook.path}</Mono> }] : []),
            ...(picture.playbook.inline_digest
              ? [{ label: d.inlineDigest, value: <Mono>{picture.playbook.inline_digest}</Mono> }]
              : []),
            ...(picture.repo
              ? [
                  { label: d.repository, value: <Mono>{picture.repo.name}</Mono> },
                  { label: d.url, value: <Mono>{picture.repo.url}</Mono> },
                  { label: d.branch, value: <Mono>{picture.repo.branch}</Mono> },
                  { label: d.subpath, value: <Mono>{picture.repo.subpath}</Mono> },
                  // The credential the repository is read with is part of the fingerprint, so it is shown too.
                  ...(picture.repo.git_credential ? [{ label: d.gitCredential, value: <Mono>{picture.repo.git_credential}</Mono> }] : []),
                ]
              : []),
          ]}
        />
      </Section>

      <Section id="targets" title={s.targets}>
        <Card>
          {targets.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{d.noTargets}</p>
          ) : (
            <table className={TABLE}>
              <thead>
                <tr>
                  <th className={TH}>{d.targetColumns.host}</th>
                  <th className={TH}>{d.targetColumns.address}</th>
                  <th className={TH}>{d.targetColumns.user}</th>
                  <th className={TH}>{d.targetColumns.port}</th>
                  <th className={TH}>{d.targetColumns.jumpHost}</th>
                  <th className={TH}>{d.targetColumns.credential}</th>
                </tr>
              </thead>
              <tbody>
                {targets.map((h) => (
                  <tr key={h.id}>
                    <td className={`${TD} font-semibold`}>{h.name}</td>
                    <td className={TD}>
                      <Mono>{h.address}</Mono>
                    </td>
                    <td className={TD}>
                      <Mono>{h.ssh_user}</Mono>
                    </td>
                    <td className={TD}>
                      <Mono>{h.ssh_port}</Mono>
                    </td>
                    <td className={TD}>
                      <Mono>{h.jump_host}</Mono>
                    </td>
                    <td className={TD}>
                      <Mono>{h.credential}</Mono>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      </Section>

      <Section id="credentials" title={s.credentials}>
        <SidebarFacts
          items={[
            { label: d.sshCredential, value: picture.ssh_credential ? <Mono>{picture.ssh_credential}</Mono> : d.noSshCredential },
            {
              label: d.vaultIdentities,
              value:
                vault.length === 0 ? (
                  d.noVaultIdentities
                ) : (
                  <ul className="flex flex-col gap-1">
                    {vault.map((v) => (
                      <li key={v.label} className="flex gap-2">
                        <Mono>{v.label}</Mono>
                        <span className="text-muted-foreground">→</span>
                        <Mono>{v.credential}</Mono>
                      </li>
                    ))}
                  </ul>
                ),
            },
          ]}
        />
      </Section>

      <Section id="vars" title={s.vars}>
        <Card>
          {vars.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{d.noVars}</p>
          ) : (
            <table className={TABLE}>
              <thead>
                <tr>
                  <th className={TH}>{d.varName}</th>
                  <th className={TH}>{d.varValue}</th>
                </tr>
              </thead>
              <tbody>
                {vars.map(([name, value]) => (
                  <tr key={name}>
                    <td className={`${TD} font-mono`}>{name}</td>
                    <td className={`${TD} font-mono break-all ${isMaskedSecret(value) ? 'text-muted-foreground italic' : ''}`}>
                      {varText(value, d.secret)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      </Section>

      {picture.retry && (
        <Section id="retry" title={s.retry}>
          <SidebarFacts
            items={[
              {
                label: d.retryJob,
                value: (
                  <Link to="/o/$org/runs/$id" params={{ org, id: picture.retry.job_id }} className="font-mono hover:text-primary">
                    {shortId(picture.retry.job_id)}
                  </Link>
                ),
              },
              {
                label: d.failedHosts,
                value: (
                  <ul className="flex flex-col gap-1">
                    {(picture.retry.failed_hosts ?? []).map((h) => (
                      <li key={h} className="font-mono">
                        {h}
                      </li>
                    ))}
                  </ul>
                ),
              },
            ]}
          />
        </Section>
      )}
    </>
  );
}
