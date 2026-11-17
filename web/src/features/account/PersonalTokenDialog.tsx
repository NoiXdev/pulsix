import { useState, type FormEvent } from 'react';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useCopy } from '../../ui/useCopy.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useOrgs } from '../../api/hooks/useOrgs.ts';
import {
  useCreatePersonalToken,
  usePersonalTokenSettings,
  useUpdatePersonalToken,
  type CreatePersonalTokenInput,
  type PersonalToken,
  type UpdatePersonalTokenInput,
} from '../../api/hooks/usePersonalTokens.ts';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';
import { AreaGrid } from '../tokens/AreaGrid.tsx';
import { allowedAreas, sameAreas, type Area } from '../tokens/areas.ts';
import { endOfLocalDay, localDay } from '../tokens/expiry.ts';
import { GrantsEditor } from './GrantsEditor.tsx';
import {
  blockedSlugs,
  grantRows,
  grantsBody,
  grantsChanged,
  highestRole,
  initialDraft,
  refusalOf,
  type GrantDraft,
  type Refused,
} from './grants.ts';

const KINDS = ['api', 'assistant'] as const;

/** The text of a refusal; `names` turns a slug into the organization's name. */
function refusalText(err: unknown, t: Messages, names: Map<string, string>, fallback: string): { text: string; refused?: Refused } {
  const r = refusalOf(err);
  const org = r?.slug ? (names.get(r.slug) ?? r.slug) : '';
  if (r?.detail === 'personal_token_limit') return { text: t.personalTokens.errors.limit };
  if (r?.detail === 'grant_blocked' && r.slug) return { text: t.personalTokens.errors.blocked(org), refused: { [r.slug]: 'blocked' } };
  if (r?.detail === 'personal_tokens_off' && r.slug) return { text: t.personalTokens.errors.off(org), refused: { [r.slug]: 'off' } };
  return { text: err instanceof Error ? err.message : fallback };
}

/**
 * PersonalTokenDialog creates a personal token, or edits one (`token`):
 * its name, kind (fixed once created), the organizations it works in with
 * the cap in each, the areas of an assistant token (limited by the highest
 * cap chosen) and its expiry. A new token's plaintext is shown once, with a
 * copy button, and is gone when the dialog closes. `tokens` is the person's
 * list, which tells where they are blocked.
 */
export function PersonalTokenDialog({
  token,
  tokens,
  onClose,
}: {
  token?: PersonalToken;
  tokens: readonly PersonalToken[];
  onClose: () => void;
}) {
  const t = useT();
  const p = t.personalTokens;
  const d = p.dialog;
  const toast = useToast();
  const copy = useCopy();
  const orgs = useOrgs();
  const create = useCreatePersonalToken();
  const update = useUpdatePersonalToken();

  const memberships = orgs.data?.orgs ?? [];
  const allowed = usePersonalTokenSettings(memberships.map((o) => o.slug));
  const [refused, setRefused] = useState<Refused>({});
  const rows = grantRows(memberships, { token, blocked: blockedSlugs(tokens), allowed, refused });
  const names = new Map(memberships.map((o) => [o.slug, o.name]));

  const initialDay = token?.expires_at ? localDay(token.expires_at) : '';
  const [name, setName] = useState(token?.name ?? '');
  const [kind, setKind] = useState<(typeof KINDS)[number]>(token?.kind ?? 'api');
  const [areas, setAreas] = useState<Area[]>((token?.areas ?? []) as Area[]);
  const [expiry, setExpiry] = useState(initialDay);
  const [never, setNever] = useState(!!token && !token.expires_at);
  const [draftState, setDraft] = useState<GrantDraft | null>(null);
  const draft = draftState ?? initialDraft(rows);
  const [errors, setErrors] = useState<{ name?: string; grants?: string; areas?: string; expiry?: string; form?: string }>({});
  const [secret, setSecret] = useState<string | null>(null);

  const body = grantsBody(rows, draft);
  const highest = highestRole(body.map((g) => g.role));
  const pending = create.isPending || update.isPending;

  function close() {
    setSecret(null);
    create.reset();
    onClose();
  }

  function validate(): boolean {
    const next: typeof errors = {};
    if (name.trim() === '') next.name = t.validation.required;
    if (body.length === 0 && (!token || grantsChanged(rows, body))) next.grants = p.grants.required;
    if (kind === 'assistant' && allowedAreas(areas, highest).length === 0) next.areas = t.tokens.areas.required;
    if (token && !never && expiry === '') next.expiry = d.expiryRequired;
    setErrors(next);
    return Object.keys(next).length === 0;
  }

  function refuse(err: unknown, fallback: string) {
    const r = refusalText(err, t, names, fallback);
    if (r.refused) setRefused((prev) => ({ ...prev, ...r.refused }));
    setErrors({ form: r.text });
  }

  async function submitCreate() {
    const input: CreatePersonalTokenInput = { name, kind, grants: body };
    if (kind === 'assistant') input.areas = allowedAreas(areas, highest);
    if (expiry !== '') input.expires_at = endOfLocalDay(expiry);
    try {
      const res = await create.mutateAsync(input);
      setSecret(res.token);
    } catch (err) {
      refuse(err, d.createFailed);
    }
  }

  async function submitEdit(tok: PersonalToken) {
    const patch: UpdatePersonalTokenInput = {};
    if (name !== tok.name) patch.name = name;
    const day = never ? '' : expiry;
    if (day !== initialDay) {
      if (never) patch.never_expires = true;
      else patch.expires_at = endOfLocalDay(day);
    }
    if (grantsChanged(rows, body)) patch.grants = body;
    const nextAreas = allowedAreas(areas, highest);
    const areasChanged = tok.kind === 'assistant' && !sameAreas(nextAreas, (tok.areas ?? []) as Area[]);
    if (areasChanged) patch.areas = nextAreas;
    try {
      await update.mutateAsync({ id: tok.id, body: patch });
      toast.show(areasChanged ? d.savedReconnect(name) : d.saved(name));
      onClose();
    } catch (err) {
      refuse(err, d.failed);
    }
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!validate()) return;
    void (token ? submitEdit(token) : submitCreate());
  }

  const title = token ? d.editTitle(token.name) : d.createTitle;

  return (
    <Dialog open onClose={close} title={title}>
      {secret ? (
        <div className="flex flex-col gap-3">
          <p className="text-xs text-muted-foreground">{d.shownOnce}</p>
          <code className="break-all rounded-md border border-border bg-background p-3 font-mono text-xs">{secret}</code>
          <div className="flex justify-end gap-2">
            <Button onClick={() => void copy(secret)}>{d.copy}</Button>
            <Button variant="primary" onClick={close}>
              {d.done}
            </Button>
          </div>
        </div>
      ) : (
        <form className="flex flex-col gap-4" onSubmit={submit} noValidate>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="personal_token_name">{d.nameLabel}</Label>
            <Input id="personal_token_name" value={name} onChange={(e) => setName(e.target.value)} placeholder="laptop" />
            {errors.name && <span className="text-[11px] text-destructive">{errors.name}</span>}
          </div>
          {token ? (
            <div className="flex flex-col gap-1.5">
              <span className="text-xs">
                {t.tokens.kind.label}: {t.tokens.kind[token.kind]}
              </span>
              <span className="text-[11px] text-muted-foreground">{d.kindFixed}</span>
            </div>
          ) : (
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1.5 text-xs font-semibold">{t.tokens.kind.label}</legend>
              <div role="radiogroup" aria-label={t.tokens.kind.label} className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
                {KINDS.map((k) => (
                  <label key={k} className="flex items-center gap-2">
                    <input type="radio" name="personal_token_kind" value={k} checked={kind === k} onChange={() => setKind(k)} />
                    {t.tokens.kind[k]}
                  </label>
                ))}
              </div>
              <span className="text-[11px] text-muted-foreground">{kind === 'assistant' ? d.assistantHint : d.apiHint}</span>
            </fieldset>
          )}
          {orgs.isPending ? (
            <span className="text-xs text-muted-foreground">{t.common.loading}</span>
          ) : (
            <GrantsEditor rows={rows} value={draft} onChange={setDraft} error={errors.grants} />
          )}
          {kind === 'assistant' && <AreaGrid role={highest} value={areas} onChange={setAreas} error={errors.areas} />}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="personal_token_expiry">{d.expiresLabel}</Label>
            <Input
              id="personal_token_expiry"
              type="date"
              value={expiry}
              disabled={never}
              aria-describedby={token ? undefined : 'personal_token_expiry_hint'}
              onChange={(e) => {
                setExpiry(e.target.value);
                if (e.target.value) setNever(false);
              }}
            />
            {!token && (
              <span id="personal_token_expiry_hint" className="text-[11px] text-muted-foreground">
                {d.expiresHint}
              </span>
            )}
            {errors.expiry && <span className="text-[11px] text-destructive">{errors.expiry}</span>}
            {token && (
              <label className="flex items-center gap-2 text-xs">
                <input
                  type="checkbox"
                  checked={never}
                  onChange={(e) => {
                    setNever(e.target.checked);
                    if (e.target.checked) setExpiry('');
                  }}
                />
                {d.neverExpires}
              </label>
            )}
          </div>
          {errors.form && (
            <span role="alert" className="text-[11px] text-destructive">
              {errors.form}
            </span>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={close}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary" disabled={pending}>
              {token ? d.save : d.createTitle}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}
