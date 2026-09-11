import { useState, type ReactNode } from 'react';
import { useParams } from '@tanstack/react-router';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { Mail, Send } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { SettingsTabs } from '../../ui/SettingsTabs.tsx';
import { useSaveSmtpSettings, useSmtpSettings, useTestSmtpSettings } from '../../api/hooks/useSmtpSettings.ts';
import { smtpSchema, type SmtpFormValues } from '../../lib/schemas.ts';

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
        <h1 className="text-xl font-semibold">Outgoing mail</h1>
        <SettingsTabs org={org} />
      </div>
      <div className="flex items-center gap-3">
        <span className="text-[13px] text-muted-foreground">
          Instance-wide: used for every organization&apos;s invitations.
        </span>
        {current.configured ? <Pill tone="ok">configured</Pill> : <Pill tone="warn">not configured</Pill>}
      </div>

      <div className="grid grid-cols-2 items-start gap-4">
        <Card>
          <SmtpForm
            key={current.configured ? `${current.host}:${current.port}:${current.username}:${current.from}` : 'empty'}
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
                toast.show('Mail settings saved');
              } catch (err) {
                toast.show(err instanceof Error ? err.message : 'failed to save mail settings', 'destructive');
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
                toast.show(`Test e-mail sent to ${testTo.trim()}`);
              } catch (err) {
                toast.show(err instanceof Error ? err.message : 'the test e-mail could not be sent', 'destructive');
              }
            }}
          >
            <h2 className="text-sm font-semibold">Send a test e-mail</h2>
            <p className="text-[11px] text-muted-foreground">
              Uses the saved settings. A failure shows the mail server&apos;s own answer.
            </p>
            <Field id="smtp_test_to" label="Recipient">
              <Input id="smtp_test_to" type="email" value={testTo} onChange={(e) => setTestTo(e.target.value)} />
            </Field>
            <div className="flex justify-end">
              <Button type="submit" disabled={!current.configured || test.isPending}>
                <Send size={14} /> Send test e-mail
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
  saving,
  onSave,
}: {
  initial: SmtpFormValues;
  saving: boolean;
  onSave: (values: SmtpFormValues) => Promise<void>;
}) {
  const form = useForm<SmtpFormValues>({ resolver: zodResolver(smtpSchema), defaultValues: initial });
  const errors = form.formState.errors;
  return (
    <form
      className="flex flex-col gap-3 p-4"
      onSubmit={form.handleSubmit(async (values) => {
        await onSave(values);
        form.resetField('password', { defaultValue: '' });
      })}
    >
      <h2 className="text-sm font-semibold">SMTP server</h2>
      <Field id="smtp_host" label="Host" error={errors.host?.message}>
        <Input id="smtp_host" {...form.register('host')} />
      </Field>
      <Field id="smtp_port" label="Port" error={errors.port?.message}>
        <Input id="smtp_port" type="number" {...form.register('port', { valueAsNumber: true })} />
      </Field>
      <Field id="smtp_username" label="Username">
        <Input id="smtp_username" autoComplete="off" {...form.register('username')} />
      </Field>
      <Field id="smtp_password" label="Password">
        <Input id="smtp_password" type="password" autoComplete="new-password" {...form.register('password')} />
      </Field>
      <p className="text-[11px] text-muted-foreground">
        The stored password is never shown. Saving replaces it with what is typed here — enter it again, or leave
        it empty for a server that needs none.
      </p>
      <Field id="smtp_from" label="From address" error={errors.from?.message}>
        <Input id="smtp_from" type="email" {...form.register('from')} />
      </Field>
      <label className="flex items-center gap-2 text-xs">
        <input type="checkbox" {...form.register('start_tls')} />
        Use STARTTLS
      </label>
      <div className="flex justify-end">
        <Button type="submit" variant="primary" disabled={saving}>
          <Mail size={14} /> Save mail settings
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
