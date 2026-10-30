# Security audit run 1 (2026-10-30)

Full audit of `main` at b13f3c6 with the security-audit skill (profile `standard`, 60 agent
invocations). These are the run's final, independently verified outputs:

- `REPORT.md`: run facts, posture, confirmed findings, needs-validation leads, hardening notes, coverage.
- `FINDINGS-DETAIL.md`: full trace and bounded reproduction of every confirmed medium/high finding.
- `NEEDS-VALIDATION.md`: leads with the exact unresolved fact and a plan to settle it.
- `findings.json`: the structured records (schema of the skill's `report-schema.json`).

Paths of the form `agents/<id>/artifacts/...` refer to sandbox evidence kept with the run's working
directory (coverage ledger, prompts, artifacts), which is not part of the repository. Backlog rows
7.7–7.24 track the follow-ups; a later run should start from this run's ledger.
