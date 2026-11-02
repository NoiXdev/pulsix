/**
 * The rule dialog's two searching pickers: a host for a host-scoped rule,
 * and an item for a tuple-scoped one. Split out of RuleDialog.tsx under
 * backlog 3.42 — each carries its own query and its own empty and
 * not-found handling, and neither is reached by the dialog's form logic
 * beyond the value it hands back.
 */
import { useId, useState } from 'react';
import { useT } from '../../i18n/useT.ts';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useHostItems } from '../../api/hooks/useItems.ts';
import { SELECT } from './ruleDialogText.ts';

/**
 * RuleHostPicker chooses the host a host- or item-scoped rule targets.
 * Mirrors CheckDialog's own HostPicker in shape (features/checks): the
 * hosts list is paginated, so it searches through the API rather than
 * filtering one page, and the host a rule already targets stays
 * selectable even when a search does not happen to return it — the same
 * review finding that fixed CheckDialog's binding select (a select whose
 * value matches no option falls back to the empty placeholder and
 * swallows Save with no message).
 *
 * RuleDTO does carry host_name now (backlog 3.30), the same enrichment a
 * check definition has, but only on the rule this dialog already loaded,
 * not on an id this picker's own paginated host search resolves — the
 * editable value here is a plain host id, so a missing current binding
 * (one the search does not happen to return) is still shown by its
 * shortened id rather than a name.
 */
export function RuleHostPicker({ org, value, onChange }: { org: string; value: string; onChange: (id: string) => void }) {
  const t = useT();
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { monitored: true, q });
  const listed = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const currentMissing = value !== '' && !listed.some((h) => h.id === value);

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="rule-host">{t.checks.host}</Label>
      <input
        type="search"
        aria-label={t.checks.hostPicker.searchLabel}
        placeholder={t.checks.hostPicker.searchPlaceholder}
        className={SELECT}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <Select
        id="rule-host"
        placeholder={t.checks.hostPicker.choose}
        required
        value={value}
        onChange={onChange}
        options={[
          ...(currentMissing ? [{ value, label: t.checks.currentBinding(value.slice(0, 8)) }] : []),
          ...listed.map((h) => ({ value: h.id, label: h.name })),
        ]}
      />
      {hosts.hasNextPage && <span className="text-[11px] text-muted-foreground">{t.checks.hostPicker.moreMatch}</span>}
      {!hosts.data && hosts.isError && (
        <span role="alert" className="text-[11px] text-destructive">
          {t.checks.hostPicker.loadFailed} {hosts.error instanceof Error ? hosts.error.message : ''}
        </span>
      )}
    </div>
  );
}

/**
 * RuleItemPicker names the item an item-scoped rule targets: a `<datalist>`
 * over the chosen host's discovered items (GET /hosts/{id}/items), with
 * the field underneath it a plain text input throughout (v0.3b-3 spec,
 * "the rule editor's item field becomes a picker ... with free text still
 * accepted"). A rule may name an item that has not appeared yet — the
 * same decoupling ItemDecisionDialog's own pattern field has from the
 * items it matches — so this never becomes a closed `<select>`: a
 * `<datalist>` suggests without restricting what can be typed or
 * submitted.
 *
 * `checkKey` narrows the suggestions to the check the rule's own metric
 * belongs to (RuleMetric's own check_key) — a mount and a unit are never
 * the same kind of item, and listing every item this host has ever
 * reported, regardless of check, would suggest ones the rule's metric
 * could not possibly apply to.
 *
 * The `<datalist>`'s id is generated per instance with useId rather than a
 * fixed string: a `list=` attribute resolves by id against the whole
 * document, and two mounted pickers sharing one id would silently collide
 * — the browser binds to whichever datalist it finds first, offering one
 * picker's suggestions on the other's input, with no error to notice it by.
 */
export function RuleItemPicker({
  org,
  hostId,
  checkKey,
  value,
  onChange,
}: {
  org: string;
  hostId: string;
  checkKey: string | undefined;
  value: string;
  onChange: (item: string) => void;
}) {
  const t = useT();
  const items = useHostItems(org, hostId, hostId !== '');
  const options = [...new Set((items.data?.items ?? []).filter((it) => it.check_key === checkKey).map((it) => it.item))].sort();
  const optionsId = useId();

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="rule-item">{t.checks.ruleDialog.item}</Label>
      <Input
        id="rule-item"
        required
        list={optionsId}
        placeholder={t.checks.ruleDialog.itemPlaceholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <datalist id={optionsId}>
        {options.map((name) => (
          <option key={name} value={name} />
        ))}
      </datalist>
    </div>
  );
}
