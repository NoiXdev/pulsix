import { describe, expect, it } from 'vitest';
import { snippets } from './snippets.ts';

const base = { url: 'https://pulsix.example.test/api/v1/hooks/jobs/7d2f', signature_header: 'X-Pulsix-Signature-256', timestamp_header: '' };

describe('trigger snippets', () => {
  it('signs the body in the curl snippet for HMAC presets', () => {
    const s = snippets({ ...base, preset: 'pulsix', auth: 'hmac' });
    expect(s.curl).toContain('openssl dgst -sha256 -hmac "$SECRET"');
    expect(s.curl).toContain('X-Pulsix-Signature-256: sha256=$sig');
    expect(s.curl).toContain(base.url);
    expect(s.curl).not.toContain('?token=');
  });

  it('signs "<timestamp>.<body>" when a timestamp header is set', () => {
    const s = snippets({ ...base, preset: 'pulsix', auth: 'hmac', timestamp_header: 'X-Pulsix-Timestamp' });
    expect(s.curl).toContain('ts=$(date +%s)');
    expect(s.curl).toContain(`printf '%s.%s' "$ts" "$body"`);
    expect(s.curl).toContain('X-Pulsix-Timestamp: $ts');
  });

  it('sends the token in a header for token presets and names the provider settings', () => {
    const s = snippets({ ...base, preset: 'gitlab', auth: 'token', signature_header: 'X-Gitlab-Token' });
    expect(s.curl).toContain('X-Gitlab-Token: $SECRET');
    expect(s.gitlab).toContain('Secret token');
    expect(snippets({ ...base, preset: 'github', auth: 'hmac', signature_header: 'X-Hub-Signature-256' }).github).toContain('application/json');
  });
});
