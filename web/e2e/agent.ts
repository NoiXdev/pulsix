/**
 * The agent the UI gate enrols: the real cmd/agent binary, built by
 * global-setup next to the manager's and started by the spec once the GUI
 * has printed an enrollment token.
 *
 * It runs as a host process rather than a compose service on purpose. The
 * manager under test is a host process too (see global-setup), the token
 * only exists once the spec has clicked through the dialog — so a service
 * declared in the compose file could not be part of `up` anyway — and a
 * container would need host-gateway networking that differs between a
 * developer's macOS and the Linux CI runner. What the gate is about is the
 * agent binary enrolling against the manager's real gRPC surface, and that
 * is exactly what this exercises.
 */
import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync, mkdtempSync, openSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { AGENT_GRPC_PORT, AGENT_BINARY_PATH } from './env.ts';

/** agentBinaryAvailable reports whether global-setup built the agent. The
 * spec skips with a visible reason when it did not, rather than failing on
 * a spawn error that says nothing about why. */
export function agentBinaryAvailable(): boolean {
  return existsSync(AGENT_BINARY_PATH);
}

export interface RunningAgent {
  /** The data directory holding the agent's identity — the spec can
   * assert the enrollment persisted something. */
  dataDir: string;
  stop: () => Promise<void>;
}

/** startAgent runs one agent with the token the GUI just printed. The
 * caller stops it in a finally block: a leaked agent keeps heartbeating
 * into the shared manager for the rest of the suite. */
export function startAgent(token: string, name: string): RunningAgent {
  const dataDir = mkdtempSync(path.join(tmpdir(), 'pulsix-agent-e2e-'));
  const logFd = openSync(path.join(dataDir, 'agent.log'), 'w');
  const child: ChildProcess = spawn(AGENT_BINARY_PATH, [], {
    env: {
      ...process.env,
      PULSIX_MANAGER_ADDR: `localhost:${AGENT_GRPC_PORT}`,
      PULSIX_MANAGER_SERVER_NAME: 'localhost',
      PULSIX_ENROLL_TOKEN: token,
      PULSIX_AGENT_NAME: name,
      PULSIX_DATA_DIR: dataDir,
    },
    stdio: ['ignore', logFd, logFd],
  });
  let exited = false;
  child.on('exit', () => {
    exited = true;
  });
  return {
    dataDir,
    stop: async () => {
      if (exited) return;
      child.kill('SIGTERM');
      await new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, 5000);
        child.once('exit', () => {
          clearTimeout(timer);
          resolve();
        });
      });
      if (!exited) child.kill('SIGKILL');
    },
  };
}
