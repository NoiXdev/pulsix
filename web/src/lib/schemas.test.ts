import { describe, expect, it } from 'vitest';
import {
  emailSchema,
  hostFormSchema,
  hostNameSchema,
  inviteFormSchema,
  passwordSchema,
  setupOrgSlugSchema,
  slugSchema,
  smtpSchema,
  zoneFormSchema,
} from './schemas.ts';

describe('slugSchema', () => {
  it('accepts lowercase letters, digits and single hyphens', () => {
    expect(slugSchema.safeParse('fra-1').success).toBe(true);
  });
  it('rejects an empty string', () => {
    expect(slugSchema.safeParse('').success).toBe(false);
  });
  it('rejects uppercase letters', () => {
    expect(slugSchema.safeParse('FRA').success).toBe(false);
  });
  it('rejects a leading hyphen', () => {
    expect(slugSchema.safeParse('-fra').success).toBe(false);
  });
});

describe('setupOrgSlugSchema', () => {
  it('accepts a valid slug', () => {
    expect(setupOrgSlugSchema.safeParse('acme').success).toBe(true);
  });
  it('rejects a slug over 40 characters', () => {
    expect(setupOrgSlugSchema.safeParse('a'.repeat(41)).success).toBe(false);
  });
  it('rejects a trailing hyphen', () => {
    expect(setupOrgSlugSchema.safeParse('acme-').success).toBe(false);
  });
});

describe('hostNameSchema', () => {
  it('accepts a hostname', () => {
    expect(hostNameSchema.safeParse('web-04.fra').success).toBe(true);
  });
  it('rejects an empty string', () => {
    expect(hostNameSchema.safeParse('').success).toBe(false);
  });
});

describe('passwordSchema', () => {
  it('requires at least 12 characters', () => {
    expect(passwordSchema.safeParse('short').success).toBe(false);
    expect(passwordSchema.safeParse('correct horse battery').success).toBe(true);
  });
});

describe('emailSchema', () => {
  it('rejects a malformed address', () => {
    expect(emailSchema.safeParse('not-an-email').success).toBe(false);
  });
  it('accepts a valid address', () => {
    expect(emailSchema.safeParse('tim@acme.io').success).toBe(true);
  });
});

describe('smtpSchema', () => {
  const valid = { host: 'smtp.acme.io', port: 587, username: '', password: '', from: 'noreply@acme.io', start_tls: true };

  it('accepts a fully filled-in config', () => {
    expect(smtpSchema.safeParse(valid).success).toBe(true);
  });

  it('rejects the all-empty defaults the SMTP step starts from — this is exactly why Important 2 had to skip validation while SMTP is off', () => {
    const result = smtpSchema.safeParse({ host: '', port: 587, username: '', password: '', from: '', start_tls: true });
    expect(result.success).toBe(false);
  });

  it('rejects a port out of range', () => {
    expect(smtpSchema.safeParse({ ...valid, port: 70000 }).success).toBe(false);
  });
});

describe('hostFormSchema', () => {
  it('requires a zone to be selected', () => {
    const result = hostFormSchema.safeParse({ name: 'web-04', zone_id: '', address: '', managed: false, monitored: false });
    expect(result.success).toBe(false);
  });
  it('accepts a minimal valid host', () => {
    const result = hostFormSchema.safeParse({ name: 'web-04', zone_id: 'z1', address: '', managed: false, monitored: false });
    expect(result.success).toBe(true);
  });
});

describe('zoneFormSchema', () => {
  it('accepts a valid zone', () => {
    expect(zoneFormSchema.safeParse({ slug: 'fra', name: 'Frankfurt' }).success).toBe(true);
  });
  it('rejects an invalid slug', () => {
    expect(zoneFormSchema.safeParse({ slug: 'FRA', name: 'Frankfurt' }).success).toBe(false);
  });
});

describe('inviteFormSchema', () => {
  it('rejects the owner role — it cannot be granted by invitation', () => {
    const result = inviteFormSchema.safeParse({ email: 'tim@acme.io', role: 'owner' });
    expect(result.success).toBe(false);
  });
  it('accepts operator', () => {
    expect(inviteFormSchema.safeParse({ email: 'tim@acme.io', role: 'operator' }).success).toBe(true);
  });
});
