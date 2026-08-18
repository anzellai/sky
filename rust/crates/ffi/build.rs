// Build script for the `ffi` crate — stages the compiler asset trees into
// `$OUT_DIR/embedded-assets/` so `src/assets.rs` can embed them with
// `include_dir!` and the shipped `sky` binary is standalone (doc 09 §E).
//
// Why a filtered copy instead of `include_dir!` straight at the repo trees:
//   * `tools/sky-ffi-inspect/` carries a committed 6.7 MB prebuilt binary — an
//     *output*, not a source. Embedding it would bloat every `sky` binary and,
//     because the binary changes per rebuild, break determinism. The filter
//     drops it (mirrors `EmbeddedInspector.collectToolSources`).
//   * `runtime-go/rt/` carries `*_test.go` + `testdata/`; stripping those at
//     stage time keeps the embedded payload lean (doc 09 §E.3). `console_app/`
//     IS embedded (unlike the normal user-app materialise, which skips it):
//     `rt/hub` — built by `sky console-serve` via the embedded `cmd/sky-hub` —
//     blank-imports `sky-app/rt/console_app`, so the standalone hub build needs
//     it on disk.
//   * `sky-bundled/` (the `console` + `doc` bundled Sky apps) is embedded
//     source-only: its committed `sky-out/` / `.skycache/` / `.skydeps/` build
//     artefacts are dropped so only `src/` + `sky.toml` ship.
//
// The `cargo:rerun-if-changed` lines below make Cargo re-stage whenever any
// source tree changes — new files included by construction. This is the direct
// replacement for the Haskell compiler's 89 hand-written "re-embed marker"
// comments (doc 09 §E.2): embedding staleness becomes structurally impossible.

use std::path::{Path, PathBuf};

fn main() {
    sha256_matches_the_test_vectors();
    let manifest = PathBuf::from(env("CARGO_MANIFEST_DIR"));
    // rust/crates/ffi -> rust/crates -> rust -> <repo root>
    let repo = manifest
        .ancestors()
        .nth(3)
        .expect("ffi crate must live at <repo>/rust/crates/ffi")
        .to_path_buf();
    let dest = PathBuf::from(env("OUT_DIR")).join("embedded-assets");

    // Fresh staging every run keyed off rerun-if-changed; a stale file left from
    // a since-deleted source must not survive into the embed.
    let _ = std::fs::remove_dir_all(&dest);
    std::fs::create_dir_all(&dest).expect("mkdir embedded-assets");

    // sky-stdlib/ — the .sky stdlib (read by the build driver + `sky doc`).
    stage(&repo.join("sky-stdlib"), &dest.join("sky-stdlib"));
    // runtime-go/ — go.mod + go.sum + the rt/ tree (materialised beside main.go)
    // + cmd/sky-hub (the pure-Go console hub `sky console-serve` builds).
    stage_runtime(&repo.join("runtime-go"), &dest.join("runtime-go"));
    // tools/sky-ffi-inspect/ — the Go introspector source (ensure_inspector).
    stage(
        &repo.join("tools").join("sky-ffi-inspect"),
        &dest.join("tools").join("sky-ffi-inspect"),
    );
    // templates/ — CLAUDE.md et al. (copied by `sky init`).
    stage(&repo.join("templates"), &dest.join("templates"));
    // sky-bundled/ — the console + doc bundled Sky apps `sky console` /
    // `sky doc --serve` / `sky doc --tui` build + spawn. Source only: the
    // committed `sky-out/` / `.skycache/` / `.skydeps/` build artefacts are
    // dropped by `skip_dir` so the embed carries just `src/` + `sky.toml`.
    stage(&repo.join("sky-bundled"), &dest.join("sky-bundled"));

    // The fingerprint is computed over the five staged roots BEFORE the marker
    // file below is written, so it never hashes itself — and so the shell
    // reconstruction (which walks the SOURCE trees) computes the same set.
    let fp = fingerprint(&dest);

    // Two channels for the same value, for two different consumers:
    //
    //   1. `cargo:rustc-env` — referenced by `src/assets.rs` via `env!`, which
    //      makes the value part of that crate's compile input: a changed
    //      fingerprint recompiles `assets.rs` and re-runs its `include_dir!`
    //      (whose file dependencies rustc does NOT track, unlike
    //      `include_str!`), so a re-staged tree can never stay embedded stale
    //      until `cargo clean -p ffi`.
    //
    //   2. A `embed-fingerprint` FILE inside the staged tree — this is the
    //      channel `scripts/lib/fresh-compiler.sh` greps out of a compiled
    //      binary. It exists because channel 1 is NOT reliably present in the
    //      binary: `env!` in a function nothing calls is dead code, and the
    //      release build strips it — measured on this repository, the first
    //      release `sky` built with only channel 1 contained no
    //      `sky-embed-fp-v1:` bytes at all. `include_dir!` data is the asset
    //      payload itself; it cannot be stripped while the assets work.
    std::fs::write(dest.join("embed-fingerprint"), format!("{fp}\n"))
        .expect("write embed-fingerprint marker file");
    println!("cargo:rustc-env=SKY_EMBED_FINGERPRINT={fp}");

    // Re-stage when any source tree changes (new files included).
    rerun(&repo.join("sky-stdlib"));
    rerun(&repo.join("runtime-go"));
    rerun(&repo.join("tools").join("sky-ffi-inspect"));
    rerun(&repo.join("templates"));
    rerun(&repo.join("sky-bundled"));
    // And when this script itself changes.
    rerun(&manifest.join("build.rs"));
}

/// Marker prefix baked (via `assets.rs`'s `env!`) into the compiled binary's
/// read-only data, so a tool that has only the BINARY can recover the embed
/// fingerprint with a `grep`-able needle — no CLI flag, no execution. This is
/// what lets `scripts/lib/fresh-compiler.sh` compare a prebuilt binary's
/// embedded content against the tree instead of trusting mtimes in both false
/// directions (a fresh checkout false-failed; a `touch`ed stale binary
/// false-passed).
const FP_MARKER: &str = "sky-embed-fp-v1:";

/// Content fingerprint of the whole staged tree: `sky-embed-fp-v1:<sha256hex>`.
///
/// Any edit to an embedded stdlib/runtime/template file changes this, which —
/// as a `rustc-env` referenced by `src/assets.rs` — forces that crate to
/// recompile and re-run its `include_dir!`, so the embed can never go stale
/// after a re-stage.
///
/// The construction is deliberately REPRODUCIBLE BY SHELL, because
/// `scripts/lib/fresh-compiler.sh::sky_embed_fingerprint_expected` computes the
/// same value from the source tree with `sha256sum`/`shasum` and compares it to
/// the marker it greps out of a binary
/// (`gates_measure_a_fresh_compiler.rs::the_shell_and_rust_fingerprints_agree`
/// fails the build if the two constructions drift):
///
///   * one manifest line per staged file, `"{sha256hex(bytes)}  {relpath}\n"` —
///     exactly the output format of `sha256sum`/`shasum -a 256`;
///   * relpaths use `/` separators and sort as BYTE STRINGS (`LC_ALL=C sort`),
///     not as path components — `PathBuf` ordering differs the moment a name
///     contains a byte below `/`;
///   * the fingerprint is the sha256 of the manifest, prefixed with
///     [`FP_MARKER`].
///
/// This replaced a `DefaultHasher` (SipHash with unspecified keys — explicitly
/// not stable across Rust releases, and not computable outside Rust at all).
pub fn fingerprint(dir: &Path) -> String {
    let mut files: Vec<PathBuf> = Vec::new();
    collect_files(dir, &mut files);
    let mut rels: Vec<(String, PathBuf)> = files
        .into_iter()
        .map(|f| {
            let rel = f
                .strip_prefix(dir)
                .unwrap_or(&f)
                .to_string_lossy()
                .replace('\\', "/");
            (rel, f)
        })
        .collect();
    rels.sort_by(|a, b| a.0.as_bytes().cmp(b.0.as_bytes()));
    let mut manifest = String::new();
    for (rel, f) in &rels {
        let bytes = std::fs::read(f).unwrap_or_else(|e| panic!("read {}: {e}", f.display()));
        manifest.push_str(&sha256_hex(&bytes));
        manifest.push_str("  ");
        manifest.push_str(rel);
        manifest.push('\n');
    }
    format!("{FP_MARKER}{}", sha256_hex(manifest.as_bytes()))
}

/// SHA-256 (FIPS 180-4), self-contained so the build script stays
/// dependency-free. Correctness is pinned by [`sha256_matches_the_test_vectors`]
/// and, end-to-end, by the shell-parity gate in
/// `gates_measure_a_fresh_compiler.rs`, which compares this against
/// `sha256sum`'s answer for the same tree.
fn sha256_hex(data: &[u8]) -> String {
    const K: [u32; 64] = [
        0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4,
        0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe,
        0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f,
        0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
        0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc,
        0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
        0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116,
        0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7,
        0xc67178f2,
    ];
    let mut h: [u32; 8] = [
        0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab,
        0x5be0cd19,
    ];
    // Pad: message ++ 0x80 ++ zeros ++ 64-bit big-endian bit length.
    let mut msg = data.to_vec();
    let bit_len = (data.len() as u64).wrapping_mul(8);
    msg.push(0x80);
    while msg.len() % 64 != 56 {
        msg.push(0);
    }
    msg.extend_from_slice(&bit_len.to_be_bytes());

    let mut w = [0u32; 64];
    for block in msg.chunks_exact(64) {
        for (i, word) in block.chunks_exact(4).enumerate() {
            w[i] = u32::from_be_bytes([word[0], word[1], word[2], word[3]]);
        }
        for i in 16..64 {
            let s0 = w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3);
            let s1 = w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16]
                .wrapping_add(s0)
                .wrapping_add(w[i - 7])
                .wrapping_add(s1);
        }
        let [mut a, mut b, mut c, mut d, mut e, mut f, mut g, mut hh] = h;
        for i in 0..64 {
            let s1 = e.rotate_right(6) ^ e.rotate_right(11) ^ e.rotate_right(25);
            let ch = (e & f) ^ (!e & g);
            let t1 = hh
                .wrapping_add(s1)
                .wrapping_add(ch)
                .wrapping_add(K[i])
                .wrapping_add(w[i]);
            let s0 = a.rotate_right(2) ^ a.rotate_right(13) ^ a.rotate_right(22);
            let maj = (a & b) ^ (a & c) ^ (b & c);
            let t2 = s0.wrapping_add(maj);
            hh = g;
            g = f;
            f = e;
            e = d.wrapping_add(t1);
            d = c;
            c = b;
            b = a;
            a = t1.wrapping_add(t2);
        }
        h[0] = h[0].wrapping_add(a);
        h[1] = h[1].wrapping_add(b);
        h[2] = h[2].wrapping_add(c);
        h[3] = h[3].wrapping_add(d);
        h[4] = h[4].wrapping_add(e);
        h[5] = h[5].wrapping_add(f);
        h[6] = h[6].wrapping_add(g);
        h[7] = h[7].wrapping_add(hh);
    }
    let mut out = String::with_capacity(64);
    for word in h {
        out.push_str(&format!("{word:08x}"));
    }
    out
}

/// NIST FIPS 180-4 test vectors. Runs in the build script itself — the cost is
/// microseconds — so a broken hash can never emit a fingerprint at all.
fn sha256_matches_the_test_vectors() {
    assert_eq!(
        sha256_hex(b""),
        "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    );
    assert_eq!(
        sha256_hex(b"abc"),
        "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
    );
    assert_eq!(
        sha256_hex(b"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq"),
        "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"
    );
}

fn collect_files(dir: &Path, out: &mut Vec<PathBuf>) {
    if let Ok(rd) = std::fs::read_dir(dir) {
        for e in rd.flatten() {
            let p = e.path();
            if p.is_dir() {
                collect_files(&p, out);
            } else {
                out.push(p);
            }
        }
    }
}

fn env(k: &str) -> String {
    std::env::var(k).unwrap_or_else(|_| panic!("missing env var {k}"))
}

/// Emit `cargo:rerun-if-changed` for `p` AND, when it's a directory, every file
/// beneath it — recursively. A directory-only watch does NOT fire when a file's
/// CONTENT changes in place (Cargo compares the directory's own mtime, which an
/// in-place edit leaves untouched), so an edit to e.g. `runtime-go/rt/live.go`
/// would not re-stage the embedded runtime and the change would silently miss
/// the next `sky` binary. Watching each file closes that gap; over-watching a
/// staged-but-filtered file only costs a redundant re-stage, never a stale embed.
fn rerun(p: &Path) {
    println!("cargo:rerun-if-changed={}", p.display());
    if p.is_dir() {
        if let Ok(entries) = std::fs::read_dir(p) {
            for e in entries.flatten() {
                rerun(&e.path());
            }
        }
    }
}

/// Recursively copy `src` → `dst`, applying the shared non-embeddable filter
/// (mirrors `EmbedDirTH.isEmbeddableRuntimeFile/Dir`).
pub fn stage(src: &Path, dst: &Path) {
    if !src.is_dir() {
        return;
    }
    let mut entries: Vec<PathBuf> = std::fs::read_dir(src)
        .unwrap_or_else(|e| panic!("read {}: {e}", src.display()))
        .filter_map(|e| e.ok().map(|e| e.path()))
        .collect();
    entries.sort();
    for path in entries {
        let name = path.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if path.is_dir() {
            if skip_dir(name) {
                continue;
            }
            stage(&path, &dst.join(name));
        } else if !skip_file(name) {
            copy_file(&path, &dst.join(name));
        }
    }
}

/// Stage `runtime-go/` selectively: `go.mod`, `go.sum`, the `rt/` tree (read by
/// the driver's write_out + ensure_go_mod), and `cmd/sky-hub/` (built directly
/// by `sky console-serve` — `go build ./cmd/sky-hub` against this tree). The
/// `rt/` stage includes `rt/console_app/` because `rt/hub` (which sky-hub
/// imports) blank-imports it, so a standalone hub build needs it present.
pub fn stage_runtime(src: &Path, dst: &Path) {
    std::fs::create_dir_all(dst).unwrap_or_else(|e| panic!("mkdir {}: {e}", dst.display()));
    for f in ["go.mod", "go.sum"] {
        let p = src.join(f);
        if p.is_file() {
            copy_file(&p, &dst.join(f));
        }
    }
    stage(&src.join("rt"), &dst.join("rt"));
    // cmd/ — the `sky-hub` daemon main package (`sky console-serve`).
    stage(&src.join("cmd"), &dst.join("cmd"));
}

fn copy_file(src: &Path, dst: &Path) {
    if let Some(parent) = dst.parent() {
        std::fs::create_dir_all(parent)
            .unwrap_or_else(|e| panic!("mkdir {}: {e}", parent.display()));
    }
    std::fs::copy(src, dst)
        .unwrap_or_else(|e| panic!("copy {} -> {}: {e}", src.display(), dst.display()));
}

/// Non-embeddable files: anything hidden, Go test files, the committed
/// inspector binary, editor junk.
///
/// **Hidden files are excluded as a CLASS, not by name.** `.DS_Store` used to
/// be the only dot-name here, and the omission of the rest embedded a runtime
/// SECRET into locally-built compilers: running a bundled console writes a 0600
/// `sky-bundled/<app>/.sky/console-token` (gitignored, regenerated at runtime),
/// and because `.sky` was not in `skip_dir` the token was staged into
/// `embedded-assets/` and baked into `sky-out/sky` — its bytes were confirmed
/// present in an installed binary, and `ffi::extract_assets_root` would have
/// re-materialised it into `~/.cache/sky/assets/<hash>/` on any machine running
/// that binary standalone. No file a build should embed is hidden (verified:
/// `git ls-files` over every staged root finds zero tracked dot-paths), while
/// the hidden names that DO appear locally are exactly the ones that must never
/// ship: `.sky/console-token`, `.env`, `.skydata`, `.skycache`, `.skydeps`.
/// `gates_measure_a_fresh_compiler.rs` stages a tree containing those and fails
/// if any survives, and separately `git check-ignore`s everything staged from
/// the real repo. Matches `EmbedDirTH.isEmbeddableRuntimeFile` plus the class
/// rule.
fn skip_file(name: &str) -> bool {
    name.starts_with('.')
        || name.ends_with("_test.go")
        || name == "sky-ffi-inspect"
        || name == "sky-ffi-inspect.exe"
        || name.ends_with(".bak")
        || name.ends_with(".swp")
        || name.ends_with('~')
}

/// Non-embeddable dirs: anything hidden (see [`skip_file`] — `.sky/` is where
/// the runtime writes `console-token`; `.skycache`/`.skydeps`/`.git` are the
/// named instances the old list carried), test fixtures, and committed build
/// outputs (`sky-out/` under `sky-bundled/*`). NOTE: `console_app` is
/// intentionally NOT skipped — `rt/hub` (built by `sky console-serve` via
/// `cmd/sky-hub`) blank-imports `sky-app/rt/console_app`, so the standalone hub
/// build needs it. The normal user-app build re-materialises `rt/` beside
/// `main.go` and skips `console_app` there (see
/// `project::build::materialise_rt`), so it never bloats a user's `sky-out/`.
fn skip_dir(name: &str) -> bool {
    name.starts_with('.') || matches!(name, "sky-out" | "testdata" | "node_modules")
}
