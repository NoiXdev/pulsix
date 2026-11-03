import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { PulsixMark } from '../../ui/PulsixMark.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { schemas, type SmtpFormValues } from '../../lib/schemas.ts';
import { Select } from '../../ui/Select.tsx';
import { useLocale, useT } from '../../i18n/useT.ts';
import { LOCALES, LOCALE_NAMES, isLocale, type Messages } from '../../i18n/types.ts';
import { completeSetup, refetchSetupStatus, useSetupStatus } from '../../api/hooks/useSetupStatus.ts';
import { ApiError } from '../../api/client.ts';
import { useRefetchMe } from '../../api/hooks/useMe.ts';
import { useResetSession } from '../../lib/session.ts';
import { useQueryClient } from '@tanstack/react-query';
import { SlugField } from '../../ui/SlugField.tsx';

type StepKey = 'admin' | 'org' | 'smtp' | 'mfa';

/** The command that prints the setup token again (pulsix-manager
 * setup-token in the stock compose deployment). */
const SETUP_TOKEN_COMMAND = 'docker compose run --rm cli setup-token';
const STEP_KEYS: readonly StepKey[] = ['admin', 'org', 'smtp', 'mfa'];

// The repeat is checked here rather than on the field, because a mismatch
// is a fact about the pair: zod's refine runs once both have a value and
// reports against the repeat, which is the field the person just left.
//
// This is the one password in the product nobody can recover. A typo in
// it locks the instance out of its own first administrator, and there is
// no second administrator to undo it with — which is why the wizard asks
// twice and the login form does not.
function wizardSchemas(t: Messages) {
  const { emailSchema, passwordSchema, setupOrgSlugSchema, smtpSchema } = schemas(t);
  const adminSchema = z
    .object({
      setup_token: z.string().trim().min(1, t.validation.required),
      admin_email: emailSchema,
      admin_password: passwordSchema,
      admin_password_repeat: z.string().min(1, t.validation.required),
      admin_name: z.string().min(1, t.validation.required),
    })
    .refine((v) => v.admin_password === v.admin_password_repeat, {
      message: t.validation.passwordsMismatch,
      path: ['admin_password_repeat'],
    });
  const orgSchema = z.object({
    org_name: z.string().min(1, t.validation.required),
    org_slug: setupOrgSlugSchema,
  });
  return { adminSchema, orgSchema, smtpSchema };
}
type AdminValues = z.infer<ReturnType<typeof wizardSchemas>['adminSchema']>;
type OrgValues = z.infer<ReturnType<typeof wizardSchemas>['orgSchema']>;

/** SetupWizardPage — design/mockups/build.mjs's SETUP WIZARD artboard: a
 * fixed 420px brand/progress panel on the left, the current step's form
 * centered on the right. Runs once; POST /setup/complete only fires from
 * the final step, carrying every field collected along the way. */
export function SetupWizardPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const refetchMe = useRefetchMe();
  const resetSession = useResetSession();
  const setupStatus = useSetupStatus();
  const t = useT();
  // The manager logs a link that carries the setup token (?token=); the
  // field starts from it so following the link is all it takes.
  const search = useSearch({ strict: false }) as { token?: unknown };
  const linkToken = typeof search.token === 'string' ? search.token : '';
  const steps = STEP_KEYS.map((key) => ({ key, ...t.setup.steps[key] }));
  // The wizard's language is the one it is shown in: before setup the
  // provider starts from the browser's, and choosing another here only
  // previews it — it is saved as the instance default by POST
  // /setup/complete below, there being nowhere to save it before that.
  const { locale, previewLocale } = useLocale();
  const { adminSchema, orgSchema, smtpSchema } = useMemo(() => wizardSchemas(t), [t]);
  const [step, setStep] = useState<StepKey>('admin');
  const [admin, setAdmin] = useState<AdminValues | null>(null);
  const [org, setOrg] = useState<OrgValues | null>(null);
  const [smtp, setSmtp] = useState<SmtpFormValues | null>(null);
  const [smtpEnabled, setSmtpEnabled] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Cheap fix (task-15a-report.md fix round 1): /setup had no guard on an
  // already-completed instance — a stale bookmark or link would let
  // anyone re-run the wizard's UI (POST /setup/complete itself already
  // refuses this server-side, but the page should not even be reachable).
  useEffect(() => {
    if (setupStatus.data?.completed) {
      void navigate({ to: '/' });
    }
  }, [setupStatus.data?.completed, navigate]);

  const stepIndex = steps.findIndex((s) => s.key === step);

  const adminForm = useForm<AdminValues>({
    resolver: zodResolver(adminSchema),
    defaultValues: admin ?? { setup_token: linkToken },
  });
  const orgForm = useForm<OrgValues>({ resolver: zodResolver(orgSchema), defaultValues: org ?? undefined });
  const smtpForm = useForm<SmtpFormValues>({
    resolver: zodResolver(smtpSchema),
    defaultValues: smtp ?? { host: '', port: 587, username: '', password: '', from: '', start_tls: true },
  });

  async function finish() {
    if (!admin || !org) return;
    setSubmitting(true);
    setError(null);
    try {
      await completeSetup({
        setup_token: admin.setup_token,
        admin_email: admin.admin_email,
        admin_password: admin.admin_password,
        admin_name: admin.admin_name,
        org_slug: org.org_slug,
        org_name: org.org_name,
        smtp: smtpEnabled && smtp ? smtp : undefined,
        default_locale: locale,
      });
      // The response set a session cookie for a new principal: start from
      // an empty cache (lib/session.ts), then refetch and await both
      // ['me'] and ['setup-status'] before navigating away, so neither is
      // left stale for RootRedirect to bounce off of later (Important 3,
      // task-15a-report.md fix round 1). The new administrator has no
      // second factor yet, so the session can only enrol one.
      resetSession();
      await Promise.all([refetchMe(), refetchSetupStatus(qc)]);
      await navigate({ to: '/account/enrol' });
    } catch (err) {
      // A refused token belongs to the first step's field: go back there
      // and say so next to it, rather than at the end of the wizard.
      if (err instanceof ApiError && err.status === 403) {
        setStep('admin');
        adminForm.setError('setup_token', { message: t.setup.admin.tokenMismatch }, { shouldFocus: true });
        return;
      }
      setError(err instanceof Error ? err.message : t.setup.failed);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="flex min-h-screen bg-background text-foreground text-[13px]">
      <div className="flex w-[420px] shrink-0 flex-col gap-7 border-r border-border bg-card p-10">
        <div className="flex items-center gap-3">
          <PulsixMark size={36} />
          <span className="font-mono text-[26px] font-extrabold tracking-tight">
            pulsi<span className="text-brand">x</span>
          </span>
        </div>
        <div className="flex flex-col gap-1.5">
          <h1 className="text-xl font-semibold">{t.setup.title}</h1>
          <p className="text-muted-foreground">{t.setup.intro}</p>
        </div>
        <div className="flex flex-col gap-4">
          {steps.map((s, i) => (
            <div key={s.key} className="flex items-start gap-3">
              <span
                className={
                  i < stepIndex
                    ? 'flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-ok text-[12px] font-bold text-background'
                    : i === stepIndex
                      ? 'flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary text-[12px] font-bold text-primary-foreground'
                      : 'flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-border text-[12px] font-bold text-muted-foreground'
                }
              >
                {i < stepIndex ? '✓' : i + 1}
              </span>
              <span className="flex flex-col gap-0.5">
                <span className={i === stepIndex ? 'font-semibold' : i < stepIndex ? '' : 'text-muted-foreground'}>
                  {s.label}
                </span>
                <span className="text-[11px] text-muted-foreground">{s.sub}</span>
              </span>
            </div>
          ))}
        </div>
      </div>

      <div className="flex flex-1 items-center justify-center p-12">
        <div className="flex w-[520px] flex-col gap-5">
          <span className="text-[11px] font-semibold tracking-widest text-brand uppercase">
            {t.setup.stepOf(stepIndex + 1, steps.length)}
          </span>

          {step === 'admin' && (
            <form
              className="flex flex-col gap-4"
              onSubmit={adminForm.handleSubmit((values) => {
                setAdmin(values);
                setStep('org');
              })}
            >
              <h2 className="text-xl font-semibold">{t.setup.admin.title}</h2>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="setup_locale">{t.language.label}</Label>
                <Select
                  id="setup_locale"
                  options={LOCALES.map((l) => ({ value: l, label: LOCALE_NAMES[l] }))}
                  value={locale}
                  onChange={(v) => {
                    if (isLocale(v)) previewLocale(v);
                  }}
                />
                <span className="text-[11px] text-muted-foreground">{t.language.wizard.hint}</span>
              </div>
              <Field id="setup_token" label={t.setup.admin.token} error={adminForm.formState.errors.setup_token?.message}>
                <Input id="setup_token" autoComplete="off" spellCheck={false} {...adminForm.register('setup_token')} />
                <span className="text-[11px] text-muted-foreground">
                  {t.setup.admin.tokenHint} <code className="font-mono">{SETUP_TOKEN_COMMAND}</code>
                </span>
              </Field>
              <Field id="admin_name" label={t.setup.admin.name} error={adminForm.formState.errors.admin_name?.message}>
                <Input id="admin_name" {...adminForm.register('admin_name')} />
              </Field>
              <Field id="admin_email" label={t.auth.emailAddress} error={adminForm.formState.errors.admin_email?.message}>
                <Input id="admin_email" type="email" {...adminForm.register('admin_email')} />
              </Field>
              <Field id="admin_password" label={t.auth.password} error={adminForm.formState.errors.admin_password?.message}>
                <Input id="admin_password" type="password" {...adminForm.register('admin_password')} />
              </Field>
              <Field
                id="admin_password_repeat"
                label={t.setup.admin.repeatPassword}
                error={adminForm.formState.errors.admin_password_repeat?.message}
              >
                <Input
                  id="admin_password_repeat"
                  type="password"
                  autoComplete="new-password"
                  {...adminForm.register('admin_password_repeat')}
                />
              </Field>
              <div className="flex justify-end">
                <Button type="submit" variant="primary">
                  {t.common.continue}
                </Button>
              </div>
            </form>
          )}

          {step === 'org' && (
            <form
              className="flex flex-col gap-4"
              onSubmit={orgForm.handleSubmit((values) => {
                setOrg(values);
                setStep('smtp');
              })}
            >
              <h2 className="text-xl font-semibold">{t.setup.org.title}</h2>
              <Field id="org_name" label={t.setup.org.name} error={orgForm.formState.errors.org_name?.message}>
                <Input id="org_name" {...orgForm.register('org_name')} />
              </Field>
              <Field id="org_slug" label={t.setup.org.slug} error={orgForm.formState.errors.org_slug?.message}>
                <SlugField
                  id="org_slug"
                  inputProps={orgForm.register('org_slug')}
                  readSource={() => orgForm.getValues('org_name') ?? ''}
                  write={(slug) => orgForm.setValue('org_slug', slug, { shouldValidate: true })}
                />
              </Field>
              <div className="flex justify-between">
                <Button type="button" onClick={() => setStep('admin')}>
                  {t.common.back}
                </Button>
                <Button type="submit" variant="primary">
                  {t.common.continue}
                </Button>
              </div>
            </form>
          )}

          {step === 'smtp' && (
            <form
              className="flex flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                // Important 2: the schema requires a host and a valid
                // "from" address, so validating it while SMTP is switched
                // off (its fields are not even rendered) always failed and
                // "Continue" silently did nothing. Skip validation
                // entirely when it is disabled — there is nothing on
                // screen for the user to have gotten wrong.
                if (!smtpEnabled) {
                  setSmtp(null);
                  setStep('mfa');
                  return;
                }
                void smtpForm.handleSubmit((values) => {
                  setSmtp(values);
                  setStep('mfa');
                })(e);
              }}
            >
              <h2 className="text-xl font-semibold">{t.setup.steps.smtp.label}</h2>
              <p className="text-muted-foreground">{t.setup.smtp.intro}</p>
              <label htmlFor="smtp_enabled" className="flex items-center gap-2 text-xs">
                <input
                  id="smtp_enabled"
                  type="checkbox"
                  checked={smtpEnabled}
                  onChange={(e) => setSmtpEnabled(e.target.checked)}
                />
                {t.setup.smtp.configureNow}
              </label>
              {smtpEnabled && (
                <>
                  <Field id="smtp_host" label={t.instance.smtp.host} error={smtpForm.formState.errors.host?.message}>
                    <Input id="smtp_host" {...smtpForm.register('host')} />
                  </Field>
                  <Field id="smtp_port" label={t.instance.smtp.port} error={smtpForm.formState.errors.port?.message}>
                    <Input id="smtp_port" type="number" {...smtpForm.register('port', { valueAsNumber: true })} />
                  </Field>
                  <Field id="smtp_username" label={t.instance.smtp.username}>
                    <Input id="smtp_username" {...smtpForm.register('username')} />
                  </Field>
                  <Field id="smtp_password" label={t.auth.password}>
                    <Input id="smtp_password" type="password" {...smtpForm.register('password')} />
                  </Field>
                  <Field id="smtp_from" label={t.instance.smtp.fromAddress} error={smtpForm.formState.errors.from?.message}>
                    <Input id="smtp_from" type="email" {...smtpForm.register('from')} />
                  </Field>
                  <label className="flex items-center gap-2 text-xs">
                    <input type="checkbox" {...smtpForm.register('start_tls')} defaultChecked />
                    {t.instance.smtp.startTls}
                  </label>
                </>
              )}
              <div className="flex justify-between">
                <Button type="button" onClick={() => setStep('org')}>
                  {t.common.back}
                </Button>
                <Button type="submit" variant="primary">
                  {t.common.continue}
                </Button>
              </div>
            </form>
          )}

          {step === 'mfa' && (
            <div className="flex flex-col gap-4">
              <h2 className="text-xl font-semibold">{t.setup.mfa.title}</h2>
              <p className="text-muted-foreground">{t.setup.mfa.intro}</p>
              {error && <p className="text-xs text-destructive">{error}</p>}
              <div className="flex justify-between">
                <Button type="button" onClick={() => setStep('smtp')} disabled={submitting}>
                  {t.common.back}
                </Button>
                <Button type="button" variant="primary" onClick={finish} disabled={submitting}>
                  {submitting ? t.setup.mfa.creating : t.setup.mfa.create}
                </Button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
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
