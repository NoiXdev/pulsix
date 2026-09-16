/**
 * The Ansible E2E's compose-stack contract (backlog 2.12a): the addresses
 * the spec drives through the UI are the docker-compose service names the
 * worker resolves inside the test network — the fixtures live where the
 * worker can reach them, and the manager (a host binary in global-setup)
 * only renders the routes into jobs. Every name has an env override so the
 * compose overlay that wires the services stays the single source of
 * truth.
 *
 * Nothing here matters unless the pulsix worker service is actually
 * running (web/e2e/worker.ts probes for it); the spec skips otherwise,
 * because the worker image is Task 17's deliverable.
 */
function fromEnv(name: string, fallback: string): string {
  return process.env[`PULSIX_E2E_${name}`] ?? fallback;
}

/** The managed host as the worker sees it (the compose service name). */
export const SSH_FIXTURE_HOST = fromEnv('SSH_HOST', 'ssh-host');
/** The jump host as the worker sees it. */
export const BASTION_FIXTURE_HOST = fromEnv('BASTION', 'bastion');
/** The fixture git repository's fetch URL (SSH form; the worker clones it
 * with the deploy key credential). */
export const GIT_FIXTURE_URL = fromEnv('GIT_URL', 'ssh://git@git-server/fixture.git');
/** The playbooks the fixture repo carries (the repo sync's content). */
export const FIXTURE_REPO_BRANCH = fromEnv('GIT_BRANCH', 'main');
