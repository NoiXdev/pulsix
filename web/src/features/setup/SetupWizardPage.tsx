import { useEffect, useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { PulsixMark } from '../../ui/PulsixMark.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { emailSchema, passwordSchema, setupOrgSlugSchema, smtpSchema, type SmtpFormValues } from '../../lib/schemas.ts';
import { completeSetup, refetchSetupStatus, useSetupStatus } from '../../api/hooks/useSetupStatus.ts';
import { useRefetchMe } from '../../api/hooks/useMe.ts';
import { useResetSession } from '../../lib/session.ts';
import { useQueryClient } from '@tanstack/react-query';

type StepKey = 'admin' | 'org' | 'smtp' | 'mfa';
const STEPS: Array<{ key: StepKey; label: string; sub: string }> = [
  { key: 'admin', label: 'Instance administrator', sub: 'email and password' },
  { key: 'org', label: 'First organization', sub: 'name and URL slug' },
  { key: 'smtp', label: 'Outgoing mail', sub: 'optional, can be set later' },
  { key: 'mfa', label: 'Second factor', sub: 'authenticator app or passkey' },
];

const adminSchema = z.object({
  admin_email: emailSchema,
  admin_password: passwordSchema,
  admin_name: z.string().min(1, 'required'),
});
type AdminValues = z.infer<typeof adminSchema>;

const orgSchema = z.object({
  org_name: z.string().min(1, 'required'),
  org_slug: setupOrgSlugSchema,
});
type OrgValues = z.infer<typeof orgSchema>;

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

  const stepIndex = STEPS.findIndex((s) => s.key === step);

  const adminForm = useForm<AdminValues>({ resolver: zodResolver(adminSchema), defaultValues: admin ?? undefined });
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
        admin_email: admin.admin_email,
        admin_password: admin.admin_password,
        admin_name: admin.admin_name,
        org_slug: org.org_slug,
        org_name: org.org_name,
        smtp: smtpEnabled && smtp ? smtp : undefined,
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
      setError(err instanceof Error ? err.message : 'failed to complete setup');
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
          <h1 className="text-xl font-semibold">Set up this installation</h1>
          <p className="text-muted-foreground">
            Runs once. Afterwards this page is gone and everything happens inside the organization.
          </p>
        </div>
        <div className="flex flex-col gap-4">
          {STEPS.map((s, i) => (
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
            Step {stepIndex + 1} of {STEPS.length}
          </span>

          {step === 'admin' && (
            <form
              className="flex flex-col gap-4"
              onSubmit={adminForm.handleSubmit((values) => {
                setAdmin(values);
                setStep('org');
              })}
            >
              <h2 className="text-xl font-semibold">Create the instance administrator</h2>
              <Field id="admin_name" label="Name" error={adminForm.formState.errors.admin_name?.message}>
                <Input id="admin_name" {...adminForm.register('admin_name')} />
              </Field>
              <Field id="admin_email" label="Email address" error={adminForm.formState.errors.admin_email?.message}>
                <Input id="admin_email" type="email" {...adminForm.register('admin_email')} />
              </Field>
              <Field id="admin_password" label="Password" error={adminForm.formState.errors.admin_password?.message}>
                <Input id="admin_password" type="password" {...adminForm.register('admin_password')} />
              </Field>
              <div className="flex justify-end">
                <Button type="submit" variant="primary">
                  Continue
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
              <h2 className="text-xl font-semibold">Create the first organization</h2>
              <Field id="org_name" label="Organization name" error={orgForm.formState.errors.org_name?.message}>
                <Input id="org_name" {...orgForm.register('org_name')} />
              </Field>
              <Field id="org_slug" label="URL slug" error={orgForm.formState.errors.org_slug?.message}>
                <Input id="org_slug" {...orgForm.register('org_slug')} />
              </Field>
              <div className="flex justify-between">
                <Button type="button" onClick={() => setStep('admin')}>
                  Back
                </Button>
                <Button type="submit" variant="primary">
                  Continue
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
              <h2 className="text-xl font-semibold">Outgoing mail</h2>
              <p className="text-muted-foreground">
                Optional, can be set later. Without SMTP configured, invitation e-mails cannot be sent.
              </p>
              <label htmlFor="smtp_enabled" className="flex items-center gap-2 text-xs">
                <input
                  id="smtp_enabled"
                  type="checkbox"
                  checked={smtpEnabled}
                  onChange={(e) => setSmtpEnabled(e.target.checked)}
                />
                Configure SMTP now
              </label>
              {smtpEnabled && (
                <>
                  <Field id="smtp_host" label="Host" error={smtpForm.formState.errors.host?.message}>
                    <Input id="smtp_host" {...smtpForm.register('host')} />
                  </Field>
                  <Field id="smtp_port" label="Port" error={smtpForm.formState.errors.port?.message}>
                    <Input id="smtp_port" type="number" {...smtpForm.register('port', { valueAsNumber: true })} />
                  </Field>
                  <Field id="smtp_username" label="Username">
                    <Input id="smtp_username" {...smtpForm.register('username')} />
                  </Field>
                  <Field id="smtp_password" label="Password">
                    <Input id="smtp_password" type="password" {...smtpForm.register('password')} />
                  </Field>
                  <Field id="smtp_from" label="From address" error={smtpForm.formState.errors.from?.message}>
                    <Input id="smtp_from" type="email" {...smtpForm.register('from')} />
                  </Field>
                  <label className="flex items-center gap-2 text-xs">
                    <input type="checkbox" {...smtpForm.register('start_tls')} defaultChecked />
                    Use STARTTLS
                  </label>
                </>
              )}
              <div className="flex justify-between">
                <Button type="button" onClick={() => setStep('org')}>
                  Back
                </Button>
                <Button type="submit" variant="primary">
                  Continue
                </Button>
              </div>
            </form>
          )}

          {step === 'mfa' && (
            <div className="flex flex-col gap-4">
              <h2 className="text-xl font-semibold">Enrol a second factor now</h2>
              <p className="text-muted-foreground">
                The instance administrator account is created next. You will be asked to add an authenticator app or
                a passkey right away — every account needs a second factor before it can do anything else.
              </p>
              {error && <p className="text-xs text-destructive">{error}</p>}
              <div className="flex justify-between">
                <Button type="button" onClick={() => setStep('smtp')} disabled={submitting}>
                  Back
                </Button>
                <Button type="button" variant="primary" onClick={finish} disabled={submitting}>
                  {submitting ? 'Creating…' : 'Create administrator & continue'}
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
