import { useState, type FormEvent } from 'react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import type { Trigger, TriggerInput } from '../../api/types.ts';
import type { TriggerTarget } from '../../api/hooks/useTriggers.ts';
import { useT } from '../../i18n/useT.ts';
import { PRESETS, presetLabel } from './presets.ts';
import { MAX_WAIT_FACTOR, settleBody, settleProblem } from './settle.ts';

const RATE_WINDOWS = ['1m', '5m', '1h'];

interface Row {
  name: string;
  path: string;
}

/** TriggerForm edits one trigger: name, sender preset, replay header,
 * allowed sources, what happens while a run is active, the rate limit, the
 * settle delay (wait for a quiet moment, run at the latest after; backlog
 * 2.137, checked against the server's bounds before it is sent) and
 * (template runs only) the payload mapping. */
export function TriggerForm({ target, initial, submitLabel, pending, error, onSubmit, onCancel }: {
  target: TriggerTarget;
  initial?: Trigger;
  submitLabel: string;
  pending: boolean;
  error: string | null;
  onSubmit: (body: TriggerInput) => void;
  onCancel: () => void;
}) {
  const t = useT();
  const f = t.triggers.form;
  const [name, setName] = useState(initial?.name ?? '');
  const [preset, setPreset] = useState<TriggerInput['preset']>(initial?.preset ?? 'pulsix');
  const [timestampHeader, setTimestampHeader] = useState(initial?.timestamp_header ?? '');
  const [sources, setSources] = useState((initial?.allow_cidrs ?? []).join('\n'));
  const [onActive, setOnActive] = useState<'queue' | 'skip'>(initial?.on_active ?? 'queue');
  const [count, setCount] = useState(initial?.rate_limit.count ?? 10);
  const [per, setPer] = useState(initial?.rate_limit.per ?? '1m');
  const [rows, setRows] = useState<Row[]>(Object.entries(initial?.mapping.extra_vars ?? {}).map(([n, p]) => ({ name: n, path: p })));
  const [limitPath, setLimitPath] = useState(initial?.mapping.limit ?? '');
  const [settle, setSettle] = useState(String(initial?.settle_seconds ?? 0));
  const [maxWait, setMaxWait] = useState(initial?.max_wait_seconds ? String(initial.max_wait_seconds) : '');
  const [problem, setProblem] = useState<'settle' | 'maxWait' | null>(null);
  const settleValue = /^\d+$/.test(settle.trim()) ? Number(settle.trim()) : 0;

  function submit(e: FormEvent) {
    e.preventDefault();
    const refused = settleProblem(settle, maxWait);
    setProblem(refused);
    if (refused) return;
    const allow = sources.split(/[\n,]/).map((s) => s.trim()).filter(Boolean);
    const vars = Object.fromEntries(rows.filter((r) => r.name && r.path).map((r) => [r.name, r.path]));
    onSubmit({
      kind: target.kind,
      ...(target.kind === 'template_run' ? { template_id: target.id } : { repo_id: target.id }),
      name,
      enabled: initial?.enabled ?? true,
      preset,
      ...(timestampHeader ? { timestamp_header: timestampHeader } : {}),
      allow_cidrs: allow,
      on_active: onActive,
      rate_limit: { count, per },
      ...settleBody(settle, maxWait),
      ...(target.kind === 'template_run' ? { mapping: { extra_vars: vars, ...(limitPath ? { limit: limitPath } : {}) } } : {}),
    });
  }

  return (
    <form className="flex flex-col gap-2" onSubmit={submit}>
      <Label htmlFor="trigger-name">{f.name}</Label>
      <Input id="trigger-name" value={name} onChange={(e) => setName(e.target.value)} />
      <Label htmlFor="trigger-preset">{f.sender}</Label>
      <Select
        id="trigger-preset"
        options={PRESETS.map((k) => ({ value: k, label: presetLabel(k, t.triggers.preset) }))}
        value={preset}
        onChange={(value) => setPreset(value as TriggerInput['preset'])}
      />
      {(preset === 'pulsix' || preset === 'github' || preset === 'gitea') && (
        <>
          <Label htmlFor="trigger-ts">{f.replayHeader}</Label>
          <Input id="trigger-ts" className="font-mono" value={timestampHeader} onChange={(e) => setTimestampHeader(e.target.value)} placeholder={f.replayPlaceholder} />
        </>
      )}
      <Label htmlFor="trigger-sources">{f.allowedSources}</Label>
      <Input id="trigger-sources" className="font-mono" value={sources} onChange={(e) => setSources(e.target.value)} placeholder={f.sourcesPlaceholder} />
      <Label htmlFor="trigger-active">{f.ifActive}</Label>
      <Select
        id="trigger-active"
        options={(['queue', 'skip'] as const).map((k) => ({ value: k, label: f.onActive[k] }))}
        value={onActive}
        onChange={(value) => setOnActive(value as 'queue' | 'skip')}
      />
      <div className="flex gap-2">
        <div className="flex flex-1 flex-col gap-1">
          <Label htmlFor="trigger-count">{f.calls}</Label>
          <Input id="trigger-count" type="number" min={1} max={1000} value={count} onChange={(e) => setCount(Number(e.target.value))} />
        </div>
        <div className="flex flex-1 flex-col gap-1">
          <Label htmlFor="trigger-per">{f.per}</Label>
          <Select id="trigger-per" options={RATE_WINDOWS.map((p) => ({ value: p, label: p }))} value={per} onChange={setPer} />
        </div>
      </div>
      <Label htmlFor="trigger-settle">{f.settle}</Label>
      <Input id="trigger-settle" inputMode="numeric" value={settle} onChange={(e) => setSettle(e.target.value)} aria-describedby="trigger-settle-hint" />
      <p id="trigger-settle-hint" className="text-[11px] text-muted-foreground">
        {f.settleHint}
      </p>
      {problem === 'settle' && <p className="text-xs text-[var(--px-crit)]">{f.settleRange}</p>}
      {settleValue > 0 && (
        <>
          <Label htmlFor="trigger-max-wait">{f.maxWait}</Label>
          <Input id="trigger-max-wait" inputMode="numeric" value={maxWait} onChange={(e) => setMaxWait(e.target.value)} placeholder={f.maxWaitPlaceholder(MAX_WAIT_FACTOR * settleValue)} />
          {problem === 'maxWait' && <p className="text-xs text-[var(--px-crit)]">{f.maxWaitRange(settleValue)}</p>}
        </>
      )}
      {target.kind === 'template_run' && (
        <fieldset className="flex flex-col gap-1">
          <legend className="text-[11px] font-medium text-muted-foreground">{f.mapping}</legend>
          {rows.map((r, i) => (
            <div key={i} className="flex gap-1">
              <Input aria-label={f.variable(i + 1)} className="font-mono" value={r.name} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} placeholder="release" />
              <Input aria-label={f.path(i + 1)} className="font-mono" value={r.path} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, path: e.target.value } : x)))} placeholder="$.ref" />
            </div>
          ))}
          <Button type="button" variant="ghost" onClick={() => setRows([...rows, { name: '', path: '' }])}>
            {f.mapVariable}
          </Button>
          <Label htmlFor="trigger-limit">{f.limitPath}</Label>
          <Input id="trigger-limit" className="font-mono" value={limitPath} onChange={(e) => setLimitPath(e.target.value)} placeholder="$.hosts[*]" />
        </fieldset>
      )}
      {error && <p className="text-xs text-[var(--px-crit)]">{error}</p>}
      <div className="flex justify-end gap-2">
        <Button type="button" onClick={onCancel}>
          {t.common.cancel}
        </Button>
        <Button type="submit" variant="primary" disabled={pending}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}
