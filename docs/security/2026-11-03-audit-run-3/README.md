# Security audit run 3 (2026-11-03)

Scoped audit of `main` at 7ae1808 with the security-audit skill (profile `standard`, 48 agent
invocations after a raise from 45). It covered run 2's deferred units, revalidated every run-2
record against its fix, and hunted every source change since run 2 (`4f9c7103..7ae1808`: the
run-2 follow-up fixes, GUI languages, backup and restore, worker certificate reload). It is a
partial pass, not a whole-repository audit. These are the run's final, independently verified
outputs:

- `REPORT.md`: run facts, revalidation of run 2, posture, confirmed findings, needs-validation leads, hardening notes, coverage.
- `FINDINGS-DETAIL.md`: full trace and bounded reproduction of every confirmed medium/high finding (none in this run; the five confirmed records are low and summarised in `REPORT.md`).
- `NEEDS-VALIDATION.md`: leads with the exact unresolved fact and a plan to settle it.
- `findings.json`: the structured records (schema of the skill's `report-schema.json`).

Paths of the form `agents/<id>/artifacts/...` refer to sandbox evidence kept with the run's working
directory (coverage ledger, prompts, artifacts), which is not part of the repository. Backlog rows
7.49–7.62 track the follow-ups; a later run should start from this run's ledger.
