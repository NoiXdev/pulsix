import { useEffect, useState, type ReactNode } from 'react';
import { PlugZap } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Switch } from '../../ui/switch.tsx';
import { CardSaveBar, SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCardForm } from '../../ui/useCardForm.ts';
import { useTestBackupSettings } from '../../api/hooks/useBackups.ts';
import type { BackupSettings, BackupSettingsInput } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { toBody, toDestinationDraft, toScheduleDraft, type DestinationDraft } from './backupDraft.ts';

/** Where the folder destination lives inside the manager container. */
const FOLDER_PATH = '/var/lib/pulsix/backups';

type TestResult = { ok: boolean; text: string } | null;

/**
 * BackupDestinationCard is the Destination card of Settings → Backups:
 * folder or S3-compatible storage, with Test connection probing the
 * destination as drafted, without saving. Saving sends this draft on top of
 * the last saved schedule, through the save it shares with the schedule
 * card (`onSave`, `saving`).
 */
export function BackupDestinationCard({
  settings,
  onSave,
  saving,
  onDirtyChange,
}: {
  settings: BackupSettings;
  onSave: (body: BackupSettingsInput) => Promise<void>;
  saving: boolean;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const t = useT();
  const d = t.backups.destination;
  const toast = useToast();
  const test = useTestBackupSettings();
  const form = useCardForm(toDestinationDraft(settings), { guard: false });
  const { draft, dirty } = form;
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  const [testResult, setTestResult] = useState<TestResult>(null);
  const set = <K extends keyof DestinationDraft>(key: K, value: DestinationDraft[K]) =>
    form.setDraft((cur) => ({ ...cur, [key]: value }));
  const s3 = draft.destination === 's3';
  const lastSavedSchedule = () => toScheduleDraft(settings);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!dirty) return;
    try {
      await onSave(toBody(draft, lastSavedSchedule()));
      // Whatever was typed into the secret field has been sent; the stored
      // secret is now the source of truth again.
      form.markSaved({ ...draft, secretKey: '', removeSecret: false }, draft);
      toast.show(t.backups.saved);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.backups.saveFailed, 'destructive');
    }
  }

  async function onTest() {
    setTestResult(null);
    try {
      await test.mutateAsync(toBody(draft, lastSavedSchedule()));
      setTestResult({ ok: true, text: d.testOk });
    } catch (err) {
      setTestResult({ ok: false, text: err instanceof Error ? err.message : d.testFailed });
    }
  }

  return (
    <form id="backup-destination-form" onSubmit={(e) => void onSubmit(e)}>
      <SettingsCard
        title={d.title}
        description={d.description}
        footer={
          <CardSaveBar
            formId="backup-destination-form"
            dirty={dirty}
            saving={saving}
            onDiscard={form.discard}
            saveLabel={d.save}
          />
        }
      >
        <SettingsRow label={d.typeLabel} labelId="backup_destination_label" stack>
          <div role="radiogroup" aria-labelledby="backup_destination_label" className="flex flex-wrap gap-x-6 gap-y-2 text-[13px]">
            <label className="flex items-center gap-2">
              <input type="radio" name="backup_destination" checked={!s3} onChange={() => set('destination', 'folder')} />
              {d.folder} (<code>{FOLDER_PATH}</code>)
            </label>
            <label className="flex items-center gap-2">
              <input type="radio" name="backup_destination" checked={s3} onChange={() => set('destination', 's3')} />
              {d.s3}
            </label>
          </div>
        </SettingsRow>
        {s3 && (
          <>
            <TextRow id="backup_endpoint" label={d.endpoint} help={d.endpointHint}>
              <Input
                id="backup_endpoint"
                aria-describedby="backup_endpoint_help"
                className="w-72 max-w-full"
                placeholder="https://s3.eu-central-1.example.com"
                value={draft.endpoint}
                onChange={(e) => set('endpoint', e.target.value)}
              />
            </TextRow>
            <TextRow id="backup_region" label={d.region}>
              <Input
                id="backup_region"
                className="w-72 max-w-full"
                placeholder="eu-central-1"
                value={draft.region}
                onChange={(e) => set('region', e.target.value)}
              />
            </TextRow>
            <TextRow id="backup_bucket" label={d.bucket}>
              <Input
                id="backup_bucket"
                className="w-72 max-w-full"
                value={draft.bucket}
                onChange={(e) => set('bucket', e.target.value)}
              />
            </TextRow>
            <TextRow id="backup_prefix" label={d.prefix}>
              <Input
                id="backup_prefix"
                className="w-72 max-w-full"
                placeholder="pulsix/"
                value={draft.prefix}
                onChange={(e) => set('prefix', e.target.value)}
              />
            </TextRow>
            <TextRow id="backup_access_key" label={d.accessKey}>
              <Input
                id="backup_access_key"
                className="w-72 max-w-full"
                autoComplete="off"
                value={draft.accessKey}
                onChange={(e) => set('accessKey', e.target.value)}
              />
            </TextRow>
            <TextRow id="backup_secret_key" label={d.secretKey} help={settings.s3.secret_key_set ? d.secretStored : d.secretNone}>
              <Input
                id="backup_secret_key"
                aria-describedby="backup_secret_key_help"
                type="password"
                className="w-72 max-w-full"
                autoComplete="new-password"
                // A stored credential of a third-party service, never the person's
                // own login: keep password managers from filling or saving it.
                data-1p-ignore=""
                data-lpignore="true"
                data-bwignore=""
                value={draft.secretKey}
                onChange={(e) => set('secretKey', e.target.value)}
              />
            </TextRow>
            {settings.s3.secret_key_set && (
              <SettingsRow label={d.removeSecret} htmlFor="backup_remove_secret" indent>
                <Switch id="backup_remove_secret" checked={draft.removeSecret} onCheckedChange={(c) => set('removeSecret', c)} />
              </SettingsRow>
            )}
            <SettingsRow label={d.pathStyle} htmlFor="backup_path_style">
              <Switch id="backup_path_style" checked={draft.pathStyle} onCheckedChange={(c) => set('pathStyle', c)} />
            </SettingsRow>
          </>
        )}
        <SettingsRow label={d.testLabel} help={d.testHint} helpId="backup_test_help">
          <div className="flex flex-col items-start gap-2 sm:items-end">
            <Button type="button" aria-describedby="backup_test_help" disabled={test.isPending} onClick={() => void onTest()}>
              <PlugZap size={14} /> {d.test}
            </Button>
            {testResult && (
              <p
                role={testResult.ok ? 'status' : 'alert'}
                className={`text-[12.5px] break-words ${testResult.ok ? 'text-muted-foreground' : 'text-destructive'}`}
              >
                {testResult.text}
              </p>
            )}
          </div>
        </SettingsRow>
      </SettingsCard>
    </form>
  );
}

/** A text field's row; a field with help points `aria-describedby` at
 * `${id}_help`. */
function TextRow({ id, label, help, children }: { id: string; label: string; help?: string; children: ReactNode }) {
  return (
    <SettingsRow label={label} htmlFor={id} help={help} helpId={`${id}_help`}>
      {children}
    </SettingsRow>
  );
}
