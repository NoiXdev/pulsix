import { useMemo, useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm, useWatch } from 'react-hook-form';
import { z } from 'zod';
import { KeyRound } from 'lucide-react';
import { DataList } from '../../ui/data-list/index.ts';
import { Button } from '../../ui/button.tsx';
import { FilterChip, ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { useCopy } from '../../ui/useCopy.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useCreateToken, useTokens } from '../../api/hooks/useTokens.ts';
import { useOrgPersonalTokens } from '../../api/hooks/usePersonalTokens.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { roleOptions } from '../members/roles.ts';
import type { Messages } from '../../i18n/types.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { ORG_OWNER, TOKEN_KINDS, TOKEN_ROLES, TOKENS_FILTERS, filterTokens } from './tokensSearch.ts';
import { AssistantConnectGuide } from './AssistantConnectGuide.tsx';
import { useSettingsOrgs } from './useSettingsOrgs.ts';
import { AreaGrid } from './AreaGrid.tsx';
import { tokenColumns, tokenRowKey, type TokenRow } from './tokenColumns.tsx';
import { useTokenListActions } from './useTokenListActions.tsx';
import { allowedAreas, type Area } from './areas.ts';
import { PageHeader } from '../../ui/settings.tsx';

const ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;
const KINDS = ['api', 'assistant'] as const;

const tokenFormSchema = (t: Messages) =>
  z
    .object({
      name: z.string().min(1, t.validation.required),
      role: z.enum(ROLES),
      kind: z.enum(KINDS),
      areas: z.array(z.custom<Area>()),
    })
    // An assistant token needs at least one area the role can cover; the
    // server refuses one without (422).
    .refine((v) => v.kind !== 'assistant' || allowedAreas(v.areas, v.role).length > 0, {
      path: ['areas'],
      message: t.tokens.areas.required,
    });
type TokenFormValues = z.infer<ReturnType<typeof tokenFormSchema>>;

const DEFAULTS: TokenFormValues = { name: '', role: 'viewer', kind: 'api', areas: [] };

export function TokensPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const t = useT();
  const copy = useCopy();
  const fmt = useFormat();
  const schema = useMemo(() => tokenFormSchema(t), [t]);

  const tokens = useTokens(org);
  const personal = useOrgPersonalTokens(org);
  const createToken = useCreateToken(org);

  const [createOpen, setCreateOpen] = useState(false);
  const [justCreated, setJustCreated] = useState<string | null>(null);
  const [createdKind, setCreatedKind] = useState<TokenFormValues['kind']>('api');

  const form = useForm<TokenFormValues>({ resolver: zodResolver(schema), defaultValues: DEFAULTS });
  const kind = useWatch({ control: form.control, name: 'kind' });
  const role = useWatch({ control: form.control, name: 'role' });

  const columns = tokenColumns(t, fmt);
  const { filters, active, setFilter, reset } = useListFilters(TOKENS_FILTERS);
  const view = useListView('org-tokens', columns, { mode: 'client', rowKey: tokenRowKey, resetKey: JSON.stringify(filters) });
  const actions = useTokenListActions(org);
  const settingsOrgs = useSettingsOrgs([org]);

  function closeCreate() {
    setCreateOpen(false);
    setJustCreated(null);
    createToken.reset();
    form.reset();
  }

  const tokensGuard = useQueryGuard(tokens);
  if (tokensGuard) return tokensGuard;
  // The organization's tokens and the members' personal tokens granted
  // here, in one list; the personal ones wait for their own answer, and a
  // failure there leaves the organization's tokens on the page.
  const all: TokenRow[] = [
    ...(tokens.data?.tokens ?? []).map((tok) => ({ type: 'org' as const, ...tok })),
    ...(personal.data?.entries ?? []).map((e) => ({ type: 'personal' as const, ...e })),
  ];
  const settled = view.ready && !personal.isPending;
  const shown = filterTokens(all, filters);
  const { rows, total } = view.view(settled ? shown : undefined);
  const f = t.tokens.filters;
  // The members whose personal tokens are granted here; an owner only the URL names stays pickable.
  const owners = new Map<string, string>(
    (personal.data?.entries ?? []).map((e) => [e.owner_id, e.owner_name] as const),
  );
  if (filters.owner && filters.owner !== ORG_OWNER && !owners.has(filters.owner)) owners.set(filters.owner, filters.owner.slice(0, 8));
  const ownerOptions = [
    { value: ORG_OWNER, label: f.ownerOrg },
    ...[...owners].map(([value, label]) => ({ value, label })).sort((a, b) => a.label.localeCompare(b.label)),
  ];
  const kindLabels = { api: t.tokens.kind.api, assistant: t.tokens.kind.assistant, personal: t.tokens.personal.tag };

  return (
    <>
      <PageHeader
        title={t.tokens.title}
        description={
          <>
            {t.tokens.explorerHint}{' '}
            <a href="/api/explorer" target="_blank" rel="noopener noreferrer" className="text-foreground underline underline-offset-2">
              {t.tokens.explorerLink}
              <span className="sr-only"> {t.shell.opensInNewTab}</span>
            </a>
          </>
        }
        actions={
          <Button variant="primary" onClick={() => setCreateOpen(true)}>
            <KeyRound size={15} /> {t.tokens.create}
          </Button>
        }
      />

      {personal.isError && (
        <p role="alert" className="text-xs text-destructive">
          {t.tokens.personal.loadFailed}
        </p>
      )}
      <DataList
        label={t.tokens.label}
        columns={columns}
        rows={rows}
        rowKey={tokenRowKey}
        rowActions={actions.rowActions}
        rowLabel={(r) => (r.type === 'personal' ? t.tokens.personal.rowLabel(r.name, r.owner_name) : `${r.name} (${r.role})`)}
        loading={!settled}
        empty={active ? t.tokens.noMatch : t.tokens.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={all.length} noun={t.tokens.count} />}>
            <SearchInput
              label={t.tokens.search}
              placeholder={t.tokens.filterPlaceholder}
              value={filters.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            <FilterSelect
              label={f.kind}
              allLabel={f.allKinds}
              options={TOKEN_KINDS.map((k) => ({ value: k, label: kindLabels[k] }))}
              value={filters.kind}
              onChange={(v) => setFilter('kind', v as typeof filters.kind)}
            />
            <FilterSelect label={f.owner} allLabel={f.allOwners} options={ownerOptions} value={filters.owner} onChange={(v) => setFilter('owner', v)} />
            <FilterSelect
              label={f.role}
              allLabel={f.allRoles}
              options={roleOptions(t, TOKEN_ROLES)}
              value={filters.role}
              onChange={(v) => setFilter('role', v as typeof filters.role)}
            />
            <FilterChip pressed={!!filters.expired} tone="crit" onToggle={() => setFilter('expired', filters.expired ? undefined : true)}>
              {f.expired}
            </FilterChip>
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.tokens.count)}
      />

      {actions.dialogs}

      <Dialog
        open={createOpen}
        onClose={closeCreate}
        title={t.tokens.create}
        size={justCreated && createdKind === 'assistant' ? 'wide' : 'default'}
      >
        {justCreated ? (
          <div className="flex flex-col gap-3">
            <p className="text-xs text-muted-foreground">
              {t.tokens.shownOnce}
            </p>
            <code className="break-all rounded-md border border-border bg-background p-3 font-mono text-xs">
              {justCreated}
            </code>
            {createdKind === 'assistant' ? (
              <>
                <div className="flex justify-end">
                  <Button onClick={() => void copy(justCreated)}>{t.tokens.copy}</Button>
                </div>
                <AssistantConnectGuide origin={window.location.origin} orgSlugs={[org]} personal={false} showSecretStep settingsOrgs={settingsOrgs} />
                <div className="flex justify-end">
                  <Button variant="primary" onClick={closeCreate}>
                    {t.tokens.done}
                  </Button>
                </div>
              </>
            ) : (
              <div className="flex justify-end gap-2">
                <Button onClick={() => void copy(justCreated)}>{t.tokens.copy}</Button>
                <Button variant="primary" onClick={closeCreate}>
                  {t.tokens.done}
                </Button>
              </div>
            )}
          </div>
        ) : (
          <form
            className="flex flex-col gap-4"
            onSubmit={form.handleSubmit(async (values) => {
              try {
                const res = await createToken.mutateAsync(
                  values.kind === 'assistant'
                    ? { name: values.name, role: values.role, kind: 'assistant', areas: allowedAreas(values.areas, values.role) }
                    : { name: values.name, role: values.role, kind: 'api' },
                );
                setCreatedKind(values.kind);
                setJustCreated(res.token);
              } catch (err) {
                toast.show(err instanceof Error ? err.message : t.tokens.createFailed, 'destructive');
              }
            })}
          >
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="token_name">{t.tokens.nameLabel}</Label>
              <Input id="token_name" {...form.register('name')} placeholder="ci-deploy" />
              {form.formState.errors.name && (
                <span className="text-[11px] text-destructive">{form.formState.errors.name.message}</span>
              )}
            </div>
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1.5 text-xs font-semibold">{t.tokens.kind.label}</legend>
              <div role="radiogroup" aria-label={t.tokens.kind.label} className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
                {KINDS.map((k) => (
                  <label key={k} className="flex items-center gap-2">
                    <input type="radio" value={k} {...form.register('kind')} />
                    {t.tokens.kind[k]}
                  </label>
                ))}
              </div>
              <span className="text-[11px] text-muted-foreground">
                {kind === 'assistant' ? t.tokens.kind.assistantHint : t.tokens.kind.apiHint}
              </span>
            </fieldset>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="token_role">{t.tokens.roleLabel}</Label>
              <Controller
                control={form.control}
                name="role"
                render={({ field }) => (
                  <Select
                    id="token_role"
                    options={roleOptions(t, ROLES)}
                    value={field.value}
                    onChange={(next) => {
                      field.onChange(next);
                      // A lower role drops the areas it cannot cover, so
                      // raising it again does not bring them back unseen.
                      form.setValue('areas', allowedAreas(form.getValues('areas'), next));
                    }}
                  />
                )}
              />
              <span className="text-[11px] text-muted-foreground">
                {t.tokens.roleHint}
              </span>
            </div>
            {kind === 'assistant' && (
              <Controller
                control={form.control}
                name="areas"
                render={({ field }) => (
                  <AreaGrid
                    role={role}
                    value={field.value}
                    onChange={field.onChange}
                    error={form.formState.errors.areas?.message}
                  />
                )}
              />
            )}
            <div className="flex justify-end gap-2">
              <Button type="button" onClick={() => setCreateOpen(false)}>
                {t.common.cancel}
              </Button>
              <Button type="submit" variant="primary" disabled={createToken.isPending}>
                {t.tokens.create}
              </Button>
            </div>
          </form>
        )}
      </Dialog>
    </>
  );
}
