{
  description = "Sky — a pure functional language compiling to Go (Haskell compiler + Go runtime)";

  inputs = {
    # Latest stable channel — GHC 9.4.8 + system libs come from here.
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

        # Haskell toolchain — pinned to GHC 9.4.8.
        ghc    = pkgs.haskell.compiler.ghc948;
        cabal  = pkgs.cabal-install;
        hsPkgs = pkgs.haskell.packages.ghc948;

        # Go 1.26.x from unstable.
        goToolchain = pkgsUnstable.go_1_26;

        commonLibs = with pkgs; [ gmp libffi ncurses zlib git ];

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
            echo "quick rebuild:       cabal install exe:sky --overwrite-policy=always --install-method=copy --installdir=sky-out"
          '';
        };

        packages.sky = pkgs.stdenv.mkDerivation rec {
          pname = "sky";
          version = "0.9.0";
          src = ./.;
          nativeBuildInputs = [ ghc cabal ] ++ commonLibs;
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

        apps.sky = {
          type = "app";
          program = "${self.packages.${system}.sky}/bin/sky";
        };
      });
}
