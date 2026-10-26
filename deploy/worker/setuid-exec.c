/*
 * pulsix-setuid-exec — the one privileged step of the playbook uid
 * separation (docs/superpowers/specs/2026-10-26-playbook-uid-separation-design.md).
 *
 * The worker starts ansible-runner under a second, lower uid so that a play
 * task targeting `localhost` cannot read the worker's client certificate and
 * key. Changing uid needs privilege, and the design's anti-goal forbids the
 * short way there: CAP_SETUID on the worker binary would let a compromised
 * worker call setuid(0) and so hand it a path to root it does not have
 * today — hardening in appearance, a weakening in fact. The privilege lives
 * here instead, in a program short enough to read in one sitting that can
 * only ever become the single uid compiled in below.
 *
 * Nothing the caller supplies picks the identity: not an argument, not an
 * environment variable. argv[1:] says WHAT to run, never AS WHOM.
 *
 * Built by deploy/worker/Dockerfile, statically linked, installed
 * root-owned with mode 4755:
 *   cc -static -Wall -Wextra -Werror -O2 -DPLAY_UID=65533 -DWORKER_GID=65532
 */
#include <errno.h>
#include <grp.h>
#include <stdio.h>
#include <string.h>
#include <sys/types.h>
#include <unistd.h>

#ifndef PLAY_UID
#error "PLAY_UID must be defined at compile time; there is no default uid to fall back to"
#endif
#ifndef WORKER_GID
#error "WORKER_GID must be defined at compile time; it must match the worker's own gid"
#endif
#if PLAY_UID == 0
#error "PLAY_UID must not be 0: exec'ing as root is the exact inverse of this program"
#endif

static const uid_t play_uid = (uid_t)PLAY_UID;
static const gid_t worker_gid = (gid_t)WORKER_GID;

static int fail(const char *what) {
	fprintf(stderr, "pulsix-setuid-exec: %s: %s\n", what, strerror(errno));
	return 1;
}

int main(int argc, char *argv[]) {
	if (argc < 2) {
		fprintf(stderr, "usage: pulsix-setuid-exec PROGRAM [ARG ...]\n");
		return 1;
	}
	/* Without euid 0 none of the drops below can change identity, and the
	 * program would exec the play as its CALLER — the worker's own uid, with
	 * the worker's own key readable. That is the separation silently not
	 * happening, so a lost setuid bit has to be a loud failure rather than a
	 * quiet pass-through. */
	if (geteuid() != 0) {
		fprintf(stderr, "pulsix-setuid-exec: not setuid-root (euid %u); refusing to exec without dropping privilege\n",
			(unsigned)geteuid());
		return 1;
	}
	/* Supplementary groups are inherited from the worker and would otherwise
	 * survive the drop, carrying every group access the worker holds into the
	 * play. They can only be dropped while euid is still 0, so this is first. */
	if (setgroups(0, NULL) != 0) {
		return fail("setgroups");
	}
	/* setgid before setuid: after the uid drop there is no privilege left to
	 * change the gid with. The gid is the WORKER's on purpose — the worker has
	 * to read back the artifacts the play writes, which is what keeps the
	 * run's event stream working. */
	if (setgid(worker_gid) != 0) {
		return fail("setgid");
	}
	/* setuid() from euid 0 sets the real, effective AND saved uid, so the
	 * exec'd play has no way back. seteuid() would leave the saved uid at 0
	 * and turn this program into a root escalation for anyone who can run it. */
	if (setuid(play_uid) != 0) {
		return fail("setuid");
	}
	/* Never trust the return values alone: the classic setuid defect is a drop
	 * that was refused (RLIMIT_NPROC, a seccomp filter) and went unnoticed.
	 * Read the identity back so a partial drop can never reach execv. */
	if (getuid() != play_uid || geteuid() != play_uid || getgid() != worker_gid || getegid() != worker_gid) {
		fprintf(stderr, "pulsix-setuid-exec: the privilege drop did not take (uid %u/%u, gid %u/%u)\n",
			(unsigned)getuid(), (unsigned)geteuid(), (unsigned)getgid(), (unsigned)getegid());
		return 1;
	}
	/* execv, not execvp: PATH comes from the environment, and the program to
	 * run must be decided by argv alone. The environment is passed through
	 * untouched — the worker already strips its own PULSIX_ and ANSIBLE_
	 * variables (runner.stripPrivateEnv) and this program reads none of it. */
	execv(argv[1], &argv[1]);
	return fail(argv[1]);
}
