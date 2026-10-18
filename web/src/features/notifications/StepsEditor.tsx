import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import type { AlertRouteStep } from '../../api/types.ts';
import type { RouteDialogChannel, RouteDialogRotation } from './RouteDialog.tsx';
import { cumulativeDelays, formatElapsed } from './steps.ts';

type StepSource = 'fixed' | 'rotation';

/** A step's own source, the same question RouteDialog's top-level
 * channelSource answers for the route itself: rotation_id DEFINED (not
 * merely truthy) means rotation, otherwise fixed — never both
 * (ValidateSteps' own either/or rule, route.go), which every mutation
 * below preserves by construction rather than by checking it afterwards.
 * Checking definedness rather than truthiness matters the moment a rung
 * switches to "rotation" before a rotation is chosen: rotation_id is set
 * to '' (so a half-built step still names rotation as its one source,
 * never silently falls back to reading as fixed, which '' as a falsy
 * value would otherwise do). Not exported — stepsAreComplete (steps.ts)
 * re-derives the same reading on its own rather than importing this,
 * since that function lives in a plain-functions-only file a component
 * may not be exported from (react-refresh/only-export-components). */
function sourceOf(step: AlertRouteStep): StepSource {
  return step.rotation_id !== undefined ? 'rotation' : 'fixed';
}

export interface StepsEditorProps {
  steps: AlertRouteStep[];
  onChange: (steps: AlertRouteStep[]) => void;
  channels: RouteDialogChannel[];
  rotations: RouteDialogRotation[];
}

/**
 * StepsEditor edits a route's escalation ladder: an ordered list of rungs,
 * each firing its own delay after the rung before it (the initial delivery
 * for rung 1) if the alert is still open and unacknowledged. It never
 * reimplements alertroutes.Route.ValidateSteps — the server is still the
 * one place that rule lives — but it does keep the editor from ever being
 * able to COMPOSE a shape that rule would reject for a structural reason:
 *
 *   - Positions are never user-entered. addStep appends len+1; removeStep
 *     renumbers every remaining rung to 1..n afterward. A gap can only
 *     exist in a ladder this editor never had the chance to produce.
 *   - A rung is always exactly one of rotation_id/channel_ids, flipped by
 *     setSource below rather than left to whatever the two fields
 *     happened to hold — "neither" is a shape ValidateSteps rejects and
 *     this editor never passes through an intermediate state that is it.
 *
 * What it does NOT prevent: a fixed-source rung with zero channels
 * ticked, or a rotation-source rung with none chosen yet — both legal
 * while a user is mid-edit, the same way RouteDialog's own top-level
 * channel section allows an incomplete selection and gates Save instead
 * of the checkbox itself. stepsAreComplete (steps.ts) is what RouteDialog
 * folds into its own `valid` for exactly that reason.
 */
export function StepsEditor({ steps, onChange, channels, rotations }: StepsEditorProps) {
  function addStep() {
    onChange([...steps, { position: steps.length + 1, delay_seconds: 300, channel_ids: [] }]);
  }

  function removeStep(index: number) {
    onChange(steps.filter((_, i) => i !== index).map((s, i) => ({ ...s, position: i + 1 })));
  }

  function updateStep(index: number, patch: Partial<AlertRouteStep>) {
    onChange(steps.map((s, i) => (i === index ? { ...s, ...patch } : s)));
  }

  function setSource(index: number, source: StepSource) {
    // Switching source always clears the other field outright — never
    // left set alongside the new one — so the step a user is mid-editing
    // can never pass through "both" on its way from one source to the
    // other, however this function gets called.
    updateStep(index, source === 'rotation' ? { rotation_id: '', channel_ids: undefined } : { channel_ids: [], rotation_id: undefined });
  }

  function toggleChannel(index: number, channelId: string) {
    const current = steps[index].channel_ids ?? [];
    const next = current.includes(channelId) ? current.filter((id) => id !== channelId) : [...current, channelId];
    updateStep(index, { channel_ids: next });
  }

  // Running total of every rung's own delay, the input formatElapsed needs
  // to name each rung by when it actually fires rather than by its own
  // isolated wait.
  const cumulativeAt = cumulativeDelays(steps);

  return (
    <fieldset className="flex flex-col gap-2 rounded-md border border-border p-2.5">
      <legend className="px-1 text-[11px] font-medium text-muted-foreground">Escalation ladder</legend>
      <p className="text-xs text-muted-foreground">
        The initial delivery above goes out {formatElapsed(0)}. Each rung below fires if the alert is still open and
        unacknowledged once its own wait — counted from the rung before it — has passed.
      </p>
      {steps.length === 0 && <p className="text-xs text-muted-foreground">No escalation — only the initial delivery.</p>}
      <ol className="flex flex-col gap-2">
        {steps.map((step, i) => {
          const source = sourceOf(step);
          const groupName = `step-${i}-source`;
          return (
            <li key={i} className="flex flex-col gap-1.5 rounded-md border border-border p-2">
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-medium">Step {step.position}</span>
                <span className="text-[11px] text-muted-foreground">{formatElapsed(cumulativeAt[i])}</span>
                <Button type="button" variant="ghost" onClick={() => removeStep(i)}>
                  Remove
                </Button>
              </div>
              <div className="flex items-center gap-2">
                <Label htmlFor={`step-${i}-delay`}>Wait</Label>
                <Input
                  id={`step-${i}-delay`}
                  type="number"
                  min={1}
                  required
                  className="w-20"
                  value={Math.round(step.delay_seconds / 60)}
                  // No minimum clamp here: a field that snaps back to "1"
                  // on every keystroke can never be cleared to type a new
                  // multi-digit value over it (the field never reads
                  // empty, so each new digit appends instead of replacing).
                  // `min={1}` above still blocks a 0-or-negative delay from
                  // ever reaching Save through the browser's own
                  // constraint validation, the same posture every other
                  // bounded number field in this dialog already takes.
                  onChange={(e) => updateStep(i, { delay_seconds: Math.max(0, Number(e.target.value) || 0) * 60 })}
                />
                <span className="text-xs text-muted-foreground">min after the previous rung</span>
              </div>
              <div role="radiogroup" aria-label={`Step ${step.position} channel source`} className="flex gap-3">
                <label className="flex items-center gap-1.5 text-xs">
                  <input type="radio" name={groupName} checked={source === 'fixed'} onChange={() => setSource(i, 'fixed')} />
                  Fixed list
                </label>
                <label className="flex items-center gap-1.5 text-xs">
                  <input type="radio" name={groupName} checked={source === 'rotation'} onChange={() => setSource(i, 'rotation')} />
                  On-call rotation
                </label>
              </div>
              {source === 'fixed' ? (
                <div className="flex flex-wrap gap-3">
                  {channels.map((c) => (
                    <label key={c.id} className="flex items-center gap-1.5 text-xs">
                      <input
                        type="checkbox"
                        checked={(step.channel_ids ?? []).includes(c.id)}
                        onChange={() => toggleChannel(i, c.id)}
                      />
                      {c.name}
                    </label>
                  ))}
                  {channels.length === 0 && <span className="text-xs text-muted-foreground">Add a notification channel first.</span>}
                </div>
              ) : (
                <>
                  <Select
                    aria-label="Rotation"
                    options={rotations.map((r) => ({ value: r.id, label: r.name }))}
                    value={step.rotation_id ?? ''}
                    onChange={(v) => updateStep(i, { rotation_id: v })}
                    placeholder="Choose a rotation…"
                    required
                  />
                  {rotations.length === 0 && <span className="text-xs text-muted-foreground">Set up an on-call rotation first.</span>}
                </>
              )}
            </li>
          );
        })}
      </ol>
      <Button type="button" variant="ghost" onClick={addStep}>
        Add rung
      </Button>
    </fieldset>
  );
}
