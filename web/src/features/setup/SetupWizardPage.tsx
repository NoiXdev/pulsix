import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { PulsixMark } from '../../ui/PulsixMark.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { emailSchema, passwordSchema, setupOrgSlugSchema, smtpSchema, type SmtpFormValues } from '../../lib/schemas.ts';
import { completeSetup } from '../../api/hooks/useSetupStatus.ts';
import { useInvalidateMe } from '../../api/hooks/useMe.ts';

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
  const invalidateMe = useInvalidateMe();
  const [step, setStep] = useState<StepKey>('admin');
  const [admin, setAdmin] = useState<AdminValues | null>(null);
  const [org, setOrg] = useState<OrgValues | null>(null);
  const [smtp, setSmtp] = useState<SmtpFormValues | null>(null);
  const [smtpEnabled, setSmtpEnabled] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
      await invalidateMe();
      await navigate({ to: '/account/security' });
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
            pulsi<span className="text-accent">x</span>
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
          <span className="text-[11px] font-semibold tracking-widest text-accent uppercase">
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
              <Field label="Name" error={adminForm.formState.errors.admin_name?.message}>
                <Input {...adminForm.register('admin_name')} />
              </Field>
              <Field label="Email address" error={adminForm.formState.errors.admin_email?.message}>
                <Input type="email" {...adminForm.register('admin_email')} />
              </Field>
              <Field label="Password" error={adminForm.formState.errors.admin_password?.message}>
                <Input type="password" {...adminForm.register('admin_password')} />
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
              <Field label="Organization name" error={orgForm.formState.errors.org_name?.message}>
                <Input {...orgForm.register('org_name')} />
              </Field>
              <Field label="URL slug" error={orgForm.formState.errors.org_slug?.message}>
                <Input {...orgForm.register('org_slug')} />
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
              onSubmit={smtpForm.handleSubmit((values) => {
                setSmtp(values);
                setStep('mfa');
              })}
            >
              <h2 className="text-xl font-semibold">Outgoing mail</h2>
              <p className="text-muted-foreground">Optional — invitations show a copyable link if this is skipped.</p>
              <label className="flex items-center gap-2 text-xs">
                <input
                  type="checkbox"
                  checked={smtpEnabled}
                  onChange={(e) => setSmtpEnabled(e.target.checked)}
                />
                Configure SMTP now
              </label>
              {smtpEnabled && (
                <>
                  <Field label="Host" error={smtpForm.formState.errors.host?.message}>
                    <Input {...smtpForm.register('host')} />
                  </Field>
                  <Field label="Port" error={smtpForm.formState.errors.port?.message}>
                    <Input type="number" {...smtpForm.register('port', { valueAsNumber: true })} />
                  </Field>
                  <Field label="Username">
                    <Input {...smtpForm.register('username')} />
                  </Field>
                  <Field label="Password">
                    <Input type="password" {...smtpForm.register('password')} />
                  </Field>
                  <Field label="From address" error={smtpForm.formState.errors.from?.message}>
                    <Input type="email" {...smtpForm.register('from')} />
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
                The instance administrator account is created next. You will land on the security page to add an
                authenticator app or a passkey immediately — this account has no second factor until you do.
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

function Field({ label, error, children }: { label: string; error?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label>{label}</Label>
      {children}
      {error && <span className="text-[11px] text-destructive">{error}</span>}
    </div>
  );
}
