import { describe, expect, it } from 'vitest';
import type { Credential } from '../../api/types.ts';
import { filterCredentials, validateCredentialsSearch } from './credentialsSearch.ts';

const cred = (name: string, extra: Partial<Credential> = {}): Credential =>
  ({ id: name, name, kind: 'ssh_key', username: 'ansible', expired: false, expires_soon: false, used_by: { hosts: 1, bastions: 0, repos: 0, templates: 0 }, ...extra }) as Credential;

describe('credentialsSearch', () => {
  it('drops unknown kinds and keeps the rest', () => {
    expect(validateCredentialsSearch({ kind: 'nope', q: 'a', expired: 'true', page: '3' })).toMatchObject({ kind: undefined, q: 'a', expired: true, page: 3 });
  });

  const rows = [
    cred('deploy'),
    cred('old', { expired: true, expires_soon: true }),
    cred('soon', { expires_soon: true, kind: 'git_token', username: undefined }),
    cred('idle', { used_by: { hosts: 0, bastions: 0, repos: 0, templates: 0 } }),
  ];
  const names = (f: Parameters<typeof filterCredentials>[1]) => filterCredentials(rows, f).map((c) => c.name);

  it('filters by search, kind, expiry and use', () => {
    expect(names({})).toEqual(['deploy', 'old', 'soon', 'idle']);
    expect(names({ q: 'ID' })).toEqual(['idle']);
    expect(names({ kind: 'git_token' })).toEqual(['soon']);
    expect(names({ expired: true })).toEqual(['old']);
    expect(names({ expiring: true })).toEqual(['soon']);
    expect(names({ unused: true })).toEqual(['idle']);
  });
});
