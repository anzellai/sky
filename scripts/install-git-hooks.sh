#!/usr/bin/env bash
# Install repo-local git hooks. Run once after clone.
#
# Hooks installed:
#   pre-push — if pushing a tag matching v*, refuse unless
#              `scripts/preflight-tag.sh` exited 0 within the last
#              30 minutes (recorded in .git/last-preflight-pass).
#
# Rationale: the v0.13.0 → v0.13.2 release loop shipped two patches
# in a row because the runtime-verify step was not enforced before
# tagging. The hook makes the verification mandatory at the only
# moment that matters: tag-push time.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# --git-common-dir, not "$REPO_ROOT/.git": inside a git worktree `.git` is a
# FILE pointing at the real gitdir, so the literal path resolves to nothing and
# hooks install (and the stamp below lands) somewhere the hook can never read.
# `git rev-parse --git-common-dir` prints a path RELATIVE to the repo (".git"),
# so it is resolved from inside REPO_ROOT; read raw from another cwd it would
# install the hook under "$PWD/.git/hooks", where git never looks.
HOOK_DIR="$(cd "$REPO_ROOT" && cd "$(git rev-parse --git-common-dir)" && pwd)/hooks"
mkdir -p "$HOOK_DIR"

cat > "$HOOK_DIR/pre-push" <<'EOF'
#!/usr/bin/env bash
# Refuse to push a v*-shaped tag unless preflight passed recently.
# Bypass: git push --no-verify (use ONLY for non-release pushes).

REPO_ROOT="$(git rev-parse --show-toplevel)"
# Shared across worktrees — see the note in install-git-hooks.sh.
STAMP="$(git rev-parse --git-common-dir)/last-preflight-pass"
MAX_AGE_SECONDS=1800   # 30 min

is_tag_push=0
while read -r local_ref local_sha remote_ref remote_sha; do
    case "$remote_ref" in
        refs/tags/v*) is_tag_push=1 ;;
    esac
done

if [ $is_tag_push -eq 0 ]; then
    exit 0
fi

if [ ! -f "$STAMP" ]; then
    echo "✗ Refusing to push tag: scripts/preflight-tag.sh has not run." >&2
    echo "  Run it first, then re-push." >&2
    echo "" >&2
    echo "  cd $REPO_ROOT && scripts/preflight-tag.sh" >&2
    exit 1
fi

now=$(date +%s)
# Read the stamp's mtime FAIL-CLOSED. The old form was
# `stat -f %m F || stat -c %Y F`; with GNU coreutils first on PATH (common on
# macOS dev machines) `stat -f` means --file-system, prints a multi-line block
# and EXITS 0, so the junk became the mtime, the arithmetic and the `[ -gt ]`
# both errored, and the hook fell through to "allowing tag push". Now each
# candidate (GNU, BSD, then `date -r`) is accepted only if it is a pure
# integer, and anything else refuses.
is_uint() {
    case "$1" in
        '' | *[!0-9]*) return 1 ;;
        *) return 0 ;;
    esac
}
stamp_mtime=""
for candidate in \
    "$(stat -c %Y "$STAMP" 2>/dev/null)" \
    "$(stat -f %m "$STAMP" 2>/dev/null)" \
    "$(date -r "$STAMP" +%s 2>/dev/null)"; do
    if is_uint "$candidate"; then
        stamp_mtime="$candidate"
        break
    fi
done
if ! is_uint "$now" || ! is_uint "$stamp_mtime"; then
    echo "✗ Refusing to push tag: could not read an integer mtime for $STAMP" >&2
    echo "  (tried stat -c %Y, stat -f %m and date -r; now='$now')." >&2
    echo "  Re-run scripts/preflight-tag.sh and retry, or check the stat/date on PATH." >&2
    exit 1
fi
stamp_age=$(( now - stamp_mtime ))
if [ "$stamp_age" -lt 0 ]; then
    echo "✗ Refusing to push tag: preflight stamp mtime is in the future (${stamp_age}s)." >&2
    echo "  Re-run scripts/preflight-tag.sh, then re-push." >&2
    exit 1
fi
if [ "$stamp_age" -gt "$MAX_AGE_SECONDS" ]; then
    echo "✗ Refusing to push tag: preflight stamp is $stamp_age s old (>${MAX_AGE_SECONDS}s)." >&2
    echo "  Re-run scripts/preflight-tag.sh, then re-push." >&2
    exit 1
fi

echo "✓ Preflight stamp fresh ($stamp_age s old); allowing tag push."
EOF

chmod +x "$HOOK_DIR/pre-push"
echo "✓ Installed $HOOK_DIR/pre-push"
echo ""
echo "  Run scripts/preflight-tag.sh before any tag push to populate"
echo "  the .git/last-preflight-pass stamp."
