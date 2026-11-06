# The fixture git repository's content (backlog 2.12a): the scenario turns
# this working tree into a bare repository served by the in-process git
# server. It carries the playbooks (and the greeter role vars_demo.yml uses,
# for the variable sources gate) but deliberately NO requirements.yml: the
# base fixture installs nothing. TestRequirementsEndToEnd (requirements_test.go)
# pushes vendored requirements (a role tarball, a collection as a directory,
# no network) on top of it. See the scenario's repo-sync step.
