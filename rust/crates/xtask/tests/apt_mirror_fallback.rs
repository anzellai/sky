//! The Playwright apt-mirror fallback really moves apt to another mirror.
//!
//! Release run 40338067557 (gate-web-1): the runner's apt sources pointed at
//! `mirror+file:/etc/apt/apt-mirrors.txt`, and the slow azure mirror lived only
//! in that list. `scripts/ci/playwright-deps.sh` rewrote `ubuntu.sources` and
//! `sources.list`, which held no archive URL, so all three attempts downloaded
//! from azure while the log said "apt mirror: <other>", and the job ran out of
//! time. These tests run the real rewrite (`scripts/lib/apt-mirror.sh`) on the
//! three shapes and check the script rewrites the mirrorlist too.

use std::path::PathBuf;
use std::process::Command;

fn repo() -> PathBuf {
    let mut d = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !d.join("scripts/lib/apt-mirror.sh").is_file() {
        assert!(d.pop(), "no repo root above the xtask crate");
    }
    d
}

/// `apt_mirror_rewrite <file> <url>` on `content`, through bash.
fn rewrite(content: &str, url: &str) -> String {
    let dir = std::env::temp_dir().join(format!(
        "xtask-apt-mirror-{}-{}",
        std::process::id(),
        N.fetch_add(1, std::sync::atomic::Ordering::SeqCst)
    ));
    std::fs::create_dir_all(&dir).unwrap();
    let file = dir.join("source");
    std::fs::write(&file, content).unwrap();
    let lib = repo().join("scripts/lib/apt-mirror.sh");
    let out = Command::new("bash")
        .arg("-c")
        .arg("source \"$1\"; apt_mirror_rewrite \"$2\" \"$3\"")
        .arg("_")
        .arg(&lib)
        .arg(&file)
        .arg(url)
        .output()
        .expect("run bash");
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8(out.stdout).unwrap()
}

static N: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);

const NEXT: &str = "http://archive.ubuntu.com/ubuntu/";

#[test]
fn the_mirrorlist_the_runner_points_at_is_rewritten() {
    let list = "http://azure.archive.ubuntu.com/ubuntu/\tpriority:1\n\
                https://archive.ubuntu.com/ubuntu/\tpriority:2\n\
                https://security.ubuntu.com/ubuntu/\tpriority:3\n";
    let got = rewrite(list, NEXT);
    assert!(!got.contains("azure"), "{got}");
    assert!(got.contains(&format!("{NEXT}\tpriority:1")), "{got}");
    // security is not an archive mirror and stays.
    assert!(got.contains("https://security.ubuntu.com/ubuntu/"), "{got}");
}

#[test]
fn deb822_and_classic_sources_are_rewritten() {
    let deb822 = "Types: deb\nURIs: http://azure.archive.ubuntu.com/ubuntu/\nSuites: noble\n";
    assert!(rewrite(deb822, NEXT).contains(&format!("URIs: {NEXT}")));
    let classic = "deb http://azure.archive.ubuntu.com/ubuntu noble main\n";
    assert!(rewrite(classic, NEXT).contains(&format!("deb {NEXT} noble main")));
    // The third attempt moves off the second mirror too.
    let second = format!("URIs: {NEXT}\n");
    let kernel = "http://mirrors.edge.kernel.org/ubuntu/";
    assert!(rewrite(&second, kernel).contains(&format!("URIs: {kernel}")));
}

#[test]
fn the_script_rewrites_the_mirrorlist_and_checks_it_took() {
    let s = std::fs::read_to_string(repo().join("scripts/ci/playwright-deps.sh")).unwrap();
    assert!(
        s.contains("/etc/apt/apt-mirrors.txt"),
        "mirrorlist not rewritten"
    );
    assert!(
        s.contains("apt_mirror_rewrite"),
        "the tested rewrite is not used"
    );
    assert!(
        s.contains("no apt source names"),
        "a rewrite that changes nothing must fail loudly"
    );
}
