# Sky on stable Nix. No flakes are needed.
#
#   nix-build                         # builds Sky -> ./result/bin/sky (+ sky-ffi-inspect)
#   nix-shell -A shell                # the Rust compiler dev shell
#
# This one file holds every piece, and `flake.nix` only re-exports them:
#
#   package    a callPackage-able function that builds Sky
#   devShell   a callPackage-able function for the dev shell
#   overlay    final: prev: { sky = final.callPackage package { }; }
#
# Use the overlay or the functions to build Sky against your own nixpkgs:
#
#   let sky = import ./path/to/sky { };
#   in import <nixpkgs> { overlays = [ sky.overlay ]; }
#
# `package` takes a `go_1_26` argument (the Go that builds sky-ffi-inspect and
# that `sky` puts on PATH as a fallback). A nixpkgs without `go_1_26` must
# supply one: `callPackage sky.package { go_1_26 = <a Go 1.26>; }`.
#
# Called with no arguments, it builds against the nixpkgs pins in `flake.lock`
# (the one pin source for both entry points): `nixpkgs` for Rust and the
# system libraries, `nixpkgs-unstable` for Go 1.26.
#
# The version is `[workspace.package] version` in rust/Cargo.toml, the one
# version source (`sky --version` prints it). `gitRev` adds the commit:
# `sky v0.27.5 (1a2b3c4)`. The flake passes its short revision; this entry
# has no git information, so a `nix-build` reports the plain version.
let
  lock = builtins.fromJSON (builtins.readFile ./flake.lock);

  # A GitHub input from flake.lock, fetched by its locked rev and NAR hash.
  fetchLocked =
    name:
    let
      l = lock.nodes.${name}.locked;
    in
    builtins.fetchTarball {
      url = "https://github.com/${l.owner}/${l.repo}/archive/${l.rev}.tar.gz";
      sha256 = l.narHash;
    };

  # Names that never enter the build source, at any depth. Hidden files and
  # directories are dropped as a class, the same rule rust/crates/ffi/build.rs
  # applies to the embed (a runtime secret such as `.sky/console-token` must
  # never reach the store or the binary). The rest are build outputs.
  isExcludedName =
    name:
    builtins.substring 0 1 name == "."
    || builtins.elem name [
      "target"
      "sky-out"
      "node_modules"
      "result"
    ];

  # A filtered copy of the repo: only the trees the build reads, so the
  # examples, docs, apps and the legacy compiler stay out of the store.
  # rust/crates/ffi/build.rs embeds sky-stdlib/, runtime-go/,
  # tools/sky-ffi-inspect/, templates/ and sky-bundled/; rust/crates/sky/build.rs
  # embeds docs/migration/v0.27.md; rust/crates/sky/src includes files from
  # runtime-go/rt/ and sky-stdlib/.
  sourceRoots = [
    "rust"
    "sky-stdlib"
    "runtime-go"
    "tools/sky-ffi-inspect"
    "templates"
    "sky-bundled"
    "docs/migration/v0.27.md"
  ];

  filteredSource =
    lib: roots:
    let
      root = toString ./.;
    in
    lib.cleanSourceWith {
      name = "sky-source";
      src = ./.;
      filter =
        path: type:
        let
          rel = lib.removePrefix (root + "/") (toString path);
          base = baseNameOf (toString path);
          # Keep a root, anything under a root, and the directories on the
          # way down to a root.
          inRoots = lib.any (r: rel == r || lib.hasPrefix (r + "/") rel || lib.hasPrefix (rel + "/") r) roots;
        in
        inRoots
        && !(isExcludedName base)
        # The committed prebuilt inspector binary is an output, not a source.
        && !(base == "sky-ffi-inspect" && type != "directory");
    };

  # callPackage-able: the Sky compiler, with sky-ffi-inspect beside it.
  package =
    {
      lib,
      rustPlatform,
      buildGoModule,
      go_1_26,
      makeWrapper,
      # The commit `sky --version` names after the version (`SKY_GIT_REV`,
      # read by rust/crates/sky/build.rs). The flake passes its short
      # revision; empty prints the plain version.
      gitRev ? "",
    }:
    let
      version = (lib.importTOML ./rust/Cargo.toml).workspace.package.version;

      sky-ffi-inspect = (buildGoModule.override { go = go_1_26; }) {
        pname = "sky-ffi-inspect";
        inherit version;
        src = lib.cleanSourceWith {
          name = "sky-ffi-inspect-source";
          src = ./tools/sky-ffi-inspect;
          filter =
            path: type:
            let
              base = baseNameOf (toString path);
            in
            !(isExcludedName base) && base != "sky-ffi-inspect";
        };
        vendorHash = "sha256-yFLJf4uEsWapuHLhXOpFqhS/MKf4cKblvSoV24tve1A=";
        env.CGO_ENABLED = "0";
        ldflags = [
          "-s"
          "-w"
        ];
        # The inspector's tests run in the release workflow's `gate-race` job
        # ("tools Go tests"); they drive a real `go` over real packages.
        doCheck = false;
        meta.mainProgram = "sky-ffi-inspect";
      };
    in
    rustPlatform.buildRustPackage {
      pname = "sky";
      inherit version;

      src = filteredSource lib sourceRoots;

      cargoLock.lockFile = ./rust/Cargo.lock;
      cargoRoot = "rust";
      buildAndTestSubdir = "rust";
      cargoBuildFlags = [
        "-p"
        "sky"
      ];

      # Set even when empty: build.rs then never asks git (the sandbox has
      # no .git anyway).
      env.SKY_GIT_REV = gitRev;

      # The xtask gates and the crate tests need Go, network and PostgreSQL;
      # they run in the dev shell and in CI, not in the sandboxed build.
      doCheck = false;

      nativeBuildInputs = [ makeWrapper ];

      # The binary embeds sky-stdlib/, runtime-go/, templates/, sky-bundled/
      # and the inspector source (rust/crates/ffi/src/assets.rs), so nothing
      # else is installed beside it. `sky` builds programs with `go`: a Go on
      # the user's PATH wins, and this Go is the fallback.
      postInstall = ''
        ln -s ${sky-ffi-inspect}/bin/sky-ffi-inspect $out/bin/sky-ffi-inspect
        wrapProgram $out/bin/sky --suffix PATH : ${go_1_26}/bin
      '';

      passthru = {
        inherit sky-ffi-inspect gitRev;
      };

      meta = {
        description = "Sky: a pure functional language compiling to Go (Rust compiler)";
        homepage = "https://github.com/anzellai/sky";
        license = lib.licenses.asl20;
        mainProgram = "sky";
        platforms = lib.platforms.unix;
      };
    };

  # callPackage-able: the dev shell for the Rust compiler.
  devShell =
    {
      mkShell,
      rustc,
      cargo,
      rustfmt,
      clippy,
      rust-analyzer,
      go_1_26,
      pkg-config,
      gnumake,
      curl,
      jq,
      git,
    }:
    mkShell {
      # stdenv's setup.sh runs `dumpVars` when the shell is entered, which
      # writes EVERY exported variable as `declare -x NAME=value` to
      # `$NIX_BUILD_TOP/env-vars`. For `nix-shell` that directory is the
      # caller's TMPDIR, so entering the dev shell copied the developer's whole
      # environment, secrets included, into a plain file in (often shared)
      # /tmp. `noDumpEnvVars` is the stdenv switch that turns the dump off;
      # `nix develop` reads the same derivation. Test:
      # scripts/ci/nix-dev-shell-env-check.sh.
      noDumpEnvVars = true;
      packages = [
        rustc
        cargo
        rustfmt
        clippy
        rust-analyzer
        go_1_26
        pkg-config
        gnumake
        curl
        jq
        git
      ];
      shellHook = ''
        export SKY_RUNTIME_DIR="$PWD/runtime-go"
        echo "sky (rust) dev shell"
        echo "  cargo $(cargo --version | awk '{print $2}')"
        echo "  rustc $(rustc --version | awk '{print $2}')"
        echo "  go    $(go version | awk '{print $3}')"
        echo
        echo "build:         ./scripts/build.sh   (cargo build --release -p sky -> sky-out/sky)"
        echo "quick rebuild: ( cd rust && cargo build --release -p sky )"
      '';
    };

  overlay = final: prev: { sky = final.callPackage package { }; };
in
{
  system ? builtins.currentSystem,
  nixpkgs ? fetchLocked "nixpkgs",
  nixpkgs-unstable ? fetchLocked "nixpkgs-unstable",
}:
let
  # Go 1.26 from the nixpkgs-unstable pin (the Go pin source the flake has
  # always used); the stable pin supplies Rust and everything else.
  goOverlay = final: prev: {
    go_1_26 = (import nixpkgs-unstable { inherit system; }).go_1_26;
  };

  pkgs = import nixpkgs {
    inherit system;
    overlays = [
      goOverlay
      overlay
    ];
  };
in
# `nix-build` builds this derivation; the attributes below ride along on it.
pkgs.sky
// {
  inherit
    package
    devShell
    overlay
    pkgs
    ;
  sky = pkgs.sky;
  shell = pkgs.callPackage devShell { };
}
