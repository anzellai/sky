//! `sky-lsp` — thin binary wrapper. The transport + analysis engine both live in
//! the `sky_lsp` library (so `sky lsp` can run the server inline from the single
//! `sky` binary). This standalone binary just calls [`sky_lsp::run`].

fn main() {
    sky_lsp::run();
}
