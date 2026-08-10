//! Regression: FFI inspection could not handle a cgo-requiring package, so
//! `11-fyne-stopwatch` was verified on NO platform at all.
//!
//! `run_inspector` pinned `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` for every
//! package, which is what makes a macOS dev and a Linux CI read identical
//! surface bytes. But Fyne genuinely requires cgo — glfw → OpenGL → the
//! platform's native windowing — and under `CGO_ENABLED=0` it cannot be
//! type-checked at all. `sky install` therefore could not generate its surface,
//! Linux CI skipped it as a GUI example, and macOS could not build it
//! (discussions #50).
//!
//! Note the shape of the real failure, which is why the fallback is not gated on
//! a keyword: fyne did not report anything about cgo. It reported a plain type
//! error inside its own driver —
//!
//! ```text
//! app/app_gl.go:13:26: cannot use glfw.NewGLDriver() … *gLDriver does not
//! implement fyne.Driver (missing method DoubleTapDelay)
//! ```
//!
//! — because `CGO_ENABLED=0` selects a stub file set whose type no longer
//! satisfies the interface. Any whitelist over that surface would be permanently
//! incomplete, so ANY pinned-target failure earns one host+cgo retry.
//!
//! These tests are toolchain-free: they pin the *contract* (what is attempted,
//! in what order, and what the caller is told) using a stub "inspector" that
//! reports the environment it was given. Whether fyne itself builds is the
//! example sweep's job — `11-fyne-stopwatch` is no longer marked `blocked`
//! there.

use std::path::{Path, PathBuf};

/// Write an executable stub that stands in for `sky-ffi-inspect`. It emits the
/// canned `stdout` only when the environment matches `want_cgo`, and otherwise
/// fails the way a package that cannot be type-checked under the pin does.
fn stub_inspector(dir: &Path, name: &str, script: &str) -> PathBuf {
    let path = dir.join(name);
    std::fs::write(&path, script).unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
    path
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-ffi-fallback-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

const PKG_JSON: &str = r#"{"pkg":"example.com/p","name":"p","functions":[]}"#;

/// A package that only type-checks with cgo on the host: the stub fails when it
/// sees the pin, and succeeds when it sees `CGO_ENABLED=1` with no GOOS pin.
/// This is the fyne case, and it must now produce a surface.
#[test]
#[cfg(unix)]
fn a_package_that_fails_the_pin_is_retried_on_the_host_with_cgo() {
    let dir = scratch("cgo-only");
    let bin = stub_inspector(
        &dir,
        "inspect.sh",
        &format!(
            "#!/bin/sh\n\
             if [ \"$CGO_ENABLED\" = \"1\" ] && [ -z \"$GOOS\" ]; then\n\
             \techo '{PKG_JSON}'\n\
             else\n\
             \techo 'app_gl.go:13:26: cannot use glfw.NewGLDriver() … does not implement fyne.Driver' 1>&2\n\
             fi\n"
        ),
    );

    let (infos, note) = ffi::run_inspector_reporting(&bin, &dir, &["example.com/p".into()])
        .expect("a package that fails the pin must fall back to the host, not give up");
    assert_eq!(infos.len(), 1);
    assert_eq!(infos[0].pkg, "example.com/p");

    let note = note.expect("the host+cgo fallback must report its provenance");
    assert!(
        note.contains("example.com/p") && note.contains("not portable"),
        "the note must name the package and warn the surface is host-specific: {note}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// The guarantee that must NOT be traded away: a package that works under the
/// pin is inspected under the pin, and never silently on the host. If the stub
/// sees anything but the pin it fails, so a stray fallback would be caught.
#[test]
#[cfg(unix)]
fn a_package_that_works_under_the_pin_never_touches_the_fallback() {
    let dir = scratch("pinned");
    let bin = stub_inspector(
        &dir,
        "inspect.sh",
        &format!(
            "#!/bin/sh\n\
             if [ \"$GOOS\" = \"linux\" ] && [ \"$GOARCH\" = \"amd64\" ] && [ \"$CGO_ENABLED\" = \"0\" ]; then\n\
             \techo '{PKG_JSON}'\n\
             else\n\
             \techo 'inspected off-pin' 1>&2\n\
             fi\n"
        ),
    );

    let (infos, note) = ffi::run_inspector_reporting(&bin, &dir, &["example.com/p".into()])
        .expect("a pure-Go package must inspect under the pin");
    assert_eq!(infos.len(), 1);
    assert!(
        note.is_none(),
        "no fallback note may be produced for a package that works under the \
         pin — the reproducible surface is the default and must stay silent: {note:?}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// A genuinely broken package must still fail, and the report must carry BOTH
/// attempts so the pinned error (the one describing the default path) is not
/// swallowed by the retry.
#[test]
#[cfg(unix)]
fn a_package_that_fails_both_targets_reports_both_errors() {
    let dir = scratch("broken");
    let bin = stub_inspector(
        &dir,
        "inspect.sh",
        "#!/bin/sh\necho 'no required module provides package example.com/nope' 1>&2\n",
    );

    let err = ffi::run_inspector_reporting(&bin, &dir, &["example.com/nope".into()])
        .expect_err("a package that resolves nowhere must not be reported as inspected");
    assert!(
        err.contains("host+cgo fallback also failed"),
        "both attempts must be reported: {err}"
    );
    assert!(
        err.contains("no required module provides"),
        "the underlying cause must survive: {err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
