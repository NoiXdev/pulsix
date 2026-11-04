import { z } from 'zod';
import type { Messages } from '../i18n/types.ts';
import { MIN_PASSWORD_LENGTH } from './password.ts';

/**
 * Zod schemas mirroring the API's own struct-tag validation, so a form can
 * reject bad input inline before a round trip — see the Go side for each
 * rule's source of truth:
 *   - org/zone slug:  internal/api/org_handlers.go (orgCreateInput.Body.Slug)
 *   - host name:       internal/api/host_handlers.go (hostInputBody.Name)
 *   - tag key:         internal/api/tag_handlers.go (tagInput.Key) /
 *                       internal/hosts (tagKeyPattern)
 *
 * schemas(t) builds them with the messages of a catalogue, so validation
 * speaks the page's language: a component calls it with useT()'s result,
 * inside useMemo keyed on `t`, and hands the schema to zodResolver.
 */
export function schemas(t: Messages) {
  const v = t.validation;

  const slugSchema = z
    .string()
    .min(1, v.required)
    .max(63, v.maxChars(63))
    .regex(/^[a-z0-9]+(-[a-z0-9]+)*$/, v.slugShape);

  /** The first-run wizard's org slug is a stricter subset (no leading/
   * trailing hyphen collapsing needed beyond the same shape, but capped so
   * it always fits legibly in the "pulsix.acme.io/o/…" preview). */
  const setupOrgSlugSchema = z
    .string()
    .min(1, v.required)
    .max(40, v.maxChars(40))
    .regex(/^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$/, v.slugShape);

  const hostNameSchema = z.string().min(1, v.required).max(254, v.maxChars(254));

  const passwordSchema = z.string().min(MIN_PASSWORD_LENGTH, v.minChars(MIN_PASSWORD_LENGTH));

  const emailSchema = z.string().min(3, v.email).email(v.email);

  const smtpSchema = z.object({
    host: z.string().min(1, v.required),
    port: z.number({ error: v.port }).int(v.port).min(1, v.port).max(65535, v.port),
    username: z.string(),
    password: z.string(),
    from: emailSchema,
    start_tls: z.boolean(),
  });

  const hostFormSchema = z.object({
    name: hostNameSchema,
    zone_id: z.string().min(1, v.selectZone),
    address: z.string(),
    managed: z.boolean(),
    monitored: z.boolean(),
  });

  const zoneFormSchema = z.object({
    slug: slugSchema,
    name: z.string().min(1, v.required),
  });

  const orgFormSchema = z.object({
    slug: slugSchema,
    name: z.string().min(1, v.required),
  });

  const inviteFormSchema = z.object({
    email: emailSchema,
    role: z.enum(['admin', 'operator', 'viewer']),
  });

  return {
    slugSchema,
    setupOrgSlugSchema,
    hostNameSchema,
    passwordSchema,
    emailSchema,
    smtpSchema,
    hostFormSchema,
    zoneFormSchema,
    orgFormSchema,
    inviteFormSchema,
  };
}

type Schemas = ReturnType<typeof schemas>;

export type SmtpFormValues = z.infer<Schemas['smtpSchema']>;

/** What the settings form actually sends: the password is optional on the
 * wire so an untouched field keeps the stored one (the form can never show
 * it back). An empty string is the explicit "remove it". */
export type SmtpSaveValues = Omit<SmtpFormValues, 'password'> & { password?: string };

export type HostFormValues = z.infer<Schemas['hostFormSchema']>;
export type ZoneFormValues = z.infer<Schemas['zoneFormSchema']>;
export type OrgFormValues = z.infer<Schemas['orgFormSchema']>;
export type InviteFormValues = z.infer<Schemas['inviteFormSchema']>;
