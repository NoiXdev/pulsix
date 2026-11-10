# Security audit run 4 (2026-11-09)

Scoped audit of `main` at 52f27625 with the security-audit skill (profile `standard`, 52 agent
invocations of a budget raised from 48 to 55). It covered run 3's deferred units, revalidated every
run-3 record against its fix, and hunted every source change since run 3 (`7ae1808..52f27625`:
the run-3 follow-up fixes, account management and password reset, the setup token, the
requirements cache with ansible-galaxy under the play uid, playbook variable sources and template
variable sync, certificate adoption after a restore, the authenticated backup header, and the
public repository's CI and release workflows). It is a partial pass, not a whole-repository
audit. These are the run's final, independently verified outputs:

- `REPORT.md`: run facts, revalidation of run 3, posture, confirmed findings, needs-validation leads, hardening notes, coverage.
- `FINDINGS-DETAIL.md`: full trace and bounded reproduction of every confirmed medium/high finding (one in this run: the worker's git fetch from a stale origin).
- `NEEDS-VALIDATION.md`: leads with the exact unresolved fact and a plan to settle it.
- `findings.json`: the structured records (schema of the skill's `report-schema.json`).

Paths of the form `agents/<id>/artifacts/...` refer to sandbox evidence kept with the run's working
directory (coverage ledger, prompts, artifacts), which is not part of the repository. Backlog rows
7.101–7.114 track the follow-ups; a later run should start from this run's ledger.
