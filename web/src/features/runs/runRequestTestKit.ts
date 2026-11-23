/** Shared fixtures for the run request pages' tests. */

export const RQ = '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b';

export function inMinutes(n: number): string {
  return new Date(Date.now() + n * 60_000).toISOString();
}

export function orgAs(role: string) {
  return { org: { id: 'o1', slug: 'acme', name: 'Acme', role, created_at: '2026-01-01T00:00:00Z' } };
}

export const MEMBERS = {
  members: [{ user_id: 'u1', display_name: 'Tim R.', email: 'tim@acme.io', role: 'owner', created_at: '2026-01-01T00:00:00Z', disabled_at: null }],
};

export function request(over: Record<string, unknown> = {}) {
  return {
    id: RQ,
    kind: 'run',
    template_id: 'tpl1',
    reason: 'Roll out the new nginx config',
    status: 'pending',
    requested_by_user_id: 'u1',
    requested_by_token_id: 'tok2',
    requested_by_token_name: 'claude-ops',
    token_state: 'active',
    created_at: '2026-11-16T09:00:00Z',
    expires_at: inMinutes(30),
    ...over,
  };
}

export function detail(over: { request?: Record<string, unknown>; picture?: Record<string, unknown>; fingerprint?: string } = {}) {
  return {
    request: request(over.request),
    fingerprint: over.fingerprint ?? 'fp-1',
    approval_path: `/o/acme/runs/requests/${RQ}`,
    picture: {
      template_id: 'tpl1',
      template_name: 'deploy-nginx',
      template_revision: 7,
      limit: 'web',
      playbook: { id: 'pb1', source: 'repo', path: 'site/nginx.yml' },
      repo: { id: 'r1', name: 'infra', url: 'https://git.example.com/infra.git', branch: 'main', subpath: 'ansible' },
      targets: [
        { id: 'h1', name: 'web-01', address: '10.0.0.11', ssh_user: 'deploy', ssh_port: 22, jump_host: 'bastion', credential: 'deploy-key' },
        { id: 'h2', name: 'web-02', address: '10.0.0.12', ssh_user: 'deploy', ssh_port: 2222 },
      ],
      ssh_credential: 'deploy-key',
      vault_identities: [{ label: 'prod', credential: 'vault-prod' }],
      extra_vars: { nginx_workers: 4, api_password: { $secret: 'set' } },
      vars_digest: 'd1',
      ...over.picture,
    },
  };
}
