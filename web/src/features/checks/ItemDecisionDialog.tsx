import { useEffect, useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Tag } from '../../ui/badge.tsx';
import { useCreateItemDecision, useItemDecisionPreview, useUpdateItemDecision } from '../../api/hooks/useItems.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import type { ItemDecision, ItemDecisionInput, ItemDecisionMatchKind, ItemDecisionPreviewInput, ItemDecisionScope, ItemDecisionVerdict } from '../../api/types.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground disabled:opacity-50';
const TEXTAREA =
  'min-h-24 w-full rounded-md border border-border bg-background p-2 text-xs text-foreground placeholder:text-muted-foreground';

/** How long the pattern field stays quiet before the preview asks — the
 * same 300ms MonitoringGroupsPage's own selector preview uses: a request
 * per keystroke would be a self-inflicted load problem, and the field is
 * typed character by character the same way a selector is. Scope, host,
 * check key and match kind are selects an operator changes deliberately
 * rather than types, so those are read live, with no debounce of their
 * own. */
const PREVIEW_DEBOUNCE_MS = 300;

/** confirmBroadMatch is the second, explicit confirmation a pattern
 * matching more than the preview can show in full has to pass before it
 * is saved — the same spirit as RuleDialog's one-way mode-switch warning.
 * A glob that catches more than its author intended is exactly the
 * failure mode this milestone exists to prevent (review focus 1): naming
 * the real count here, not the capped list length, is what makes the
 * second click a deliberate one rather than a reflex. */
function confirmBroadMatch(total: number): string {
  return (
    `This pattern currently matches ${total} items — more than the preview lists in full. ` +
    'Saving changes every one of them. Continue?'
  );
}

export interface ItemDecisionDialogProps {
  org: string;
  /** The decision being edited; undefined creates one. */
  decision?: ItemDecision;
  /** The check keys this organization's checks resolve — the same
   * catalogue (GET /rules/metrics, collapsed to its check keys)
   * RuleDialog's own metric picker draws from, so this picker cannot name
   * a check key items.Service would refuse. */
  checkKeys: string[];
  onClose: () => void;
}

/**
 * ItemDecisionDialog is the pattern editor: it creates or edits one item
 * decision, previewing what the pattern currently matches on a debounce
 * before anyone has chosen ignore or watch (preview reads only the fields
 * that decide a match — scope, host, check key, match kind, pattern — see
 * useItemDecisionPreview's own doc comment).
 *
 * Organization scope never offers "watch": watching is already what
 * happens by default, so an organization-wide watch would promise an
 * effect it cannot have. The option — and a line saying why it is absent
 * — only appears once scope is switched to a single host, which is the
 * overrule it exists for.
 *
 * A pattern previewing at more than the preview can list in full needs a
 * second, explicit confirmation before Save actually persists it,
 * mirroring RuleDialog's one-way mode-switch warning for the same reason:
 * a wide blast radius should cost one more deliberate act, not a single
 * click that reads the same as a narrow one.
 */
export function ItemDecisionDialog({ org, decision, checkKeys, onClose }: ItemDecisionDialogProps) {
  const editing = !!decision;
  const create = useCreateItemDecision(org);
  const update = useUpdateItemDecision(org, decision?.id ?? '');

  const [scope, setScope] = useState<ItemDecisionScope>(decision?.scope ?? 'org');
  const [hostId, setHostId] = useState(decision?.host_id ?? '');
  const [checkKey, setCheckKey] = useState(decision?.check_key ?? checkKeys[0] ?? '');
  const [matchKind, setMatchKind] = useState<ItemDecisionMatchKind>(decision?.match_kind ?? 'exact');
  const [pattern, setPattern] = useState(decision?.pattern ?? '');
  const [verdict, setVerdict] = useState<ItemDecisionVerdict>(decision?.decision ?? 'ignore');
  const [note, setNote] = useState(decision?.note ?? '');
  const [preview, setPreview] = useState<{ total: number; capped: boolean } | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Watch at organization scope is never offered — it changes nothing, so
  // switching to org scope while watch is selected falls back to ignore,
  // the only verdict that can have an effect there.
  function onScopeChange(next: ItemDecisionScope) {
    setScope(next);
    if (next === 'org') {
      setHostId('');
      setVerdict((v) => (v === 'watch' ? 'ignore' : v));
    }
  }

  function body(): ItemDecisionInput {
    return {
      scope,
      ...(scope === 'host' ? { host_id: hostId } : {}),
      check_key: checkKey,
      match_kind: matchKind,
      pattern: pattern.trim(),
      decision: verdict,
      ...(note.trim() ? { note: note.trim() } : {}),
    };
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (preview?.capped && !confirm(confirmBroadMatch(preview.total))) return;
    try {
      if (editing) await update.mutateAsync(body());
      else await create.mutateAsync(body());
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  const title = editing ? `Edit decision for ${decision.pattern}` : 'New item decision';
  const saveDisabled = create.isPending || update.isPending;

  return (
    <Dialog open onClose={onClose} title={title} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <div className="grid grid-cols-3 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="decision-scope">Applies to</Label>
            <select
              id="decision-scope"
              className={SELECT}
              value={scope}
              onChange={(e) => onScopeChange(e.target.value as ItemDecisionScope)}
            >
              <option value="org">The whole organization</option>
              <option value="host">One host</option>
            </select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="decision-check">Check</Label>
            <select id="decision-check" className={SELECT} required value={checkKey} onChange={(e) => setCheckKey(e.target.value)}>
              {checkKeys.length === 0 && <option value="">No checks yet</option>}
              {/* An edited decision's own check key, when the catalogue this
                  dialog was given does not happen to contain it — the same
                  fix RuleDialog's own group select carries: a select whose
                  value matches no option falls back silently to the empty
                  placeholder and swallows Save with no message. */}
              {checkKey !== '' && !checkKeys.includes(checkKey) && <option value={checkKey}>{`${checkKey} (current binding)`}</option>}
              {checkKeys.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="decision-match-kind">Match kind</Label>
            <select
              id="decision-match-kind"
              className={SELECT}
              value={matchKind}
              onChange={(e) => setMatchKind(e.target.value as ItemDecisionMatchKind)}
            >
              <option value="exact">Exact — one literal item name</option>
              <option value="glob">Glob — a &apos;*&apos; pattern</option>
            </select>
          </div>
        </div>
        {scope === 'host' && <DecisionHostPicker org={org} value={hostId} onChange={setHostId} />}
        <div className="flex flex-col gap-1">
          <Label htmlFor="decision-pattern">Pattern</Label>
          <Input
            id="decision-pattern"
            required
            maxLength={512}
            className="font-mono"
            placeholder={matchKind === 'glob' ? '/var/*, backup-*' : '/var, sshd'}
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
          />
        </div>
        <PatternPreview org={org} scope={scope} hostId={hostId} checkKey={checkKey} matchKind={matchKind} pattern={pattern} onResult={setPreview} />
        <div className="flex flex-col gap-1">
          <Label htmlFor="decision-verdict">Verdict</Label>
          <select id="decision-verdict" className={SELECT} value={verdict} onChange={(e) => setVerdict(e.target.value as ItemDecisionVerdict)}>
            <option value="ignore">Ignore — stop judging this item</option>
            {scope === 'host' && <option value="watch">Watch — judge it even though a wider decision ignores it</option>}
          </select>
          {scope === 'org' && (
            <p className="text-[11px] text-muted-foreground">
              Watch is not offered at organization scope: watching is already what happens by default, so an organization-wide watch
              would change nothing. It is available once this decision is scoped to a single host, which is the overrule it exists for.
            </p>
          )}
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="decision-note">Note</Label>
          <textarea
            id="decision-note"
            className={TEXTAREA}
            maxLength={500}
            placeholder="Why does this item not need judging? The next operator reads this before switching it back on."
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
        </div>
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={saveDisabled}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** previewBody builds the preview request's body, or null while scope,
 * host (when scope is host), check key or pattern are not all set yet —
 * the same "nothing worth asking yet" null-guard SelectorPreview and
 * RuleExpression each give their own debounced field. It never includes a
 * `decision` key: the preview ignores it, so there is nothing to put there
 * even once the operator has picked one — see useItemDecisionPreview's own
 * doc comment. */
function previewBody(
  scope: ItemDecisionScope,
  hostId: string,
  checkKey: string,
  matchKind: ItemDecisionMatchKind,
  pattern: string,
): ItemDecisionPreviewInput | null {
  const trimmed = pattern.trim();
  if (trimmed === '' || checkKey === '' || (scope === 'host' && hostId === '')) return null;
  return {
    scope,
    ...(scope === 'host' ? { host_id: hostId } : {}),
    check_key: checkKey,
    match_kind: matchKind,
    pattern: trimmed,
  };
}

/**
 * PatternPreview resolves what the pattern currently typed would match,
 * rendering the server's own total rather than the length of the capped
 * match list it also carries — "matches 4 182 items" is the sentence that
 * stops someone saving `*` (review focus 1). It previews as soon as scope,
 * host, check key, match kind and pattern are set, independent of whether
 * a verdict has been chosen yet: Preview validates none of that (see
 * previewBody's own doc comment).
 *
 * It does not judge the pattern itself — an exact pattern containing a
 * glob character, say, is reported with the service's own message, which
 * names what is wrong and suggests the fix; a second, client-side check
 * here could only drift from the one that decides.
 */
function PatternPreview({
  org,
  scope,
  hostId,
  checkKey,
  matchKind,
  pattern,
  onResult,
}: {
  org: string;
  scope: ItemDecisionScope;
  hostId: string;
  checkKey: string;
  matchKind: ItemDecisionMatchKind;
  pattern: string;
  onResult: (result: { total: number; capped: boolean } | null) => void;
}) {
  const [debouncedPattern, setDebouncedPattern] = useState(pattern);
  useEffect(() => {
    const t = setTimeout(() => setDebouncedPattern(pattern), PREVIEW_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [pattern]);

  const body = previewBody(scope, hostId, checkKey, matchKind, debouncedPattern);
  const preview = useItemDecisionPreview(org, body);

  useEffect(() => {
    onResult(preview.data ? { total: preview.data.total, capped: preview.data.capped } : null);
    // onResult is the dialog's setPreview, a stable setState reference;
    // depending on it as well would only risk re-running this on every
    // parent render for no reason.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preview.data]);

  if (body === null) {
    return <span className="text-[11px] text-muted-foreground">Type a pattern to see which items it would match.</span>;
  }
  if (preview.isError) {
    return (
      <p role="alert" className="text-xs text-[var(--px-crit)]">
        {preview.error instanceof Error ? preview.error.message : 'Could not resolve this pattern'}
      </p>
    );
  }
  if (!preview.data) {
    return <span className="text-[11px] text-muted-foreground">Matching…</span>;
  }
  const { total, matches, capped } = preview.data;
  if (total === 0) {
    return (
      <span className="text-xs font-semibold text-[var(--px-warn)]">
        Matches no currently known item — it still applies the moment one appears
      </span>
    );
  }
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-xs font-semibold text-[var(--px-ok)]">
        Matches {total} item{total === 1 ? '' : 's'}
      </span>
      {capped && (
        <span className="text-[11px] text-muted-foreground">
          Showing the first {matches?.length ?? 0} below — narrow the pattern to see the rest.
        </span>
      )}
      <div className="flex max-h-24 flex-wrap gap-1 overflow-auto">
        {(matches ?? []).map((m, i) => (
          <Tag key={`${m.host_id}-${m.item}-${i}`}>{m.item}</Tag>
        ))}
      </div>
    </div>
  );
}

/**
 * DecisionHostPicker chooses the host a host-scoped decision targets.
 * Mirrors RuleDialog's own RuleHostPicker in shape: the hosts list is
 * paginated, so it searches through the API rather than filtering one
 * page, and the host a decision already targets stays selectable even
 * when a search does not happen to return it.
 */
function DecisionHostPicker({ org, value, onChange }: { org: string; value: string; onChange: (id: string) => void }) {
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { monitored: true, q });
  const listed = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const currentMissing = value !== '' && !listed.some((h) => h.id === value);

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="decision-host">Host</Label>
      <input
        type="search"
        aria-label="Search monitored hosts"
        placeholder="Search monitored hosts…"
        className={SELECT}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <select id="decision-host" className={SELECT} required value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Choose a monitored host…</option>
        {currentMissing && <option value={value}>{`${value.slice(0, 8)} (current binding)`}</option>}
        {listed.map((h) => (
          <option key={h.id} value={h.id}>
            {h.name}
          </option>
        ))}
      </select>
      {hosts.hasNextPage && <span className="text-[11px] text-muted-foreground">More hosts match than are listed — narrow the search.</span>}
    </div>
  );
}
