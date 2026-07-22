// Release builds bake the tag into the binary via `SKY_BUILD_VERSION` (set by
// the release workflow). `option_env!` reads it at compile time; this line makes
// cargo recompile when the value changes so the baked version stays in sync.
fn main() {
    println!("cargo:rerun-if-env-changed=SKY_BUILD_VERSION");
}
