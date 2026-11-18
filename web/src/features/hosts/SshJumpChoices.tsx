import { useId, type ReactNode } from 'react';
import type { Bastion } from '../../api/types.ts';
import { Select } from '../../ui/Select.tsx';
import { cn } from '../../lib/utils.ts';
import { useT } from '../../i18n/useT.ts';
import type { JumpMode } from './sshDraft.ts';

/**
 * SshJumpChoices is the SSH access dialog's jump host question as three
 * choice cards: inherit (with what that resolves to now), direct, or a
 * specific jump host picked below its card.
 */
export function SshJumpChoices({
  mode, bastionId, bastions, inheritHint, error, disabled, onMode, onBastion, onNewBastion,
}: {
  mode: JumpMode;
  bastionId: string;
  bastions: readonly Bastion[];
  /** What inheriting resolves to, said in the Inherit card. */
  inheritHint: string;
  error?: string;
  disabled: boolean;
  onMode: (mode: JumpMode) => void;
  onBastion: (id: string) => void;
  /** Opens the jump host create dialog; absent without credentials.write. */
  onNewBastion?: (name: string) => void;
}) {
  const t = useT();
  const s = t.hosts.sshDialog;
  return (
    <fieldset className="flex flex-col gap-1.5">
      <legend className="mb-1.5 text-[11px] font-medium text-muted-foreground">{t.hosts.ssh.jumpHost}</legend>
      <div className="grid gap-2 sm:grid-cols-3">
        <Choice value="inherit" mode={mode} title={s.inherit} hint={inheritHint} disabled={disabled} onMode={onMode} />
        <Choice value="none" mode={mode} title={s.direct} hint={s.directHint} disabled={disabled} onMode={onMode} />
        <Choice value="explicit" mode={mode} title={s.explicit} hint={s.explicitHint} disabled={disabled} onMode={onMode} />
      </div>
      {mode === 'explicit' && (
        <div className="flex flex-col gap-1">
          <Select
            aria-label={t.hosts.ssh.jumpHostFor}
            options={bastions.map((b) => ({ value: b.id, label: b.name, hint: `${b.username}@${b.address}:${b.port}` }))}
            value={bastionId}
            onChange={onBastion}
            placeholder={t.hosts.ssh.choose}
            disabled={disabled}
            invalid={!!error}
            create={onNewBastion ? { label: t.hosts.ssh.newJumpHost, onSelect: onNewBastion } : undefined}
          />
          {error && <span className="text-[11px] text-destructive">{error}</span>}
        </div>
      )}
    </fieldset>
  );
}

function Choice({
  value, mode, title, hint, disabled, onMode,
}: {
  value: JumpMode;
  mode: JumpMode;
  title: string;
  hint: ReactNode;
  disabled: boolean;
  onMode: (mode: JumpMode) => void;
}) {
  const id = useId();
  const checked = mode === value;
  return (
    <label
      className={cn(
        'flex cursor-pointer flex-col gap-1 rounded-md border p-2.5 text-xs',
        checked ? 'border-primary bg-accent' : 'border-border hover:bg-accent/50',
        disabled && 'cursor-default opacity-70',
      )}
    >
      <span className="flex items-center gap-2">
        <input
          type="radio"
          name="ssh-jump-mode"
          checked={checked}
          onChange={() => onMode(value)}
          disabled={disabled}
          aria-labelledby={`${id}-title`}
          aria-describedby={`${id}-hint`}
        />
        <span id={`${id}-title`} className="font-medium">
          {title}
        </span>
      </span>
      <span id={`${id}-hint`} className="text-[11px] text-muted-foreground">
        {hint}
      </span>
    </label>
  );
}
