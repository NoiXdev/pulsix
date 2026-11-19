import { useId, useMemo, type ReactNode } from 'react';
import { useT } from '../../i18n/useT.ts';
import { playbookVariables, usesVarsFiles, type VariableSource } from './variables.ts';
import type { YAMLProblem } from './validate.ts';

/** SidePanel is one titled box of the playbook page's side column, a region
 * named by its title. */
export function SidePanel({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="flex flex-col gap-2 rounded-[10px] border border-border bg-card px-4 py-3">
      <div className="flex items-center justify-between gap-2">
        <h2 id={id} className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
          {title}
        </h2>
        {action}
      </div>
      {children}
    </section>
  );
}

/** PlaybookCheckPanel says whether the draft passes the preview (the
 * server's own messages, with the line where the preview knows it) and,
 * after a refused save, what the server said. `idle` stands in for the
 * verdict while there is nothing to judge yet (an empty new playbook). */
export function PlaybookCheckPanel({ problem, serverError, idle }: { problem: YAMLProblem | null; serverError: string | null; idle?: string }) {
  const t = useT();
  const c = t.playbooks.page.check;
  return (
    <SidePanel title={c.title}>
      {idle !== undefined ? (
        <p className="text-xs text-muted-foreground">{idle}</p>
      ) : problem ? (
        <p role="alert" className="text-xs text-destructive">
          {problem.line !== undefined && <span className="font-mono">{c.line(problem.line)}: </span>}
          <span>{problem.message}</span>
        </p>
      ) : (
        <p role="status" className="text-xs" style={{ color: 'var(--px-ok)' }}>
          {t.playbooks.editor.ready}
        </p>
      )}
      {serverError && (
        <div role="alert" className="flex flex-col gap-0.5 text-xs text-destructive">
          <span>{c.server}</span>
          <span className="font-mono break-words">{serverError}</span>
        </div>
      )}
      <p className="text-[11px] text-muted-foreground">{c.hint}</p>
    </SidePanel>
  );
}

const SOURCE_COLOR: Record<VariableSource, string> = {
  vars: 'var(--px-ok)',
  task: 'var(--px-accent)',
  external: 'var(--px-warn)',
};

/** PlaybookVariablesPanel lists the names the YAML's expressions use and
 * where each gets its value (variables.ts) — a hint, and labelled so. */
export function PlaybookVariablesPanel({ yaml }: { yaml: string }) {
  const t = useT();
  const v = t.playbooks.page.variables;
  const names = useMemo(() => playbookVariables(yaml), [yaml]);
  const varsFiles = useMemo(() => usesVarsFiles(yaml), [yaml]);
  return (
    <SidePanel title={v.title}>
      <p className="text-[11px] text-muted-foreground">{v.hint}</p>
      {varsFiles && <p className="text-[11px] text-muted-foreground">{v.varsFiles}</p>}
      {names.length === 0 ? (
        <p className="text-xs text-muted-foreground">{v.none}</p>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {names.map((n) => (
            <li key={n.name} className="flex min-w-0 flex-col text-xs">
              <span className="font-mono break-all">{n.name}</span>
              <span className="text-[11px]" style={{ color: SOURCE_COLOR[n.source] }}>
                {v[n.source]}
              </span>
            </li>
          ))}
        </ul>
      )}
    </SidePanel>
  );
}
