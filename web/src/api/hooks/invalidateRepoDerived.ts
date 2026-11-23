import type { QueryClient } from '@tanstack/react-query';

/**
 * invalidateRepoDerived refreshes everything a repository sync produces or
 * changes: the repositories, the playbooks it found, the template variable
 * sources it recorded (and applied, which moves a template's revision) and
 * the runs list. Starting a sync, seeing one end and a dropped watch all
 * call it, so the three places cannot drift apart.
 */
export function invalidateRepoDerived(qc: QueryClient, org: string): void {
  void qc.invalidateQueries({ queryKey: ['repos', org] });
  void qc.invalidateQueries({ queryKey: ['playbooks', org] });
  void qc.invalidateQueries({ queryKey: ['templates', org] });
  void qc.invalidateQueries({ queryKey: ['jobs', org] });
}
