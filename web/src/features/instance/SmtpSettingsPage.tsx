import { useMemo, useState, type ReactNode } from 'react';
import { useParams } from '@tanstack/react-router';
import { Controller, useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { Mail, Send } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Switch } from '../../ui/switch.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { SettingsTabs } from '../settings/SettingsTabs.tsx';
import { useSaveSmtpSettings, useSmtpSettings, useTestSmtpSettings } from '../../api/hooks/useSmtpSettings.ts';
import { schemas, type SmtpFormValues, type SmtpSaveValues } from '../../lib/schemas.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * SmtpSettingsPage is the instance's outgoing mail configuration, for
 * instance administrators: GET/PUT /admin/settings/smtp and the test
 * endpoint. The setup wizard lets an operator skip mail, and without it
 * invitations cannot be e-mailed (they still work — the link is shown to
 * whoever invites); this is where mail is configured afterwards.
 *
 * It lives under an organization's Settings only for navigation; the
 * settings are instance-wide, which the page says. The stored password is
 * never sent back, so saving always sends the password field as typed.
 */
export function SmtpSettingsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const t = useT();
  const settings = useSmtpSettings();
  const save = useSaveSmtpSettings();
  const test = useTestSmtpSettings();
  const [testTo, setTestTo] = useState('');

  const guard = useQueryGuard(settings);
  if (guard) return guard;
  if (!settings.data) return <QueryLoading />;

  const current = settings.data;
  return (
    <>
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold">{t.settings.tabs.mail}</h1>
        <SettingsTabs org={org} />
      </div>
      <div className="flex items-center gap-3">
        <span className="text-[13px] text-muted-foreground">{t.instance.smtp.hint}</span>
        {current.configured ? (
          <Pill tone="ok">{t.instance.smtp.configured}</Pill>
        ) : (
          <Pill tone="warn">{t.instance.smtp.notConfigured}</Pill>
        )}
      </div>

      <div className="grid grid-cols-2 items-start gap-4">
        <Card>
          <SmtpForm
            key={current.configured ? `${current.host}:${current.port}:${current.username}:${current.from}` : 'empty'}
            configured={current.configured}
            initial={{
              host: current.host ?? '',
              port: current.port ?? 587,
              username: current.username ?? '',
              password: '',
              from: current.from ?? '',
              start_tls: current.configured ? (current.start_tls ?? false) : true,
            }}
            saving={save.isPending}
            onSave={async (values) => {
              try {
                await save.mutateAsync(values);
                save.reset();
                toast.show(t.instance.smtp.saved);
              } catch (err) {
                toast.show(err instanceof Error ? err.message : t.instance.smtp.saveFailed, 'destructive');
              }
            }}
          />
        </Card>

        <Card>
          <form
            className="flex flex-col gap-3 p-4"
            onSubmit={async (e) => {
              e.preventDefault();
              if (!testTo.trim()) return;
              try {
                await test.mutateAsync(testTo.trim());
                toast.show(t.instance.smtp.test.sent(testTo.trim()));
              } catch (err) {
                toast.show(err instanceof Error ? err.message : t.instance.smtp.test.failed, 'destructive');
              }
            }}
          >
            <h2 className="text-sm font-semibold">{t.instance.smtp.test.title}</h2>
            <p className="text-[11px] text-muted-foreground">{t.instance.smtp.test.hint}</p>
            <Field id="smtp_test_to" label={t.instance.smtp.test.recipient}>
              <Input id="smtp_test_to" type="email" value={testTo} onChange={(e) => setTestTo(e.target.value)} />
            </Field>
            <div className="flex justify-end">
              <Button type="submit" disabled={!current.configured || test.isPending}>
                <Send size={14} /> {t.instance.smtp.test.send}
              </Button>
            </div>
          </form>
        </Card>
      </div>
    </>
  );
}

function SmtpForm({
  initial,
  configured,
  saving,
  onSave,
}: {
  initial: SmtpFormValues;
  /** Whether a password is stored, which decides if there is anything to
   * remove — the checkbox only makes sense then. */
  configured: boolean;
  saving: boolean;
  onSave: (values: SmtpSaveValues) => Promise<void>;
}) {
  const t = useT();
  const { smtpSchema } = useMemo(() => schemas(t), [t]);
  const form = useForm<SmtpFormValues>({ resolver: zodResolver(smtpSchema), defaultValues: initial });
  const errors = form.formState.errors;
  const [removeStored, setRemoveStored] = useState(false);
  return (
    <form
      className="flex flex-col gap-3 p-4"
      onSubmit={form.handleSubmit(async (values) => {
        // An untouched password field means "leave the stored one alone":
        // the page cannot show the password back, so an empty box is the
        // normal state of every save that changes something else. Only a
        // typed value replaces it, and only the explicit checkbox below
        // clears it.
        const { password, ...rest } = values;
        await onSave(removeStored ? { ...rest, password: '' } : password ? values : rest);
        form.resetField('password', { defaultValue: '' });
        setRemoveStored(false);
      })}
    >
      <h2 className="text-sm font-semibold">{t.instance.smtp.serverTitle}</h2>
      <Field id="smtp_host" label={t.instance.smtp.host} error={errors.host?.message}>
        <Input id="smtp_host" {...form.register('host')} />
      </Field>
      <Field id="smtp_port" label={t.instance.smtp.port} error={errors.port?.message}>
        <Input id="smtp_port" type="number" {...form.register('port', { valueAsNumber: true })} />
      </Field>
      <Field id="smtp_username" label={t.instance.smtp.username}>
        <Input id="smtp_username" autoComplete="off" {...form.register('username')} />
      </Field>
      <Field id="smtp_password" label={t.auth.password}>
        <Input id="smtp_password" type="password" autoComplete="new-password" {...form.register('password')} />
      </Field>
      <p className="text-[11px] text-muted-foreground">{t.instance.smtp.passwordHint}</p>
      {configured && (
        <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
          <Switch
            id="smtp_remove_stored"
            checked={removeStored}
            onCheckedChange={setRemoveStored}
            aria-label={t.instance.smtp.removeStoredAria}
          />
          <label htmlFor="smtp_remove_stored">{t.instance.smtp.removeStored}</label>
        </div>
      )}
      <Field id="smtp_from" label={t.instance.smtp.fromAddress} error={errors.from?.message}>
        <Input id="smtp_from" type="email" {...form.register('from')} />
      </Field>
      <div className="flex items-center gap-2 text-xs">
        <Controller
          control={form.control}
          name="start_tls"
          render={({ field }) => <Switch id="smtp_start_tls" checked={field.value} onCheckedChange={field.onChange} />}
        />
        <label htmlFor="smtp_start_tls">{t.instance.smtp.startTls}</label>
      </div>
      <div className="flex justify-end">
        <Button type="submit" variant="primary" disabled={saving}>
          <Mail size={14} /> {t.instance.smtp.save}
        </Button>
      </div>
    </form>
  );
}

function Field({ id, label, error, children }: { id: string; label: string; error?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error && <span className="text-[11px] text-destructive">{error}</span>}
    </div>
  );
}
