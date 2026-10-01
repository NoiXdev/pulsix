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

/** The server's own bound on a pattern (items.MaxPatternLen), in BYTES —
 * which is what items.ValidPattern counts. The field's `maxLength` counts
 * UTF-16 code units instead, and a code unit is one to three UTF-8 bytes
 * (four, as a surrogate pair), so 512 characters of multibyte item names
 * sail past the browser's cap and come back 422 from the server. Keeping
 * maxLength as well is still worth it — it is a coarse cap that can never
 * block a pattern the server would accept, since UTF-8 bytes are never
 * fewer than UTF-16 units — but patternBytes below is the bound that
 * actually agrees with the server's. */
const PATTERN_MAX_BYTES = 512;

/** utf8Length counts p as the server counts it: UTF-8 bytes. */
function utf8Length(p: string): number {
  return new TextEncoder().encode(p).length;
}

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
  const [error, setError] = useState<string | null>(null);

  // The preview query lives here, not inside PatternPreview, because the
  // confirmation gate below needs to know — synchronously, on every
  // render — whether it describes the pattern currently typed. A version
  // that let a child own the query and report back through a callback
  // (an earlier round of this file) left a window open: the callback only
  // fires from an effect, so clicking Save inside the 300ms debounce saw
  // whatever the *previous* preview said (often "nothing yet", i.e. no
  // confirmation at all) rather than blocking until a fresh one existed.
  const [debouncedPattern, setDebouncedPattern] = useState(pattern);
  useEffect(() => {
    const t = setTimeout(() => setDebouncedPattern(pattern), PREVIEW_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [pattern]);

  const liveBody = previewBody(scope, hostId, checkKey, matchKind, pattern);
  const debouncedBody = previewBody(scope, hostId, checkKey, matchKind, debouncedPattern);
  const preview = useItemDecisionPreview(org, debouncedBody);

  // Stale exactly when there is something worth previewing at all
  // (liveBody) and this dialog does not have a count for that exact
  // input: either the debounce has not caught up with what is currently
  // typed, or the request for the debounced value has not landed a
  // count. Save stays disabled for the whole of this window — the review
  // finding this fixes: typing a broad pattern and clicking Save before
  // the preview ever ran skipped both the preview and the confirmation
  // it exists to require.
  //
  // A FAILED preview counts as stale too (it used to be the one way out
  // of this gate): the count is the entire brake on a pattern that
  // catches the fleet, and `capped` is undefined on an error, so an
  // errored preview re-enabled Save AND silently skipped the second
  // confirmation. With retry: false on the query, one network blip while
  // `*` is typed was enough. The error text is already on screen; what
  // this adds is that it blocks rather than merely informs.
  const previewStale =
    liveBody !== null && (pattern !== debouncedPattern || preview.isFetching || preview.isError || preview.data === undefined);

  // Watch at organization scope is never offered — it changes nothing (and
  // the server now refuses to store it at all), so switching to org scope
  // while watch is selected falls back to ignore, the only verdict that
  // can have an effect there.
  function onScopeChange(next: ItemDecisionScope) {
    setScope(next);
    if (next === 'org') {
      setHostId('');
      setVerdict((v) => (v === 'watch' ? 'ignore' : v));
    }
  }

  // True only for a decision loaded with a combination the server itself
  // now refuses to ever write (org scope, watch verdict) — reachable only
  // through a row written before that rule existed, or by hand, never
  // through this dialog's own write path (onScopeChange above already
  // clamps it on every live scope change). Surfaced truthfully rather
  // than silently coerced to "ignore": a <select> whose bound value
  // matches no rendered <option> shows its sole remaining option as
  // selected while leaving the real value untouched, which previously
  // meant the screen read "Ignore" while Save would have re-persisted
  // "Watch" underneath it — showing the stored value truthfully and
  // refusing to save beats showing one thing and submitting another.
  const orgWatchMismatch = scope === 'org' && verdict === 'watch';
  // Measured on the trimmed pattern, because that is exactly what body()
  // below submits and therefore what the server will measure.
  const patternTooLong = utf8Length(pattern.trim()) > PATTERN_MAX_BYTES;

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
    // Belt and braces alongside the disabled Save button below: a <button
    // disabled> does not fire a click, but this guard keeps the decision
    // in one place rather than trusting every path to the form down to
    // the DOM's own submit semantics.
    if (previewStale || orgWatchMismatch || patternTooLong) return;
    if (preview.data?.capped && !confirm(confirmBroadMatch(preview.data.total))) return;
    try {
      if (editing) await update.mutateAsync(body());
      else await create.mutateAsync(body());
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  const title = editing ? `Edit decision for ${decision.pattern}` : 'New item decision';
  const saveDisabled = create.isPending || update.isPending || previewStale || orgWatchMismatch || patternTooLong;

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
            maxLength={PATTERN_MAX_BYTES}
            className="font-mono"
            placeholder={matchKind === 'glob' ? '/var/*, backup-*' : '/var, sshd'}
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
          />
          {patternTooLong && (
            <p role="alert" className="text-xs text-[var(--px-crit)]">
              This pattern is {utf8Length(pattern.trim())} bytes long; the limit is {PATTERN_MAX_BYTES}. Item names outside the ASCII
              range cost more than one byte each, so a pattern can pass the field&apos;s own character count and still be too long.
            </p>
          )}
        </div>
        <PatternPreview body={debouncedBody} query={preview} />
        <div className="flex flex-col gap-1">
          <Label htmlFor="decision-verdict">Verdict</Label>
          <select id="decision-verdict" className={SELECT} value={verdict} onChange={(e) => setVerdict(e.target.value as ItemDecisionVerdict)}>
            <option value="ignore">Ignore — stop judging this item</option>
            {scope === 'host' && <option value="watch">Watch — judge it even though a wider decision ignores it</option>}
            {/* Renders the true stored value for the org+watch mismatch
                case (see orgWatchMismatch's own comment) instead of letting
                the select silently show "Ignore" for a "Watch" it still
                holds — disabled, since this combination is not something
                choosing it again from here could ever produce. */}
            {orgWatchMismatch && (
              <option value="watch" disabled>
                Watch (stored outside this editor — has no effect at organization scope)
              </option>
            )}
          </select>
          {scope === 'org' && !orgWatchMismatch && (
            <p className="text-[11px] text-muted-foreground">
              Watch is not offered at organization scope: watching is already what happens by default, so an organization-wide watch
              would change nothing. It is available once this decision is scoped to a single host, which is the overrule it exists for.
            </p>
          )}
          {orgWatchMismatch && (
            <p role="alert" className="text-xs text-[var(--px-crit)]">
              This decision is stored as an organization-wide watch, which has no effect and the server now refuses to save. Choose
              ignore, or scope it to a host to keep watch as the overrule it is meant to be.
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
 * PatternPreview renders what the pattern currently debounced would match,
 * from the query the dialog already fetched (useItemDecisionPreview lives
 * in the parent now — see ItemDecisionDialog's own comment on `preview`
 * for why rendering and the confirmation gate must read the same query
 * rather than a child's own, separately-timed copy of it).
 *
 * It shows the server's own total rather than the length of the capped
 * match list it also carries — "matches 4 182 items" is the sentence that
 * stops someone saving `*` (review focus 1) — and does not judge the
 * pattern itself: an exact pattern containing a glob character, say, is
 * reported with the service's own message, which names what is wrong and
 * suggests the fix; a second, client-side check here could only drift
 * from the one that decides.
 */
function PatternPreview({
  body,
  query,
}: {
  body: ItemDecisionPreviewInput | null;
  query: ReturnType<typeof useItemDecisionPreview>;
}) {
  if (body === null) {
    return <span className="text-[11px] text-muted-foreground">Type a pattern to see which items it would match.</span>;
  }
  if (query.isError) {
    return (
      <p role="alert" className="text-xs text-[var(--px-crit)]">
        {query.error instanceof Error ? query.error.message : 'Could not resolve this pattern'}
      </p>
    );
  }
  if (!query.data) {
    return <span className="text-[11px] text-muted-foreground">Matching…</span>;
  }
  const { total, matches, capped } = query.data;
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
