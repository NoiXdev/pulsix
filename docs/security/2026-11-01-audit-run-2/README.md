# Security audit run 2 (2026-11-01)

Full audit of `main` at 3099194 (reviewed as 4f9c710, which adds one docs-only commit) with the
security-audit skill (profile `standard`, 70 agent invocations after a raise from 60). It covered
run 1's deferred units, revalidated every run-1 record against its fix, and hunted the changed
source afresh. These are the run's final, independently verified outputs:

- `REPORT.md`: run facts, revalidation of run 1, posture, confirmed findings, needs-validation leads, hardening notes, coverage.
- `FINDINGS-DETAIL.md`: full trace and bounded reproduction of every confirmed medium/high finding.
- `NEEDS-VALIDATION.md`: leads with the exact unresolved fact and a plan to settle it.
- `findings.json`: the structured records (schema of the skill's `report-schema.json`).

Paths of the form `agents/<id>/artifacts/...` refer to sandbox evidence kept with the run's working
directory (coverage ledger, prompts, artifacts), which is not part of the repository. Backlog rows
7.25–7.44 track the follow-ups; a later run should start from this run's ledger.
