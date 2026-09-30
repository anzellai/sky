#!/usr/bin/env bash
# Regenerate rust/crates/hir/data/stdlib-exports-<tag>.tsv, the snapshot of what
# the stdlib exported in a previous release. The E-5 collision test
# (rust/crates/hir/tests/stdlib_collisions.rs) and the [E1012] message
# (rust/crates/hir/src/stdlib_history.rs) read it.
#
#   rust/crates/hir/tests/regen-stdlib-exports-snapshot.sh v0.26.1
#
# The exports are computed by the CURRENT hir (`compute_exports`), from the
# tagged `sky-stdlib/` tree and the tagged `hir/src/kernel.rs` kernel table, so
# the comparison uses one export rule on both sides.
set -euo pipefail
tag="${1:?usage: $0 <release tag, e.g. v0.26.1>}"
root="$(git rev-parse --show-toplevel)"
source "$root/scripts/lib/with-timeout.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git -C "$root" archive "$tag" sky-stdlib | tar -x -C "$work"
git -C "$root" show "$tag:rust/crates/hir/src/kernel.rs" > "$work/kernel.rs"
out="$root/rust/crates/hir/data/stdlib-exports-$tag.tsv"
cd "$root/rust"
SKY_SNAPSHOT_STDLIB="$work/sky-stdlib" \
SKY_SNAPSHOT_KERNEL_RS="$work/kernel.rs" \
SKY_SNAPSHOT_TAG="$tag" \
SKY_SNAPSHOT_OUT="$out.tmp" \
  with_timeout 1800 cargo test -p hir --test stdlib_collisions \
    regenerate_previous_release_snapshot -- --ignored --exact
mv -f "$out.tmp" "$out"
echo "wrote $out ($(grep -vc '^#' "$out") rows)"
