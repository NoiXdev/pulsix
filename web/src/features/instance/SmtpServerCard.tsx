import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Controller, useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { Input } from '../../ui/input.tsx';
import { Switch } from '../../ui/switch.tsx';
import { CardSaveBar, SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { useUnsavedGuard } from '../../ui/useUnsavedGuard.ts';
import { schemas, type SmtpFormValues, type SmtpSaveValues } from '../../lib/schemas.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * SmtpServerCard is the SMTP server card of the Outgoing mail page: host,
 * port, credentials, sender and STARTTLS as one form with a save bar that
 * is live only while something changed. The stored password is never sent
 * back, so an empty password field means "leave it alone"; a typed value
 * replaces it and only the explicit switch clears it. `onSave` answers
 * whether the save went through.
 *
 * Like useCardForm: while the card is clean it follows `initial` (the
 * server's values, refetched), a dirty draft is left alone, and Discard
 * brings back the latest `initial`. A save keeps whatever was typed while
 * its request was in flight (R6).
 */
export function SmtpServerCard({
  initial,
  configured,
  saving,
  onSave,
}: {
  initial: SmtpFormValues;
  /** Whether a password is stored, which decides if there is anything to
   * remove — the switch only makes sense then. */
  configured: boolean;
  saving: boolean;
  onSave: (values: SmtpSaveValues) => Promise<boolean>;
}) {
  const t = useT();
  const smtp = t.instance.smtp;
  const { smtpSchema } = useMemo(() => schemas(t), [t]);
  const form = useForm<SmtpFormValues>({ resolver: zodResolver(smtpSchema), defaultValues: initial });
  const errors = form.formState.errors;
  const [removeStored, setRemoveStored] = useState(false);
  const dirty = form.formState.isDirty || removeStored;
  useUnsavedGuard(dirty);

  const defaults = form.formState.defaultValues;
  useEffect(() => {
    if (!dirty && !same(defaults, initial)) form.reset(initial);
  }, [dirty, defaults, initial, form]);

  const submit = form.handleSubmit(async (values) => {
    // An untouched password field means "leave the stored one alone": the
    // page cannot show the password back, so an empty box is the normal
    // state of every save that changes something else.
    const { password, ...rest } = values;
    const ok = await onSave(removeStored ? { ...rest, password: '' } : password ? values : rest);
    if (!ok) return;
    // The saved values become the baseline; a field changed while the save
    // was in flight keeps its new value and stays dirty.
    const now = form.getValues();
    form.reset({ ...values, password: '' });
    for (const key of Object.keys(values) as (keyof SmtpFormValues)[]) {
      if (!same(now[key], values[key])) form.setValue(key, now[key], { shouldDirty: true });
    }
    setRemoveStored(false);
  });

  return (
    <form id="smtp-server-form" onSubmit={(e) => void submit(e)}>
      <SettingsCard
        title={smtp.serverTitle}
        description={smtp.serverDescription}
        footer={
          <CardSaveBar
            formId="smtp-server-form"
            dirty={dirty}
            saving={saving}
            saveLabel={smtp.save}
            onDiscard={() => {
              form.reset(initial);
              setRemoveStored(false);
            }}
          />
        }
      >
        <SettingsRow label={smtp.host} htmlFor="smtp_host">
          <Field error={errors.host?.message}>
            <Input id="smtp_host" className="w-72 max-w-full" {...form.register('host')} />
          </Field>
        </SettingsRow>
        <SettingsRow label={smtp.port} htmlFor="smtp_port">
          <Field error={errors.port?.message}>
            <Input id="smtp_port" type="number" className="w-28" {...form.register('port', { valueAsNumber: true })} />
          </Field>
        </SettingsRow>
        <SettingsRow label={smtp.username} htmlFor="smtp_username">
          <Input id="smtp_username" autoComplete="off" className="w-72 max-w-full" {...form.register('username')} />
        </SettingsRow>
        <SettingsRow label={t.auth.password} htmlFor="smtp_password" help={smtp.passwordHint} helpId="smtp_password_help">
          <Input
            id="smtp_password"
            aria-describedby="smtp_password_help"
            type="password"
            autoComplete="new-password"
            className="w-72 max-w-full"
            {...form.register('password')}
          />
        </SettingsRow>
        {configured && (
          <SettingsRow label={smtp.removeStored} htmlFor="smtp_remove_stored" indent>
            <Switch
              id="smtp_remove_stored"
              checked={removeStored}
              onCheckedChange={setRemoveStored}
              aria-label={smtp.removeStoredAria}
            />
          </SettingsRow>
        )}
        <SettingsRow label={smtp.fromAddress} htmlFor="smtp_from">
          <Field error={errors.from?.message}>
            <Input id="smtp_from" type="email" className="w-72 max-w-full" {...form.register('from')} />
          </Field>
        </SettingsRow>
        <SettingsRow label={smtp.startTls} htmlFor="smtp_start_tls">
          <Controller
            control={form.control}
            name="start_tls"
            render={({ field }) => <Switch id="smtp_start_tls" checked={field.value} onCheckedChange={field.onChange} />}
          />
        </SettingsRow>
      </SettingsCard>
    </form>
  );
}

function Field({ error, children }: { error?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      {children}
      {error && <span className="text-[12.5px] text-destructive">{error}</span>}
    </div>
  );
}

function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}
