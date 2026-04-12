{
  description = "Sky — a pure functional language compiling to Go (Haskell compiler + Go runtime)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        # Pin the toolchain versions Sky is tested against.
        ghc     = pkgs.haskell.compiler.ghc948;
        cabal   = pkgs.cabal-install;
        goToolchain = pkgs.go_1_23;  # nixpkgs stable lags latest; 1.23+ works

        # Haskell package set with our pinned GHC.
        hsPkgs = pkgs.haskell.packages.ghc948;

        # System libs the compiler's transitive cabal deps link against.
        commonLibs = with pkgs; [
          gmp
          libffi
          ncurses
          zlib
          git
        ];

        # Build the compiler using haskell-nix-style cabal2nix — but we keep it
        # simple: developerShell gives you cabal + ghc + go; you run
        # `cabal install exe:sky` inside.
        devTools = [
          ghc
          cabal
          goToolchain
          pkgs.pkg-config
          pkgs.gnumake
          pkgs.curl
          pkgs.jq
        ] ++ commonLibs;

      in {
        # ────────────────────────────────────────────────────────────
        # `nix develop` — reproducible shell with GHC 9.4.8 + Go 1.23 +
        # every system lib the Sky compiler needs.
        # ────────────────────────────────────────────────────────────
        devShells.default = pkgs.mkShell {
          buildInputs = devTools;

          shellHook = ''
            export SKY_RUNTIME_DIR="$PWD/runtime-go"
            echo "sky dev shell"
            echo "  ghc   $(ghc   --numeric-version)"
            echo "  cabal $(cabal --numeric-version)"
            echo "  go    $(go version | awk '{print $3}')"
            echo
            echo "build locally with:  ./scripts/build.sh"
            echo "quick rebuild:        cabal install exe:sky --overwrite-policy=always --install-method=copy --installdir=sky-out"
          '';
        };

        # ────────────────────────────────────────────────────────────
        # `nix build` — produce the sky compiler binary in ./result/bin/sky.
        # Useful for CI or anyone who wants a non-cabal build.
        # ────────────────────────────────────────────────────────────
        packages.sky = pkgs.stdenv.mkDerivation rec {
          pname = "sky";
          version = "1.0.0";
          src = ./.;

          nativeBuildInputs = [ ghc cabal ] ++ commonLibs;

          # Offline cabal build — relies on cabal's local build plan.
          buildPhase = ''
            export HOME=$TMPDIR
            cabal update
            cabal install exe:sky \
              --overwrite-policy=always \
              --install-method=copy \
              --installdir=$TMPDIR/out
          '';

          installPhase = ''
            mkdir -p $out/bin $out/share/sky
            cp $TMPDIR/out/sky $out/bin/sky
            cp -r runtime-go $out/share/sky/runtime-go
            cp -r templates  $out/share/sky/templates 2>/dev/null || true
          '';

          meta = with pkgs.lib; {
            description = "Sky — pure functional language compiling to Go";
            platforms = platforms.unix;
          };
        };

        packages.default = self.packages.${system}.sky;

        # `nix run .#sky -- build src/Main.sky` for a one-off run.
        apps.sky = {
          type = "app";
          program = "${self.packages.${system}.sky}/bin/sky";
        };
      });
}
