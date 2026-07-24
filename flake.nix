{
  description = "Sky — a pure functional language compiling to Go (Rust compiler + Go runtime)";

  inputs = {
    # Latest stable channel — Rust stable + system libs come from here.
    nixpkgs.url          = "github:NixOS/nixpkgs/nixos-25.11";
    # Unstable — only used to pull a recent Go 1.26.x.
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url      = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, nixpkgs-unstable, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs         = import nixpkgs          { inherit system; };
        pkgsUnstable = import nixpkgs-unstable { inherit system; };

        # Go 1.26.x from unstable — the runtime target + FFI inspector build.
        goToolchain = pkgsUnstable.go_1_26;

        # Rust toolchain (primary compiler). nixpkgs stable rust; matches the
        # `channel = "stable"` in rust/rust-toolchain.toml. For exact-version
        # parity with CI, swap in oxalica/rust-overlay's fromRustupToolchainFile.
        rustTools = with pkgs; [
          rustc
          cargo
          rustfmt
          clippy
          rust-analyzer
        ];

        commonLibs = with pkgs; [ git ];

        # Legacy Haskell toolchain — only for building the archived compiler
        # under legacy-haskell-compiler/.
        ghc    = pkgs.haskell.compiler.ghc948;
        cabal  = pkgs.cabal-install;
        legacyTools = with pkgs; [ ghc cabal gmp libffi ncurses zlib ];
      in {
        # Primary dev shell — the Rust compiler.
        devShells.default = pkgs.mkShell {
          buildInputs = rustTools ++ [ goToolchain pkgs.pkg-config pkgs.gnumake pkgs.curl pkgs.jq ] ++ commonLibs;
          shellHook = ''
            export SKY_RUNTIME_DIR="$PWD/runtime-go"
            echo "sky (rust) dev shell"
            echo "  cargo $(cargo --version | awk '{print $2}')"
            echo "  rustc $(rustc --version | awk '{print $2}')"
            echo "  go    $(go version | awk '{print $3}')"
            echo
            echo "build:         ./scripts/build.sh   (cargo build --release -p sky → sky-out/sky)"
            echo "quick rebuild: ( cd rust && cargo build --release -p sky )"
          '';
        };

        # Legacy dev shell — build the archived Haskell compiler.
        devShells.legacy = pkgs.mkShell {
          buildInputs = legacyTools ++ [ goToolchain pkgs.pkg-config pkgs.gnumake pkgs.git ];
          shellHook = ''
            export SKY_RUNTIME_DIR="$PWD/runtime-go"
            echo "sky LEGACY (haskell) dev shell — build under legacy-haskell-compiler/"
            echo "  ghc   $(ghc   --numeric-version)"
            echo "  cabal $(cabal --numeric-version)"
          '';
        };

        # Primary package — the Rust compiler. The full repo is the source
        # because rust/crates/ffi/build.rs embeds the sibling trees
        # (sky-stdlib/, runtime-go/, tools/, templates/, sky-bundled/).
        packages.sky = pkgs.rustPlatform.buildRustPackage {
          pname = "sky";
          version = "0.18.0";
          src = ./.;
          cargoLock.lockFile = ./rust/Cargo.lock;
          buildAndTestSubdir = "rust";
          cargoBuildFlags = [ "-p" "sky" ];
          # xtask gates need the Go toolchain + network; run them in the
          # devShell / CI, not in the sandboxed package build.
          doCheck = false;
          nativeBuildInputs = [ goToolchain ];
          postInstall = ''
            mkdir -p $out/share/sky
            cp -r runtime-go $out/share/sky/runtime-go
            cp -r sky-stdlib $out/share/sky/sky-stdlib
            cp -r templates  $out/share/sky/templates 2>/dev/null || true
          '';
          meta = with pkgs.lib; {
            description = "Sky — pure functional language compiling to Go (Rust compiler)";
            platforms = platforms.unix;
          };
        };
        packages.default = self.packages.${system}.sky;

        apps.sky = {
          type = "app";
          program = "${self.packages.${system}.sky}/bin/sky";
        };
      });
}
