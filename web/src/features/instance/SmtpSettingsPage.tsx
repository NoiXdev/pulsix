import { useState } from 'react';
import { Send } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Pill } from '../../ui/badge.tsx';
import { SettingsCard, SettingsPage, SettingsRow } from '../../ui/settings.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useSaveSmtpSettings, useSmtpSettings, useTestSmtpSettings } from '../../api/hooks/useSmtpSettings.ts';
import { useT } from '../../i18n/useT.ts';
import { SmtpServerCard } from './SmtpServerCard.tsx';

/**
 * SmtpSettingsPage is the instance's outgoing mail configuration, for
 * instance administrators: GET/PUT /admin/settings/smtp and the test
 * endpoint. The setup wizard lets an operator skip mail, and without it
 * invitations cannot be e-mailed (they still work — the link is shown to
 * whoever invites); this is where mail is configured afterwards.
 *
 * It lives under an organization's Settings only for navigation; the
 * settings are instance-wide, which the page says. Two cards: the SMTP
 * server (SmtpServerCard) and a test e-mail.
 */
export function SmtpSettingsPage() {
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
  const smtp = t.instance.smtp;
  return (
    <SettingsPage
      title={t.settings.tabs.mail}
      description={smtp.hint}
      badge={current.configured ? <Pill tone="ok">{smtp.configured}</Pill> : <Pill tone="warn">{smtp.notConfigured}</Pill>}
    >
      <SmtpServerCard
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
            toast.show(smtp.saved);
            return true;
          } catch (err) {
            toast.show(err instanceof Error ? err.message : smtp.saveFailed, 'destructive');
            return false;
          }
        }}
      />

      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (!testTo.trim()) return;
          try {
            await test.mutateAsync(testTo.trim());
            toast.show(smtp.test.sent(testTo.trim()));
          } catch (err) {
            toast.show(err instanceof Error ? err.message : smtp.test.failed, 'destructive');
          }
        }}
      >
        <SettingsCard title={smtp.test.title} description={smtp.test.description}>
          <SettingsRow label={smtp.test.recipient} htmlFor="smtp_test_to">
            <div className="flex flex-wrap items-center gap-2">
              <Input
                id="smtp_test_to"
                type="email"
                className="w-72 max-w-full"
                value={testTo}
                onChange={(e) => setTestTo(e.target.value)}
              />
              <Button type="submit" disabled={!current.configured || test.isPending}>
                <Send size={14} /> {smtp.test.send}
              </Button>
            </div>
          </SettingsRow>
        </SettingsCard>
      </form>
    </SettingsPage>
  );
}
