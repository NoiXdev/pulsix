import { useRef, useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Label } from '../../ui/label.tsx';
import { CodeEditor, type CodeEditorHandle } from '../../ui/code-editor.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useRunAdhocCommand } from '../../api/hooks/useJobs.ts';
import { useT } from '../../i18n/useT.ts';
import { HostChecklist } from './HostChecklist.tsx';

// MAX_ARGV mirrors the server's bound (internal/jobs/adhoc.go's
// maxAdhocArgv, the API's huma maxLength): a client-side cap only, the
// server is the authority.
const MAX_ARGV = 8 << 10;

/**
 * AdhocDialog runs one shell command on explicit managed hosts (spec §7,
 * backlog 2.14): a paginated, searchable multi-select of managed hosts
 * (HostChecklist) plus the command itself. Module is fixed to shell in
 * v0.2b — the field exists server-side so v0.3 can extend it, nothing to
 * choose here yet. On success the dialog navigates to the run view of the
 * first job of the group, the same landing every other run action uses.
 */
export function AdhocDialog({
  org,
  onClose,
  initialHostIds,
}: {
  org: string;
  onClose: () => void;
  /** Hosts selected when the dialog opens, e.g. the host a row menu was
   * opened on. The user can still change the selection. */
  initialHostIds?: string[];
}) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const runAdhoc = useRunAdhocCommand(org);
  const [selected, setSelected] = useState<readonly string[]>(initialHostIds ?? []);
  const [argv, setArgv] = useState('');
  const argvEditor = useRef<CodeEditorHandle>(null);
  const [error, setError] = useState<string | null>(null);

  function toggle(id: string) {
    setSelected((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (selected.length === 0) {
      setError(t.runs.adhoc.chooseHost);
      return;
    }
    if (argv.trim() === '') {
      setError(t.runs.adhoc.commandRequired);
      return;
    }
    try {
      const { jobs } = await runAdhoc.mutateAsync({ host_ids: [...selected], module: 'shell', argv });
      toast.show(t.runs.adhoc.started(selected.length));
      if (jobs && jobs.length > 0) {
        await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.runs.adhoc.failed);
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={t.runs.adhoc.title}>
      <form onSubmit={submit} className="flex max-h-[70vh] flex-col gap-3 overflow-y-auto">
        <p className="text-xs text-muted-foreground">
          {t.runs.adhoc.intro}
        </p>
        <HostChecklist org={org} id="adhoc-search" label={t.runs.adhoc.hosts} selected={selected} onToggle={toggle} />
        <div className="flex flex-col gap-1">
          <Label id="adhoc-argv-label" onClick={() => argvEditor.current?.focus()}>{t.runs.adhoc.command}</Label>
          <CodeEditor
            ref={argvEditor}
            labelledBy="adhoc-argv-label"
            language="text"
            maxLength={MAX_ARGV}
            minHeight="5rem"
            maxHeight="16rem"
            value={argv}
            onChange={setArgv}
            placeholder="uptime"
          />
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={runAdhoc.isPending}>
            {t.runs.adhoc.submit}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
