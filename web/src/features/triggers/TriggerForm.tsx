import { useState, type FormEvent } from 'react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import type { Trigger, TriggerInput } from '../../api/types.ts';
import type { TriggerTarget } from '../../api/hooks/useTriggers.ts';
import { PRESET_LABEL } from './presets.ts';

const ON_ACTIVE_LABEL: Record<'queue' | 'skip', string> = { queue: 'queue another run', skip: 'skip' };
const RATE_WINDOWS = ['1m', '5m', '1h'];

interface Row {
  name: string;
  path: string;
}

/** TriggerForm edits one trigger: name, sender preset, replay header,
 * allowed sources, what happens while a run is active, the rate limit and
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
  const [name, setName] = useState(initial?.name ?? '');
  const [preset, setPreset] = useState<TriggerInput['preset']>(initial?.preset ?? 'pulsix');
  const [timestampHeader, setTimestampHeader] = useState(initial?.timestamp_header ?? '');
  const [sources, setSources] = useState((initial?.allow_cidrs ?? []).join('\n'));
  const [onActive, setOnActive] = useState<'queue' | 'skip'>(initial?.on_active ?? 'queue');
  const [count, setCount] = useState(initial?.rate_limit.count ?? 10);
  const [per, setPer] = useState(initial?.rate_limit.per ?? '1m');
  const [rows, setRows] = useState<Row[]>(Object.entries(initial?.mapping.extra_vars ?? {}).map(([n, p]) => ({ name: n, path: p })));
  const [limitPath, setLimitPath] = useState(initial?.mapping.limit ?? '');

  function submit(e: FormEvent) {
    e.preventDefault();
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
      ...(target.kind === 'template_run' ? { mapping: { extra_vars: vars, ...(limitPath ? { limit: limitPath } : {}) } } : {}),
    });
  }

  return (
    <form className="flex flex-col gap-2" onSubmit={submit}>
      <Label htmlFor="trigger-name">Name</Label>
      <Input id="trigger-name" value={name} onChange={(e) => setName(e.target.value)} />
      <Label htmlFor="trigger-preset">Sender</Label>
      <Select
        id="trigger-preset"
        options={Object.entries(PRESET_LABEL).map(([k, v]) => ({ value: k, label: v }))}
        value={preset}
        onChange={(value) => setPreset(value as TriggerInput['preset'])}
      />
      {(preset === 'pulsix' || preset === 'github' || preset === 'gitea') && (
        <>
          <Label htmlFor="trigger-ts">Replay protection header</Label>
          <Input id="trigger-ts" className="font-mono" value={timestampHeader} onChange={(e) => setTimestampHeader(e.target.value)} placeholder="X-Pulsix-Timestamp (optional)" />
        </>
      )}
      <Label htmlFor="trigger-sources">Allowed sources</Label>
      <Input id="trigger-sources" className="font-mono" value={sources} onChange={(e) => setSources(e.target.value)} placeholder="any source; or CIDRs, comma separated" />
      <Label htmlFor="trigger-active">If a run is active</Label>
      <Select
        id="trigger-active"
        options={(Object.keys(ON_ACTIVE_LABEL) as Array<'queue' | 'skip'>).map((k) => ({ value: k, label: ON_ACTIVE_LABEL[k] }))}
        value={onActive}
        onChange={(value) => setOnActive(value as 'queue' | 'skip')}
      />
      <div className="flex gap-2">
        <div className="flex flex-1 flex-col gap-1">
          <Label htmlFor="trigger-count">Calls</Label>
          <Input id="trigger-count" type="number" min={1} max={1000} value={count} onChange={(e) => setCount(Number(e.target.value))} />
        </div>
        <div className="flex flex-1 flex-col gap-1">
          <Label htmlFor="trigger-per">Per</Label>
          <Select id="trigger-per" options={RATE_WINDOWS.map((p) => ({ value: p, label: p }))} value={per} onChange={setPer} />
        </div>
      </div>
      {target.kind === 'template_run' && (
        <fieldset className="flex flex-col gap-1">
          <legend className="text-[11px] font-medium text-muted-foreground">Payload mapping</legend>
          {rows.map((r, i) => (
            <div key={i} className="flex gap-1">
              <Input aria-label={`Variable ${i + 1}`} className="font-mono" value={r.name} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} placeholder="release" />
              <Input aria-label={`Path ${i + 1}`} className="font-mono" value={r.path} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, path: e.target.value } : x)))} placeholder="$.ref" />
            </div>
          ))}
          <Button type="button" variant="ghost" onClick={() => setRows([...rows, { name: '', path: '' }])}>
            + Map a variable
          </Button>
          <Label htmlFor="trigger-limit">Limit path</Label>
          <Input id="trigger-limit" className="font-mono" value={limitPath} onChange={(e) => setLimitPath(e.target.value)} placeholder="$.hosts[*]" />
        </fieldset>
      )}
      {error && <p className="text-xs text-[var(--px-crit)]">{error}</p>}
      <div className="flex justify-end gap-2">
        <Button type="button" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={pending}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}
