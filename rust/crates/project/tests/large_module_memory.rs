//! Compiler peak memory on a large generated module — a regression budget.
//!
//! A Sky.Spa auto-split generates one `Shared` module holding a
//! `Codec.object Ctor |> Codec.field … |> Codec.buildObject` pipeline per wire
//! record. Every step of such a pipeline has a type that carries the whole
//! N-ary constructor arrow over the fully expanded field types, and there are
//! O(N) steps, so storing one owned `Ty` per expression grows as
//! O(N × size(record)) per codec. The whole-program lowerer held every def's
//! table at once (twice: the salsa `infer` memo plus the lowerer's own copy)
//! and keyed its `go_ty` memo on owned `Ty` clones of every sub-tree. On a real
//! app's backend leg that reached 6.6 GB and mem-guard killed the build.
//!
//! The fix hash-conses those tables (`ty::tytable`) and the memo keys, so a
//! codec's cost follows the number of DISTINCT types, not the sum of their
//! sizes. This test pins it with a counting global allocator: it lowers the
//! same program with 1 and with `K` wide codecs and bounds the extra peak live
//! bytes the `K - 1` added codecs cost. Allocation bytes, not RSS, so the
//! figure does not depend on the allocator returning pages to the OS.
//!
//! Measured with this fixture (60 fields, `cargo test` profile): before the fix
//! each added codec cost 36 MB of peak live heap; after it, 0.6 MB.

use project::emit_example_source;
use std::alloc::{GlobalAlloc, Layout, System};
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};

struct Counting;

static LIVE: AtomicUsize = AtomicUsize::new(0);
static PEAK: AtomicUsize = AtomicUsize::new(0);

fn note_alloc(size: usize) {
    let now = LIVE.fetch_add(size, Ordering::Relaxed) + size;
    PEAK.fetch_max(now, Ordering::Relaxed);
}

// SAFETY: every method forwards to `System` unchanged and only adds
// bookkeeping on atomics, which never allocate.
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let p = System.alloc(layout);
        if !p.is_null() {
            note_alloc(layout.size());
        }
        p
    }
    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        let p = System.alloc_zeroed(layout);
        if !p.is_null() {
            note_alloc(layout.size());
        }
        p
    }
    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        System.dealloc(ptr, layout);
        LIVE.fetch_sub(layout.size(), Ordering::Relaxed);
    }
    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let p = System.realloc(ptr, layout, new_size);
        if !p.is_null() {
            LIVE.fetch_sub(layout.size(), Ordering::Relaxed);
            note_alloc(new_size);
        }
        p
    }
}

#[global_allocator]
static GLOBAL: Counting = Counting;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(
            dir.pop(),
            "could not locate repo root (no sky-stdlib ancestor)"
        );
    }
}

/// Fields per wide record. The real module's wire records reach ~70.
const FIELDS: usize = 60;
/// Wide codecs in the large variant.
const K: usize = 4;

/// A program with `codecs` wide records, each with a `Codec.object` pipeline
/// over `FIELDS` fields that mix a nested record, lists and maybes of it, and
/// primitives — the shape the Sky.Spa split generates. `main` encodes every
/// record, so DCE keeps every codec.
fn program(codecs: usize) -> String {
    let mut s = String::from(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Codec as Codec exposing (Codec)\n\
         import Std.Log exposing (println)\n\n\
         type alias Row =\n    { id : String\n    , name : String\n    , count : Int\n    \
         , tags : List String\n    , note : Maybe String\n    , active : Bool\n    }\n\n\
         rowCodec : Codec Row\nrowCodec =\n    Codec.object Row\n\
         \x20       |> Codec.field \"id\" .id Codec.string\n\
         \x20       |> Codec.field \"name\" .name Codec.string\n\
         \x20       |> Codec.field \"count\" .count Codec.int\n\
         \x20       |> Codec.field \"tags\" .tags (Codec.list Codec.string)\n\
         \x20       |> Codec.field \"note\" .note (Codec.maybe Codec.string)\n\
         \x20       |> Codec.field \"active\" .active Codec.bool\n\
         \x20       |> Codec.buildObject\n\n",
    );
    let kinds = [
        ("List Row", "(Codec.list rowCodec)", "[]"),
        ("Maybe Row", "(Codec.maybe rowCodec)", "Nothing"),
        ("String", "Codec.string", "\"\""),
        ("Int", "Codec.int", "0"),
        ("Bool", "Codec.bool", "False"),
        ("List String", "(Codec.list Codec.string)", "[]"),
    ];
    for c in 0..codecs {
        let field = |i: usize| format!("w{c}f{i:02}");
        s.push_str(&format!("type alias Wide{c} =\n"));
        for i in 0..FIELDS {
            let lead = if i == 0 { "    { " } else { "    , " };
            s.push_str(&format!(
                "{lead}{} : {}\n",
                field(i),
                kinds[i % kinds.len()].0
            ));
        }
        s.push_str("    }\n\n");
        s.push_str(&format!(
            "wide{c}Codec : Codec Wide{c}\nwide{c}Codec =\n    Codec.object Wide{c}\n"
        ));
        for i in 0..FIELDS {
            let f = field(i);
            s.push_str(&format!(
                "        |> Codec.field \"{f}\" .{f} {}\n",
                kinds[i % kinds.len()].1
            ));
        }
        s.push_str("        |> Codec.buildObject\n\n");
        s.push_str(&format!("wide{c}Blank : Wide{c}\nwide{c}Blank =\n"));
        for i in 0..FIELDS {
            let lead = if i == 0 { "    { " } else { "    , " };
            s.push_str(&format!(
                "{lead}{} = {}\n",
                field(i),
                kinds[i % kinds.len()].2
            ));
        }
        s.push_str("    }\n\n");
    }
    s.push_str("main =\n    println\n        (String.join \",\"\n            [ ");
    let encs: Vec<String> = (0..codecs)
        .map(|c| format!("Codec.toJson wide{c}Codec wide{c}Blank"))
        .collect();
    s.push_str(&encs.join("\n            , "));
    s.push_str("\n            ]\n        )\n");
    s
}

fn scratch(tag: &str, main: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-largemod-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"largemod\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    dir
}

/// Peak live heap bytes while lowering `codecs` wide codecs, above the live
/// bytes at the start of the run. Returns the emitted Go too.
fn peak_for(codecs: usize) -> (usize, String) {
    let project = scratch(&format!("k{codecs}"), &program(codecs));
    let base = LIVE.load(Ordering::Relaxed);
    PEAK.store(base, Ordering::Relaxed);
    let go = emit_example_source(&repo_root(), &project)
        .unwrap_or_else(|e| panic!("emit failed for {codecs} codec(s): {e}"));
    let peak = PEAK.load(Ordering::Relaxed) - base;
    let _ = std::fs::remove_dir_all(&project);
    (peak, go)
}

#[test]
fn wide_codec_pipelines_cost_linear_memory() {
    // Warm process-wide caches (runtime arity scan, etc.) so they are not
    // charged to either measured run.
    let _ = peak_for(1);
    let (one, go_one) = peak_for(1);
    let (many, go_many) = peak_for(K);
    assert!(
        go_one.contains("wide0Codec") || go_one.contains("Wide0"),
        "codec lowered"
    );
    assert!(
        go_many.contains(&format!("Wide{}", K - 1)),
        "every codec lowered"
    );
    let extra = many.saturating_sub(one);
    let per_codec = extra / (K - 1);
    eprintln!(
        "peak live heap: 1 codec {} MB, {K} codecs {} MB, per added codec {} KB",
        one >> 20,
        many >> 20,
        per_codec >> 10
    );
    // Measured: 36 MB per added codec before the fix, 0.6 MB after. The
    // budget sits 10x above the fixed figure and 6x below the unfixed one.
    assert!(
        per_codec < 6 << 20,
        "each added {FIELDS}-field codec costs {} MB of peak live heap \
         (budget 6 MB): the typed tables or the go_ty memo are storing \
         expanded types again (see ty::tytable)",
        per_codec >> 20
    );
}
