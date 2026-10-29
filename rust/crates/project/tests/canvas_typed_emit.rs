//! v0.29.0: the Std.Image.Canvas drawing steps go to their typed runtime
//! twins, so a step narrows nothing (doc 14 origin R5, the kernel return,
//! `lower.rs::kernel_call_lowered`; lever §5.3, a typed kernel entry point,
//! `kernel_typed_return_twin`). A Canvas, a Mask and a Font are opaque
//! handles (`type Std_Image_Canvas_Canvas = rt.SkyADT`); each twin returns
//! the handle ADT itself. runtime-go/rt/canvas_kernels_test.go checks each
//! twin against its erased kernel. Red before the twins: every wrapper
//! returned `rt.Coerce[Std_Image_Canvas_Canvas](...)`.

use project::emit_example_source;
use std::path::PathBuf;

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

fn emit(main: &str) -> String {
    let uniq = format!(
        "sky-canvas-emit-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"canvas-emit\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    let out = emit_example_source(&repo_root(), &dir);
    let _ = std::fs::remove_dir_all(&dir);
    out.unwrap_or_else(|e| panic!("emit failed: {e}"))
}

/// The emitted Go function `name`, from its `func` line to the next top-level
/// declaration.
fn func_body<'a>(src: &'a str, name: &str) -> &'a str {
    let head = format!("func {name}(");
    let start = src
        .find(&head)
        .unwrap_or_else(|| panic!("{name} is emitted:\n{src}"));
    let rest = &src[start..];
    let end = rest[1..]
        .find("\nfunc ")
        .or_else(|| rest[1..].find("\nvar "))
        .map(|i| i + 1)
        .unwrap_or(rest.len());
    &rest[..end]
}

const DRAWING: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Task as Task\n\
import Std.Image as Image\n\
import Std.Image.Canvas as Canvas exposing (Paint(..))\n\
import Std.Log exposing (println)\n\n\n\
black : Canvas.Rgba\n\
black =\n    Canvas.rgba 0 0 0 255\n\n\n\
drawing : String -> Canvas.Canvas\n\
drawing photo =\n    \
Canvas.blank 40 30 black\n        \
|> Canvas.rect 0 0 5 5 (Fill black)\n        \
|> Canvas.roundedRect 0 0 9 9 2 (Stroke 1 black)\n        \
|> Canvas.ellipse 0 0 9 9 (Fill black)\n        \
|> Canvas.arc 5 5 4 0.0 90.0 (Fill black)\n        \
|> Canvas.line 0 0 9 9 1 black\n        \
|> Canvas.paste 0 0 (Just (Canvas.roundedMask 4 4 1)) photo\n        \
|> Canvas.paste 0 0 (Just (Canvas.alphaMask photo)) photo\n        \
|> Canvas.pasteCanvas 0 0 (Just (Canvas.ellipseMask 4 4)) (Canvas.fromImage photo)\n        \
|> Canvas.blur 1.0\n        \
|> Canvas.resize Canvas.Lanczos 20 15\n        \
|> Canvas.text { font = Canvas.defaultFont, size = 8.0, colour = black } 0 0 \"a\"\n\n\n\
main =\n    \
case Task.run (Canvas.encode Image.Png (drawing \"\")) of\n        \
Ok _ ->\n            \
println \"ok\"\n\n        \
Err _ ->\n            \
println \"err\"\n";

#[test]
fn canvas_steps_use_their_typed_twins_and_narrow_nothing() {
    let src = emit(DRAWING);
    for (f, twin) in [
        ("Std_Image_Canvas_blank", "rt.Image_canvasBlankT("),
        ("Std_Image_Canvas_rect", "rt.Image_canvasRectT("),
        (
            "Std_Image_Canvas_roundedRect",
            "rt.Image_canvasRoundedRectT(",
        ),
        ("Std_Image_Canvas_ellipse", "rt.Image_canvasEllipseT("),
        ("Std_Image_Canvas_arc", "rt.Image_canvasArcT("),
        ("Std_Image_Canvas_line", "rt.Image_canvasLineT("),
        ("Std_Image_Canvas_paste", "rt.Image_canvasPasteT("),
        (
            "Std_Image_Canvas_pasteCanvas",
            "rt.Image_canvasPasteCanvasT(",
        ),
        ("Std_Image_Canvas_resize", "rt.Image_canvasResizeT("),
        ("Std_Image_Canvas_text", "rt.Image_canvasTextT("),
    ] {
        let body = func_body(&src, f);
        assert!(body.contains(twin), "{f} calls {twin}:\n{body}");
        assert!(!body.contains("rt.Coerce["), "{f} narrows nothing:\n{body}");
    }
    // The point-free kernels (`fromImage = Ffi.kernel ...`) are called at
    // the use site.
    for twin in [
        "rt.Image_canvasDefaultFontT(",
        "rt.Image_canvasFromImageT(",
        "rt.Image_canvasBlurT(",
        "rt.Image_canvasRoundedMaskT(",
        "rt.Image_canvasEllipseMaskT(",
        "rt.Image_canvasAlphaMaskT(",
    ] {
        assert!(src.contains(twin), "the program calls {twin}");
    }
    // No handle is narrowed anywhere. (A `Paint` payload read is the stdlib
    // ADT floor, R6, and is not a handle.)
    for handle in [
        "rt.Coerce[Std_Image_Canvas_Canvas]",
        "rt.Coerce[Std_Image_Canvas_Mask]",
        "rt.Coerce[Std_Image_Canvas_Font]",
    ] {
        assert!(!src.contains(handle), "{handle} is emitted:\n{src}");
    }
}
