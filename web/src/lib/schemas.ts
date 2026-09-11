import { z } from 'zod';

/**
 * Zod schemas mirroring the API's own struct-tag validation, so a form can
 * reject bad input inline before a round trip — see the Go side for each
 * rule's source of truth:
 *   - org/zone slug:  internal/api/org_handlers.go (orgCreateInput.Body.Slug)
 *   - host name:       internal/api/host_handlers.go (hostInputBody.Name)
 *   - tag key:         internal/api/tag_handlers.go (tagInput.Key) /
 *                       internal/hosts (tagKeyPattern)
 */

export const slugSchema = z
  .string()
  .min(1, 'required')
  .max(63, 'must be 63 characters or fewer')
  .regex(/^[a-z0-9]+(-[a-z0-9]+)*$/, 'lowercase letters, digits and single hyphens only');

/** The first-run wizard's org slug is a stricter subset (no leading/
 * trailing hyphen collapsing needed beyond the same shape, but capped so
 * it always fits legibly in the "pulsix.acme.io/o/…" preview). */
export const setupOrgSlugSchema = z
  .string()
  .min(1, 'required')
  .max(40, 'must be 40 characters or fewer')
  .regex(/^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$/, 'lowercase letters, digits and single hyphens only');

export const hostNameSchema = z.string().min(1, 'required').max(254, 'must be 254 characters or fewer');

export const tagKeySchema = z
  .string()
  .min(1, 'required')
  .max(64, 'must be 64 characters or fewer')
  .regex(/^[a-z0-9][a-z0-9_.-]*$/, 'lowercase letters, digits, "_", "." and "-" only');

export const passwordSchema = z.string().min(12, 'at least 12 characters');

export const emailSchema = z.string().min(3).email('enter a valid email address');

export const setupSchema = z.object({
  admin_email: emailSchema,
  admin_password: passwordSchema,
  admin_name: z.string().min(1, 'required'),
  org_slug: setupOrgSlugSchema,
  org_name: z.string().min(1, 'required'),
});
export type SetupFormValues = z.infer<typeof setupSchema>;

export const smtpSchema = z.object({
  host: z.string().min(1, 'required'),
  port: z.number().int().min(1).max(65535),
  username: z.string(),
  password: z.string(),
  from: emailSchema,
  start_tls: z.boolean(),
});
export type SmtpFormValues = z.infer<typeof smtpSchema>;

export const hostFormSchema = z.object({
  name: hostNameSchema,
  zone_id: z.string().min(1, 'select a zone'),
  address: z.string(),
  managed: z.boolean(),
  monitored: z.boolean(),
});
export type HostFormValues = z.infer<typeof hostFormSchema>;

export const zoneFormSchema = z.object({
  slug: slugSchema,
  name: z.string().min(1, 'required'),
});
export type ZoneFormValues = z.infer<typeof zoneFormSchema>;

export const inviteFormSchema = z.object({
  email: emailSchema,
  role: z.enum(['admin', 'operator', 'viewer']),
});
export type InviteFormValues = z.infer<typeof inviteFormSchema>;
