/**
 * Runs once before the whole Playwright suite (Playwright guarantees this
 * regardless of how many projects/spec files exist): it brings up a
 * throwaway Postgres + mailpit stack, builds the real SPA and embeds it in
 * the manager binary the same way `make build` does, then starts the
 * manager against that database and waits for it to answer.
 *
 * The manager runs from a **prebuilt binary** (`go build -o
 * web/.tmp/pulsix-manager-e2e ./cmd/manager`), not `go run` and not the
 * (nonexistent) production image. `go run` was the first thing tried here
 * and had to be abandoned: it compiles a temporary binary and runs it as a
 * *child* process of the `go run` process itself, and killing the parent
 * (SIGTERM, even SIGKILL) does not reliably kill that child — it can be
 * orphaned and keep listening on PULSIX_LISTEN. The next run's `go run`
 * then fails to bind that port, but this function's own readiness check
 * was polling the *old*, orphaned process the whole time (which answers
 * /healthz just fine — it just has a connection pool for a database
 * `docker compose down -v` already destroyed), so the failure surfaces
 * three steps later as a mystifying 500 from setup/status instead of the
 * actual "address already in use". A single prebuilt binary has no such
 * parent/child ambiguity: the pid this function starts is the pid it
 * stops.
 *
 * Building the frontend first is still required either way:
 * internal/web/embed.go's `//go:embed all:dist` captures whatever is in
 * internal/web/dist at *compile* time, so building the Go binary before
 * the SPA exists would embed the placeholder index.html committed there
 * for `go build`/`go test` to work without a frontend build (see that
 * file's own comment) — never the real app. This function does the same
 * swap `make web-build` does (build the SPA, copy web/dist over
 * internal/web/dist) before the `go build` below, and its returned
 * teardown restores the tracked placeholder afterwards so the working
 * tree is clean again for a commit — see task-15a-report.md's own note on
 * exactly this hazard.
 *
 * A fresh database is required on every run (setup.spec's first assertion
 * — a brand new instance redirects to /setup — depends on nothing being
 * left over from a previous run), so this always tears down and recreates
 * the compose stack rather than reusing a container that might already be
 * running. As a second safety net against exactly the orphaned-process
 * problem above, this also kills anything already bound to
 * PULSIX_LISTEN's port before starting a new manager.
 */
import { execFileSync, spawnSync, spawn, type ChildProcess } from 'node:child_process';
import { chmodSync, closeSync, mkdirSync, openSync, rmSync, cpSync } from 'node:fs';
import path from 'node:path';
import {
  AGENT_GRPC_PORT,
  BASE_URL,
  COMPOSE_FILE,
  DATABASE_URL,
  MAILPIT_HTTP_URL,
  MANAGER_LOG_PATH,
  MANAGER_PORT,
  MASTER_KEY,
} from './env.ts';

const webDir = path.resolve(import.meta.dirname, '..');
const repoRoot = path.resolve(webDir, '..');
const distDir = path.join(repoRoot, 'internal', 'web', 'dist');
const managerBinaryPath = path.join(webDir, '.tmp', 'pulsix-manager-e2e');
const agentBinaryPath = path.join(webDir, '.tmp', 'pulsix-agent-e2e');

function run(cmd: string, args: string[], cwd = repoRoot): void {
  execFileSync(cmd, args, { cwd, stdio: 'inherit' });
}

async function waitFor(label: string, check: () => Promise<boolean>, timeoutMs: number, intervalMs = 1000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastErr: unknown;
  for (;;) {
    try {
      if (await check()) return;
    } catch (err) {
      lastErr = err;
    }
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${timeoutMs}ms waiting for ${label}${lastErr ? `: ${String(lastErr)}` : ''}`);
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}

/** killAnyoneOnManagerPort defends against exactly the orphaned-process
 * hazard this file's own top comment describes: a previous run (a crash,
 * an interrupted `go run` child, a manually-killed test process) may have
 * left something bound to PULSIX_LISTEN's port. `lsof` ships on every
 * platform this suite is expected to run on (macOS locally, the
 * ubuntu-latest GitHub Actions runner in CI); its absence is not treated
 * as fatal; a leftover process the next `go build` binary genuinely
 * cannot bind past is.
 */
function killAnyoneOnManagerPort(): void {
  const result = spawnSync('lsof', ['-tiTCP:' + MANAGER_PORT, '-sTCP:LISTEN'], { encoding: 'utf8' });
  const pids = (result.stdout ?? '')
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean);
  for (const pid of pids) {
    console.log(`[e2e] killing leftover process ${pid} still bound to port ${MANAGER_PORT}`);
    try {
      process.kill(Number(pid), 'SIGKILL');
    } catch {
      // Already gone — fine.
    }
  }
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  mkdirSync(path.dirname(MANAGER_LOG_PATH), { recursive: true });
  killAnyoneOnManagerPort();

  console.log('[e2e] tearing down any stack left over from a previous run …');
  try {
    run('docker', ['compose', '-f', COMPOSE_FILE, 'down', '-v', '--remove-orphans']);
  } catch {
    // Nothing running yet on a first-ever run — fine.
  }

  console.log('[e2e] starting a fresh database and mail catcher …');
  run('docker', ['compose', '-f', COMPOSE_FILE, 'up', '-d', '--wait', '--wait-timeout', '90']);

  await waitFor(
    'the mailpit HTTP API',
    async () => (await fetch(`${MAILPIT_HTTP_URL}/api/v1/messages`)).ok,
    30_000,
    500,
  );

  console.log('[e2e] building the SPA (the manager binary must embed the real build, not the go:embed placeholder) …');
  run('npm', ['run', 'build'], webDir);
  rmSync(distDir, { recursive: true, force: true });
  cpSync(path.join(webDir, 'dist'), distDir, { recursive: true });

  console.log('[e2e] building the manager binary …');
  mkdirSync(path.dirname(managerBinaryPath), { recursive: true });
  // CGO_ENABLED=0, matching the Makefile's own `build` target exactly —
  // no reason for the e2e binary to be built any differently than the one
  // that ships.
  execFileSync('go', ['build', '-o', managerBinaryPath, './cmd/manager'], {
    cwd: repoRoot,
    stdio: 'inherit',
    env: { ...process.env, CGO_ENABLED: '0' },
  });
  chmodSync(managerBinaryPath, 0o755);

  // The agent binary agent.spec enrols with. Built here for the same
  // reason the manager is: the spec must exercise the real binary, and a
  // `go run` child would outlive its parent on teardown.
  console.log('[e2e] building the agent binary …');
  execFileSync('go', ['build', '-o', agentBinaryPath, './cmd/agent'], {
    cwd: repoRoot,
    stdio: 'inherit',
    env: { ...process.env, CGO_ENABLED: '0' },
  });
  chmodSync(agentBinaryPath, 0o755);

  console.log('[e2e] starting the manager …');
  const logFd = openSync(MANAGER_LOG_PATH, 'w');
  const manager: ChildProcess = spawn(managerBinaryPath, [], {
    cwd: repoRoot,
    env: {
      ...process.env,
      PULSIX_DATABASE_URL: DATABASE_URL,
      PULSIX_MASTER_KEY: MASTER_KEY,
      PULSIX_PUBLIC_URL: BASE_URL,
      PULSIX_LISTEN: `:${MANAGER_PORT}`,
      // The gRPC surface agents (and workers) dial. "localhost" is both
      // the bind host and the CA's server-certificate SAN, which is what
      // an enrolling agent verifies against.
      PULSIX_GRPC_LISTEN: `localhost:${AGENT_GRPC_PORT}`,
      PULSIX_GRPC_PUBLIC_ADDR: `localhost:${AGENT_GRPC_PORT}`,
    },
    stdio: ['ignore', logFd, logFd],
  });

  let exited: { code: number | null; signal: NodeJS.Signals | null } | null = null;
  manager.on('exit', (code, signal) => {
    exited = { code, signal };
  });

  await waitFor(
    'the manager to answer /healthz',
    async () => {
      if (exited) {
        throw new Error(`manager exited early (code=${exited.code}, signal=${exited.signal}) — see ${MANAGER_LOG_PATH}`);
      }
      const res = await fetch(`${BASE_URL}/healthz`);
      return res.ok;
    },
    60_000,
    500,
  );
  console.log('[e2e] manager is up at', BASE_URL);

  return async function globalTeardown(): Promise<void> {
    console.log('[e2e] stopping the manager …');
    if (!exited) {
      manager.kill('SIGTERM');
      await new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, 5000);
        manager.once('exit', () => {
          clearTimeout(timer);
          resolve();
        });
      });
      if (!exited) manager.kill('SIGKILL');
    }
    closeSync(logFd);

    console.log('[e2e] restoring internal/web/dist to its committed placeholder …');
    try {
      rmSync(distDir, { recursive: true, force: true });
      run('git', ['checkout', '--', 'internal/web/dist']);
    } catch (err) {
      console.error(
        '[e2e] failed to restore internal/web/dist automatically — run `git checkout -- internal/web/dist` before committing:',
        err,
      );
    }

    console.log('[e2e] tearing down the test stack …');
    try {
      run('docker', ['compose', '-f', COMPOSE_FILE, 'down', '-v', '--remove-orphans']);
    } catch (err) {
      console.error('[e2e] failed to tear down the docker compose stack:', err);
    }
  };
}
