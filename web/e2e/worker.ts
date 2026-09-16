/**
 * workerServiceAvailable probes the compose test stack for the pulsix
 * worker service: the Ansible E2E's whole scenario rides on a worker
 * claiming the jobs the UI enqueues, and that worker is Task 17's image —
 * until it lands there is no stack to test against. The spec asks this
 * helper and skips with a visible reason when it answers "no".
 */
import { execFileSync } from 'node:child_process';
import { COMPOSE_FILE } from './env.ts';

export function workerServiceAvailable(): boolean {
  try {
    // Running services only: a service defined in the compose file but
    // not up (the pre-Task-17 state) must read as absent.
    const out = execFileSync('docker', ['compose', '-f', COMPOSE_FILE, 'ps', '--status=running', '--format', 'json', 'worker'], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    return out.trim().length > 0;
  } catch {
    return false;
  }
}
