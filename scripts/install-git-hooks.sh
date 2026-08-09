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
HOOK_DIR="$(git -C "$REPO_ROOT" rev-parse --git-common-dir)/hooks"
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
# Portable mtime read: try BSD `stat -f %m` first (macOS), then GNU
# `stat -c %Y` (Linux). Capture stderr to /dev/null so the wrong-OS
# form doesn't pollute the hook output. Both forms return a bare
# integer on success.
stamp_mtime=$(stat -f %m "$STAMP" 2>/dev/null || stat -c %Y "$STAMP" 2>/dev/null)
if [ -z "$stamp_mtime" ]; then
    echo "✗ Could not read mtime of $STAMP (stat -f / -c both failed)." >&2
    echo "  Re-run scripts/preflight-tag.sh and retry." >&2
    exit 1
fi
stamp_age=$(( now - stamp_mtime ))
if [ $stamp_age -gt $MAX_AGE_SECONDS ]; then
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
