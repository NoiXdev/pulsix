import { Label } from '../../ui/label.tsx';
import { useT } from '../../i18n/useT.ts';
import { FieldError } from '../instance/shared/formParts.tsx';
import { HARDWARE_PARAM, type HardwarePlugin } from './hardwareParams.ts';

const PLACEHOLDER: Record<HardwarePlugin, string> = {
  temperature: 'coretemp_core_0\nnvme_composite',
  smart: '/dev/sda\n/dev/nvme0',
  raid: 'md0\nmd1',
};

/**
 * HardwareListField edits the one list parameter of a temperature, smart
 * or raid check (backlog 3.9): sensors, devices or arrays, one per line,
 * empty meaning all of them.
 */
export function HardwareListField({
  idPrefix, plugin, value, error, disabled, onChange,
}: {
  /** Prefixes the textarea's id, so two forms never share one. */
  idPrefix: string;
  plugin: HardwarePlugin;
  value: string;
  error?: string;
  disabled?: boolean;
  onChange: (value: string) => void;
}) {
  const t = useT();
  const text = t.checks.hardware[HARDWARE_PARAM[plugin]];
  const id = `${idPrefix}-${HARDWARE_PARAM[plugin]}`;
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{text.label}</Label>
      <textarea
        id={id}
        className="min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs"
        value={value}
        disabled={disabled}
        placeholder={PLACEHOLDER[plugin]}
        aria-invalid={error ? true : undefined}
        aria-describedby={`${id}-hint`}
        onChange={(e) => onChange(e.target.value)}
      />
      <p id={`${id}-hint`} className="text-[11px] text-muted-foreground">{text.hint}</p>
      <FieldError>{error}</FieldError>
    </div>
  );
}
