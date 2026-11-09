// Package varsource reads the variables a playbook declares and maps them
// onto schema fields (spec 7.75 section 6): role argument specs and the
// playbook's sidecar, vars_prompt entries, and inline "# pulsix:" markers on
// play variables. Every problem it meets becomes a Note with file and line;
// nothing it reads stops a sync.
//
// The package is pure: it reads only the bytes and YAML nodes it is handed
// (and, for whole playbooks, the fs.FS it is given), with no database, no
// network and no other I/O. The worker runs it during a repo sync, the
// manager for inline playbooks.
//
// A repo sync may also hand it the roles installed from requirements.yml
// as a second fs.FS of their own (ParseRepoWithRoles): a role name, plain
// or with a slash, not found in <playbook dir>/roles is looked up there
// before the playbook's own directory, as ansible-playbook does; its files
// are named "(requirements)/<role>/…" (InstalledRolesPrefix) and kept apart
// from any repository path of that name, and its reads count against the
// same limits as the repository's.
//
// It may also hand it the installed collections (ParseRepoInstalled,
// backlog 7.96): a collection role, named fully (namespace.collection.role)
// or short through a play's, block's or task's collections list, is looked
// up first in the collections beside the playbook and then in the installed
// ones, as ansible-playbook does; its files are named
// "(collections)/<namespace>/<collection>/roles/…"
// (InstalledCollectionsPrefix).
package varsource
