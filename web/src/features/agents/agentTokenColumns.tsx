import { Link } from '@tanstack/react-router';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { AgentToken } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import type { useFormat } from '../../i18n/format.ts';

/** A pending token's name: the hint it was created with, else its id's start. */
export function agentTokenName(tok: AgentToken): string {
  return tok.name_hint || tok.id.slice(0, 8);
}

/**
 * agentTokenColumns is every column the pending enrollment token list
 * offers (list id `agent-tokens`, a second list on the Agents page whose
 * sort and page stay in component state). Withdraw is the row menu's. A
 * token a run minted for its host names the run and links to it.
 */
export function agentTokenColumns(t: Messages, fmt: ReturnType<typeof useFormat>, org: string): DataListColumn<AgentToken>[] {
  const c = t.agents.columns;
  return [
    {
      id: 'token',
      label: c.pendingToken,
      width: 'minmax(0, 1fr)',
      sortKey: 'token',
      sortValue: (tok) => agentTokenName(tok),
      required: true,
      defaultVisible: true,
      render: (tok) => (
        <span className="block truncate">
          <span className="font-mono">{agentTokenName(tok)}</span>
          {tok.job_id && (
            <Link to="/o/$org/runs/$id" params={{ org, id: tok.job_id }} className={`ml-2 text-muted-foreground hover:text-primary ${CELL_MINOR}`}>
              {t.agents.forJob(tok.job_id.slice(0, 8))}
            </Link>
          )}
        </span>
      ),
    },
    {
      id: 'uses',
      label: c.uses,
      width: '120px',
      sortKey: 'uses',
      sortValue: (tok) => tok.uses,
      defaultVisible: true,
      render: (tok) => (
        <span className={`text-muted-foreground ${CELL_MINOR}`}>
          {tok.uses} / {tok.max_uses}
        </span>
      ),
    },
    {
      id: 'bound',
      label: c.bound,
      width: '110px',
      sortKey: 'bound',
      sortValue: (tok) => !!tok.host_id,
      defaultVisible: true,
      render: (tok) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{tok.host_id ? t.agents.boundToHost : t.agents.boundNewHost}</span>,
    },
    {
      id: 'expires',
      label: c.expires,
      width: '160px',
      sortKey: 'expires',
      sortValue: (tok) => tok.expires_at,
      defaultVisible: true,
      render: (tok) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.dateTime(tok.expires_at)}</span>,
    },
  ];
}
