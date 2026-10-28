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
 * Usage:
 *   pulsix-setuid-exec UID PROGRAM [ARG ...]
 *   pulsix-setuid-exec UID --signal SIGNAL PGID
 *
 * Nothing the caller supplies picks the identity. argv[1] is the uid the
 * caller BELIEVES it is asking for, and the only thing this program does
 * with it is refuse when it disagrees with the compiled-in PLAY_UID. That
 * is a drift check, not an input: the worker reads PULSIX_WORKER_PLAY_UID
 * and the image compiles PLAY_UID in, two numbers that can be edited apart.
 * Without the check a play would quietly run as a uid nobody configured —
 * on a worker where the image's uid happens to be privileged, that is the
 * separation inverted. argv[2:] says WHAT to run, never AS WHOM.
 *
 * The second form (backlog 2.104) lets the worker reach a play's process
 * group when the play runs under a uid the worker itself may not signal.
 * It adds no new authority: the helper performs the exact same
 * setgroups/setgid/setuid drop below, with the same read-back of the
 * kernel's own answer, BEFORE it ever calls kill(2). At the moment it
 * signals, it holds exactly the privilege the play uid holds and nothing
 * more — root was given up several lines earlier. SIGNAL is exactly TERM
 * or KILL, the only two signals the worker's cancel policy ever sends, and
 * PGID must be a process group greater than 1: kill(-1, ...) means every
 * process this uid may signal, so admitting 0, 1 or a negative number here
 * would turn one cancel into a fleet-wide kill of the play uid's processes.
 *
 * The two forms are told apart by comparing argv[2] to the literal string
 * "--signal". That makes a PROGRAM literally named --signal indistinguishable
 * from the signal mode, which is accepted here on purpose: the worker always
 * passes PROGRAM as an absolute path (never a bare name looked up on PATH —
 * see the execv note below), so "--signal" can never occur as argv[2] in the
 * exec form in practice.
 *
 * Built by deploy/worker/Dockerfile, statically linked, installed
 * root-owned with mode 4750:
 *   cc -static -Wall -Wextra -Werror -O2 -DPLAY_UID=65533 -DWORKER_GID=65532
 */
#include <errno.h>
#include <grp.h>
#include <limits.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
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

/* parse_pgid parses PGID strictly: strtoul, reject a parse errno, an empty
 * parse, trailing characters, and — before strtoul ever sees the string —
 * anything but a digit in the first position. The leading character is
 * checked rather than merely tested for '-', because strtoul accepts a
 * sign AND skips leading whitespace first: a bare "s[0] == '-'" guard is
 * defeated by one space, and " -18446744073709551614" then wraps through
 * strtoul to 2 and is accepted. Requiring a digit rejects whitespace, '+'
 * and '-' in one condition, which is the whole-string strictness this
 * comment claims. It additionally rejects anything <= 1. kill(-1, ...) means
 * "every process this uid may signal", so a lax parse here is not merely
 * the wrong process group — it turns one cancel into a fleet-wide kill of
 * every process the play uid holds. 0 and 1 are refused for the same
 * reason: neither is ever a play's own process group.
 *
 * Both ends of the range matter, not just the lower one: v is parsed into
 * an unsigned long (commonly 64-bit) and then narrowed into a pid_t
 * (32-bit signed on every platform this builds for). Without a check
 * against the narrow type's own range, a value like 2^32+1 sails past
 * "> 1" on the wide type and then truncates to 1 on the cast — the exact
 * -1-equivalent fleet-wide kill the lower bound exists to prevent, just
 * reached from the other direction. Bounding v against INT_MAX before the
 * cast closes that: nothing that would change value across the narrowing
 * is accepted in the first place. */
static int parse_pgid(const char *s, pid_t *out) {
	if (s[0] < '0' || s[0] > '9') {
		return -1;
	}
	char *rest = NULL;
	errno = 0;
	unsigned long v = strtoul(s, &rest, 10);
	if (errno != 0 || rest == s || *rest != '\0') {
		return -1;
	}
	if (v <= 1 || v > (unsigned long)INT_MAX) {
		return -1;
	}
	*out = (pid_t)v;
	return 0;
}

/* signal_for_name maps exactly the two signals the worker's TERM->grace->
 * KILL cancel policy ever sends. Nothing else is accepted: a general
 * signal relay would be a larger privileged surface for no gain. */
static int signal_for_name(const char *name, int *out) {
	if (strcmp(name, "TERM") == 0) {
		*out = SIGTERM;
		return 0;
	}
	if (strcmp(name, "KILL") == 0) {
		*out = SIGKILL;
		return 0;
	}
	return -1;
}

int main(int argc, char *argv[]) {
	if (argc < 3) {
		fprintf(stderr, "usage: pulsix-setuid-exec UID PROGRAM [ARG ...]\n"
				"       pulsix-setuid-exec UID --signal SIGNAL PGID\n");
		return 1;
	}
	/* The drift check. strtoul alone would accept "65533junk" and, worse,
	 * "-1" (which wraps), so the whole string has to be consumed and a
	 * leading '-' rejected outright. It is not a strict numeric grammar:
	 * strtoul still skips leading whitespace and accepts a leading '+', so
	 * " +65533" parses. That is harmless — equality with PLAY_UID is
	 * required either way, and this program never does anything with the
	 * value but compare it. A caller and an image that disagree about the
	 * uid must stop here, naming both numbers, rather than run the play as
	 * whichever of the two happens to be compiled in. */
	char *rest = NULL;
	errno = 0;
	unsigned long wanted = strtoul(argv[1], &rest, 10);
	if (errno != 0 || rest == argv[1] || *rest != '\0' || argv[1][0] == '-') {
		fprintf(stderr, "pulsix-setuid-exec: %s is not a uid\n", argv[1]);
		return 1;
	}
	if (wanted != (unsigned long)play_uid) {
		fprintf(stderr, "pulsix-setuid-exec: asked for uid %lu but this helper is built for uid %u; "
				"PULSIX_WORKER_PLAY_UID and the image's PLAY_UID must be the same number\n",
			wanted, (unsigned)play_uid);
		return 1;
	}
	/* Mode detection, and then all of that mode's own validation, BEFORE
	 * the geteuid() refusal below. Validating first is what makes the
	 * argument contract testable without root: a bad signal name or pgid
	 * is refused here regardless of privilege, and only a well-formed
	 * SIGNAL and PGID ever reach the privilege check. */
	int signal_mode = (strcmp(argv[2], "--signal") == 0);
	int sig = 0;
	pid_t pgid = 0;
	if (signal_mode) {
		if (argc != 5) {
			fprintf(stderr, "usage: pulsix-setuid-exec UID --signal SIGNAL PGID\n");
			return 1;
		}
		if (signal_for_name(argv[3], &sig) != 0) {
			fprintf(stderr, "pulsix-setuid-exec: %s is an unsupported signal; only TERM and KILL are accepted\n",
				argv[3]);
			return 1;
		}
		if (parse_pgid(argv[4], &pgid) != 0) {
			fprintf(stderr, "pulsix-setuid-exec: %s is not a process group (must be an integer greater than 1)\n",
				argv[4]);
			return 1;
		}
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
	 * play. They can only be dropped while euid is still 0, so this is first.
	 * Read back like the uid and gid below, and for the same reason: a return
	 * value is a claim, the kernel's own answer is the fact. */
	if (setgroups(0, NULL) != 0) {
		return fail("setgroups");
	}
	if (getgroups(0, NULL) != 0) {
		fprintf(stderr, "pulsix-setuid-exec: supplementary groups survived the drop (%d left)\n", getgroups(0, NULL));
		return 1;
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
	if (signal_mode) {
		/* The drop above is the whole safety argument: this kill(2) runs
		 * with exactly the privilege the play uid holds and nothing more,
		 * so the kernel's own permission check confines it to processes
		 * uid 65533 may signal. ESRCH is not an error here — it means the
		 * process group finished on its own between the worker's decision
		 * to cancel and this call, the common race, not a failure to
		 * report. */
		if (kill(-pgid, sig) != 0 && errno != ESRCH) {
			return fail("kill");
		}
		return 0;
	}
	/* execv, not execvp: PATH comes from the environment, and the program to
	 * run must be decided by argv alone. The environment is passed through
	 * untouched — the worker already strips its own PULSIX_ and ANSIBLE_
	 * variables (runner.stripPrivateEnv) and this program reads none of it.
	 *
	 * Descriptors above 2 are not closed and the signal mask is not reset,
	 * which a setuid program that gains privilege would have to do. This one
	 * only ever LOSES privilege: uid 65533 holds strictly less than any
	 * account that can run this binary, so an inherited descriptor or a
	 * blocked signal reaches a process weaker than the one that passed it.
	 * It is the worker's own pipes the runner needs on 0/1/2 anyway. If the
	 * invariant in the Dockerfile beside mode 4750 ever stops holding, this
	 * paragraph stops holding with it. */
	execv(argv[2], &argv[2]);
	return fail(argv[2]);
}
