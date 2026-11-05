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
package varsource
