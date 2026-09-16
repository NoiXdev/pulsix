# The fixture git repository's content (backlog 2.12a): the scenario turns
# this working tree into a bare repository served by the in-process git
# server. It carries the two playbooks but deliberately NO requirements.yml
# — the galaxy install needs network access, requirements handling is
# optional in the worker, and Task 11's sync tests cover the install path
# at the unit level. See the scenario's repo-sync step.
