// Package requirements holds the per-commit cache of Ansible Galaxy roles and
// collections that a repository playbook needs, shared by the worker's run
// path and its variable sync.
//
// The cache sits next to the repository clone, outside its working tree:
//
//	<RepoCacheDir>/<org-id>/<repo-id>                 the clone
//	<RepoCacheDir>/<org-id>/<repo-id>.requirements/   0700, created by the worker
//	    .lock                                         flock target
//	    .tmp-<random>/                                an install in progress
//	    <commit>-<hash16>/                            a published entry, never modified
//	        roles/
//	        collections/ansible_collections/<ns>/<name>
//
// Every operation on the cache directory and on an entry goes through the
// *os.Root that OpenDir returns, so a path component swapped for a symlink
// between a check and an open cannot lead out of the cache.
//
// The package repairs nothing the worker did not put there. A symlink or a
// file at the cache or entry path is refused and named, never removed or
// followed; only the worker's own leftovers (temporary directories, evicted
// entries) are ever deleted.
//
// Galaxy's behaviour is cited by function name, read against ansible-core
// 2.21.5.
//
// The package imports nothing from internal/worker or internal/worker/runner;
// both import it.
package requirements
