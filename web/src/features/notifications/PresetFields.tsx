import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import type { Messages } from '../../i18n/types.ts';
import { BUILT_URL, SECRET_NAMES, type Preset, type PresetSettings, type SecretName } from './presetSettings.ts';

interface Props {
  preset: Preset;
  settings: PresetSettings;
  onSettings: (s: PresetSettings) => void;
  secrets: Partial<Record<SecretName, string>>;
  onSecret: (name: SecretName, value: string) => void;
  /** The secrets stored for this preset, kept when their field is empty. */
  stored: readonly string[];
  d: Messages['notifications']['channelDialog'];
}

/** PresetFields are the settings, secrets and hints of the presets beyond
 * the URL (backlog 4.8): ntfy's token hint, Twilio's account, numbers and
 * cost note, PagerDuty's region, and every preset's secrets. */
export function PresetFields({ preset, settings, onSettings, secrets, onSecret, stored, d }: Props) {
  const names = SECRET_NAMES[preset] ?? [];
  return (
    <>
      {preset === 'ntfy' && <p className="text-[11px] text-muted-foreground">{d.ntfyToken}</p>}
      {BUILT_URL.has(preset) && <p className="text-[11px] text-muted-foreground">{d.builtUrl}</p>}
      {preset === 'twilio' && (
        <>
          <p className="text-[11px] text-[var(--px-warn)]">{d.twilioCost}</p>
          <Label htmlFor="ch-account-sid">{d.accountSid}</Label>
          <Input id="ch-account-sid" className="font-mono" value={settings.accountSid} onChange={(e) => onSettings({ ...settings, accountSid: e.target.value })} required maxLength={64} placeholder="AC…" />
          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="ch-from">{d.from}</Label>
              <Input id="ch-from" className="font-mono" value={settings.from} onChange={(e) => onSettings({ ...settings, from: e.target.value })} required maxLength={32} placeholder={d.phonePlaceholder} />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ch-to">{d.to}</Label>
              <Input id="ch-to" className="font-mono" value={settings.to} onChange={(e) => onSettings({ ...settings, to: e.target.value })} required maxLength={32} placeholder={d.phonePlaceholder} />
            </div>
          </div>
        </>
      )}
      {preset === 'pagerduty' && (
        <>
          <p className="text-[11px] text-muted-foreground">{d.pagerdutyEvents}</p>
          <Label htmlFor="ch-region">{d.region}</Label>
          <Select
            id="ch-region"
            options={(['us', 'eu'] as const).map((r) => ({ value: r, label: d.regions[r] }))}
            value={settings.region}
            onChange={(v) => onSettings({ ...settings, region: v as 'us' | 'eu' })}
          />
        </>
      )}
      {names.map((name) => {
        const kept = stored.includes(name);
        return (
          <div key={name} className="flex flex-col gap-1">
            <Label htmlFor={`ch-secret-${name}`}>{d.secretNames[name]}</Label>
            <Input
              id={`ch-secret-${name}`}
              type="password"
              className="font-mono"
              autoComplete="off"
              value={secrets[name] ?? ''}
              onChange={(e) => onSecret(name, e.target.value)}
              placeholder={kept ? d.keep : undefined}
              required={!kept}
            />
          </div>
        );
      })}
      {names.length > 0 && <p className="text-[11px] text-muted-foreground">{d.secretsStored}</p>}
    </>
  );
}
