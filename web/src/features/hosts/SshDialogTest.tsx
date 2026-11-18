import { Link } from '@tanstack/react-router';
import { Activity } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { useHostTest } from '../../api/hooks/useHostOps.ts';
import { useT } from '../../i18n/useT.ts';
import { ConnectionTestResult } from './ConnectionTestCard.tsx';
import type { DialogConnectionTest } from './useDialogConnectionTest.ts';

/**
 * SshDialogTest is the dialog's connection test row. The test checks what
 * is saved (a connection test job resolves the host's route when a worker
 * claims it), so with unsaved changes the button saves first: "Save and
 * test". The result shows in place once the run has ended.
 */
export function SshDialogTest({
  org, hostId, test, dirty, disabled, onRun,
}: {
  org: string;
  hostId: string;
  test: DialogConnectionTest;
  dirty: boolean;
  disabled: boolean;
  onRun: () => void;
}) {
  const t = useT();
  const s = t.hosts.sshDialog;
  const latest = useHostTest(org, hostId);
  const latestTest = latest.data?.test;
  const result = test.started?.read && latestTest && test.isThisRun(latestTest.at) ? latestTest : undefined;
  return (
    <div className="flex flex-col gap-2 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-3">
        <Button type="button" disabled={disabled || test.pending} onClick={onRun} aria-describedby="ssh-test-help">
          <Activity size={13} /> {dirty ? s.saveAndTest : s.test}
        </Button>
        <span id="ssh-test-help" className="text-[11px] text-muted-foreground">
          {dirty ? s.saveAndTestHelp : s.testHelp}
        </span>
      </div>
      {test.error && (
        <p role="alert" className="text-xs text-destructive">
          {test.error}
        </p>
      )}
      {test.started && (
        <div className="flex flex-col gap-2" aria-live="polite">
          {!test.started.read && <p className="text-xs text-muted-foreground">{test.queued ? s.queued : s.testing}</p>}
          {result && <ConnectionTestResult org={org} hostId={hostId} test={result} canWrite />}
          {test.started.read && !result && test.status && <p className="text-xs text-muted-foreground">{s.testEnded(test.status)}</p>}
          <Link to="/o/$org/runs/$id" params={{ org, id: test.started.jobId }} className="text-xs text-primary hover:underline">
            {s.openRun}
          </Link>
        </div>
      )}
    </div>
  );
}
