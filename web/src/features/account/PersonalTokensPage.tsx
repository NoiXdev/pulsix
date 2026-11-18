import { useState } from 'react';
import { KeyRound } from 'lucide-react';
import { DataList, type RowAction } from '../../ui/data-list/index.ts';
import { Button } from '../../ui/button.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { usePersonalTokens, useRevokePersonalToken, type PersonalToken } from '../../api/hooks/usePersonalTokens.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { useListView } from '../lists/useListView.ts';
import { AssistantConnectDialog } from '../tokens/AssistantConnectDialog.tsx';
import { isExpired } from '../tokens/expiry.ts';
import { PersonalTokenDialog } from './PersonalTokenDialog.tsx';
import { personalTokenColumns } from './personalTokenColumns.tsx';
import { PageHeader } from '../../ui/settings.tsx';

/** The dialog open on the page: a new token, or the token being edited. */
type Editing = { kind: 'create' } | { kind: 'edit'; token: PersonalToken };

/**
 * PersonalTokensPage is Settings → API tokens (/account/tokens, backlog
 * 3.100): the person's own API and assistant tokens, each valid only in the
 * organizations chosen on it, with a cap per organization. The row menu
 * edits (not an expired token), connects an assistant (assistant tokens) or revokes a token; revoking ends it in
 * every organization.
 */
export function PersonalTokensPage() {
  const t = useT();
  const p = t.personalTokens;
  const fmt = useFormat();
  const toast = useToast();
  const tokens = usePersonalTokens();
  const revoke = useRevokePersonalToken();
  const [editing, setEditing] = useState<Editing | null>(null);
  const [connecting, setConnecting] = useState<PersonalToken | null>(null);

  const columns = personalTokenColumns(t, fmt);
  const view = useListView('personal-tokens', columns, { mode: 'client', rowKey: (tok) => tok.id });
  const del = useConfirmedDelete<PersonalToken>({
    message: (tok) => p.revokeConfirm(tok.name),
    failed: p.revokeFailed,
    confirmLabel: t.ui.confirm.confirm,
    remove: (id) => revoke.mutateAsync(id),
    onDone: (tok) => toast.show(p.revoked(tok.name)),
  });

  const guard = useQueryGuard(tokens);
  if (guard) return guard;
  const all = tokens.data?.tokens ?? [];
  const { rows, total } = view.view(view.ready ? all : undefined);

  function rowActions(tok: PersonalToken): RowAction[] {
    const connect: RowAction[] =
      tok.kind === 'assistant' ? [{ id: 'connect', label: t.tokens.connect.item, onSelect: () => setConnecting(tok) }] : [];
    return [
      ...connect,
      { id: 'edit', label: p.editItem, onSelect: () => setEditing({ kind: 'edit', token: tok }), disabled: isExpired(tok.expires_at) },
      { id: 'revoke', label: p.revokeItem, destructive: true, onSelect: () => void del.run(tok), disabled: del.pending.has(tok.id) },
    ];
  }

  return (
    <>
      <PageHeader
        title={p.title}
        description={p.subtitle}
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'create' })}>
            <KeyRound size={15} /> {p.create}
          </Button>
        }
      />
      <DataList
        label={p.label}
        columns={columns}
        rows={rows}
        rowKey={(tok) => tok.id}
        rowLabel={(tok) => tok.name}
        rowActions={rowActions}
        loading={!view.ready}
        empty={p.empty}
        {...view.listProps(total, p.count)}
      />
      {connecting && (
        <AssistantConnectDialog
          key={connecting.id}
          name={connecting.name}
          orgSlugs={(connecting.grants ?? []).filter((g) => g.state === 'active').map((g) => g.org_slug)}
          personal
          onClose={() => setConnecting(null)}
        />
      )}
      {editing && (
        <PersonalTokenDialog
          key={editing.kind === 'edit' ? editing.token.id : 'create'}
          token={editing.kind === 'edit' ? editing.token : undefined}
          tokens={all}
          onClose={() => setEditing(null)}
        />
      )}
    </>
  );
}
