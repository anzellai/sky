#!/usr/bin/env bash
# scripts/lib/with-timeout.sh — bound a command's wall clock, portably.
#
# Why this exists
# ---------------
# GNU coreutils `timeout` is not a given. macOS ships neither `timeout` nor
# `gtimeout`; this repository's own dev shell supplied one from nix, and when
# that shell went away the binary went with it. Fourteen scripts invoked a bare
# `timeout`. What happened next is the whole point of this file:
#
#     $ cd runtime-go && timeout 1200 env CGO_ENABLED=1 go test -race ./rt/... | tail -8
#     (eval):1: command not found: timeout
#     [exited with code 0]
#
# The race detector never ran. `command not found` went to stderr, the
# pipeline's status came from `tail`, and the caller read exit 0 as a pass. A
# verification that did not run reported success — the same defect class this
# branch has spent seven adversarial rounds closing, this time in the harness
# that runs the gates.
#
# Three shapes in the tree turned a missing binary into a GREEN result rather
# than a red one, each demonstrated before this file was written:
#
#   * `scripts/verify-cli.sh` tui-start (`… || true` into an unchecked
#     capture): five TUI examples printed "✓ … (no panic)" having never
#     started the binary. No panic is easy when nothing runs.
#   * `scripts/test-ci.sh` phase_compiler_build (`( … ) && install_binary`
#     under `set -uo pipefail`, no `-e`): the compiler build silently did
#     nothing, the phase returned 0, and every later phase used whatever stale
#     `sky-out/sky` was lying around.
#   * `scripts/grill-mutation-matrix.sh` run_suite (`… > log 2>&1 || true`):
#     an empty log, from which `grep '^--- FAIL: '` extracts zero failures, so
#     the mutation matrix concludes the suite is fully green having run
#     nothing.
#
# The other eleven sites failed loudly — misleadingly (a missing `timeout` was
# reported as "build failed"), but non-zero. Loud-and-wrong is a bug; quiet-and-
# green is the bug that ships.
#
# The opposite failure is not hypothetical either, and is why this file never
# silently runs a command unbounded. `scripts/conformance.sh` used to fall
# through with no bound when it found no `timeout`, on the reasoning that "the
# CI job still has its own outer timeout". The macos-determinism job that runs
# it declares no `timeout-minutes`, so a wedged `sky test` burned GitHub's
# 6-hour default at the macOS minute multiplier.
#
# Six scripts had already open-coded a fallback, in four subtly different
# variants: two returned 137 where GNU `timeout` returns 124, one tried to map
# to 124 and documented that it could not do so reliably, none escalated
# TERM→KILL consistently. That is the "one site fixed, the copies survive"
# pattern this repository keeps getting bitten by. There is now one spelling.
#
# Usage
# -----
#     source "$ROOT/scripts/lib/with-timeout.sh"
#     with_timeout 600 go test ./rt/...        # returns the command's status
#     with_timeout 30 "$SKY" build src/Main.sky || rc=$?   # 124 == timed out
#
# Contract (identical on both implementations):
#   * On natural exit, the command's OWN exit status is returned, unchanged.
#     A shim that swallowed a non-zero exit would recreate the bug it exists
#     to prevent, one layer down.
#   * On expiry, 124 — GNU `timeout`'s convention. If the command ignored
#     SIGTERM and had to be SIGKILLed, 137, again exactly as GNU reports it,
#     so the two implementations are indistinguishable from a caller's status.
#   * A command that cannot be executed at all yields 127, as GNU does.
#   * A command killed by signal N yields 128+N, as the shell does.
#   * stdin/stdout/stderr are inherited, so `echo x | with_timeout 10 prog`
#     and `with_timeout 3 prog < /dev/null` behave as written.
#   * Expiry escalates: SIGTERM to the command's process group, then SIGKILL
#     after a grace period. A child that ignores TERM still dies.
#
# Resolution order: `timeout` → `gtimeout` → a `perl` fork/exec supervisor.
# If none of the three exists the shim FAILS, naming what to install. It never
# runs the command unbounded, and it never pretends to have run it.

# Idempotent: several scripts source more than one lib, and libs may source
# each other.
if [ -n "${_SKY_WITH_TIMEOUT_SOURCED:-}" ]; then
    return 0 2>/dev/null || true
fi
_SKY_WITH_TIMEOUT_SOURCED=1

# Grace period between SIGTERM and SIGKILL on expiry, seconds. Applies to both
# implementations so they cannot drift.
: "${SKY_WITH_TIMEOUT_KILL_AFTER:=10}"

# The perl supervisor. Kept in a variable so there is exactly one copy of it
# and the gate can find it. It forks rather than `exec`ing: `alarm()` does
# survive `exec`, but the exec'd process then dies of SIGALRM (status 142) and
# there is no wrapper left to translate that into 124 or to escalate to KILL.
_SKY_WITH_TIMEOUT_PERL_PROG='
use strict; use warnings;
use POSIX qw(:sys_wait_h);
use Time::HiRes qw(time sleep);

my $secs  = shift @ARGV;
my $after = shift @ARGV;
die "with_timeout: no command given\n" unless @ARGV;

my $pid = fork();
die "with_timeout: fork failed: $!\n" unless defined $pid;

if ($pid == 0) {
    # Own process group, so expiry can reach the whole tree the command
    # spawns (go test forks compilers and test binaries; sky build forks go).
    POSIX::setpgid(0, 0);
    # `no warnings "exec"` only silences the compile-time "statement unlikely
    # to be reached" notice; the lines below run when exec FAILS, which is the
    # 127 case and must not be dropped.
    { no warnings "exec"; exec { $ARGV[0] } @ARGV; }
    print STDERR "with_timeout: cannot run $ARGV[0]: $!\n";
    POSIX::_exit(127);
}
# Also from the parent: whichever wins the race, the group exists before the
# first kill() below.
eval { POSIX::setpgid($pid, $pid) };

# Forward the signals an operator or a CI runner actually sends, so Ctrl-C
# still reaches a command we have deliberately put in another process group.
for my $name (qw(TERM INT HUP QUIT)) {
    $SIG{$name} = sub { kill($name, -$pid) or kill($name, $pid); };
}

my $reap = sub {          # -> (reaped?, raw status)
    my $r = waitpid($pid, WNOHANG);
    return (1, $?) if $r == $pid;
    return (1, 0)  if $r < 0;     # nothing left to wait for
    return (0, 0);
};

my ($done, $status) = (0, 0);
my $deadline = time() + $secs;
while (1) {
    ($done, $status) = $reap->();
    last if $done;
    last if time() >= $deadline;
    sleep(0.02);
}

unless ($done) {
    kill("TERM", -$pid) or kill("TERM", $pid);
    my $grace = time() + $after;
    my $died = 0;
    while (time() < $grace) {
        my ($d) = $reap->();
        if ($d) { $died = 1; last; }
        sleep(0.02);
    }
    # 124 when SIGTERM was enough, 137 when the command ignored it and had to
    # be SIGKILLed. That is exactly what `timeout -k` reports, so the two
    # implementations cannot be told apart by an exit status.
    exit 124 if $died;
    kill("KILL", -$pid) or kill("KILL", $pid);
    waitpid($pid, 0);
    exit 137;
}

my $sig = $status & 127;
exit(128 + $sig) if $sig;
exit($status >> 8);
'

# Resolve once per shell. `SKY_WITH_TIMEOUT_IMPL` forces an implementation so
# the gate can exercise the fallback on a host that has the binary; it is a
# test hook, not a tuning knob.
_sky_with_timeout_resolve() {
    [ -n "${_SKY_WITH_TIMEOUT_RESOLVED:-}" ] && return 0

    _SKY_WITH_TIMEOUT_BIN=""
    _SKY_WITH_TIMEOUT_KFLAG=""

    case "${SKY_WITH_TIMEOUT_IMPL:-}" in
        perl)
            _SKY_WITH_TIMEOUT_RESOLVED=1
            return 0
            ;;
    esac

    if command -v timeout >/dev/null 2>&1; then
        _SKY_WITH_TIMEOUT_BIN="$(command -v timeout)"
    elif command -v gtimeout >/dev/null 2>&1; then
        _SKY_WITH_TIMEOUT_BIN="$(command -v gtimeout)"
    fi

    # `-k` is GNU; a busybox/toybox `timeout` may not have it. Probe rather
    # than assume, so the escalation behaviour matches the perl path wherever
    # it can and degrades to plain TERM where it cannot.
    if [ -n "$_SKY_WITH_TIMEOUT_BIN" ]; then
        if "$_SKY_WITH_TIMEOUT_BIN" -k 1 1 true >/dev/null 2>&1; then
            _SKY_WITH_TIMEOUT_KFLAG="-k $SKY_WITH_TIMEOUT_KILL_AFTER"
        fi
    fi

    _SKY_WITH_TIMEOUT_RESOLVED=1
}

# with_timeout <seconds> <command> [args...]
with_timeout() {
    if [ "$#" -lt 2 ] || [ -z "${1:-}" ]; then
        echo "with_timeout: usage: with_timeout <seconds> <command> [args...]" >&2
        return 125
    fi
    local secs="$1"
    shift

    _sky_with_timeout_resolve

    if [ -n "$_SKY_WITH_TIMEOUT_BIN" ]; then
        # $_SKY_WITH_TIMEOUT_KFLAG is deliberately unquoted: it is either empty
        # or the two words `-k N`.
        #
        # On the KILL path `timeout -k` re-raises SIGKILL on itself to report
        # 137, so bash prints `Killed: 9` naming the line below. That is bash
        # accurately reporting the status, not a fault in this file; it cannot
        # be suppressed without also swallowing the command's own stderr, and a
        # subshell does not help (bash execs a single-command subshell).
        # shellcheck disable=SC2086
        "$_SKY_WITH_TIMEOUT_BIN" $_SKY_WITH_TIMEOUT_KFLAG "$secs" "$@"
        return $?
    fi

    if command -v perl >/dev/null 2>&1; then
        perl -e "$_SKY_WITH_TIMEOUT_PERL_PROG" -- \
            "$secs" "$SKY_WITH_TIMEOUT_KILL_AFTER" "$@"
        return $?
    fi

    # No bounding mechanism at all. Running unbounded is how a wedged `sky
    # test` burned six hours of macOS CI minutes; running nothing and claiming
    # success is worse still. Fail, and name the fix.
    echo "with_timeout: cannot bound '$1' — no 'timeout', no 'gtimeout', no 'perl' on PATH." >&2
    echo "  Install one of:  coreutils (provides timeout/gtimeout)  |  perl" >&2
    echo "    macOS:  brew install coreutils" >&2
    echo "    Debian: apt-get install -y coreutils perl" >&2
    return 127
}
