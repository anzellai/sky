{
  description = "Sky: a pure functional language compiling to Go (Rust compiler + Go runtime)";

  # A thin wrapper. Every piece (the package, the dev shell, the overlay) lives
  # in default.nix, which also builds on stable Nix with no flakes:
  # `nix-build` and `nix-shell -A shell`. This lock is the one pin source for
  # both entry points: default.nix reads it when it is called without inputs.
  inputs = {
    # Stable channel: Rust and the system libraries.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
    # Unstable: Go 1.26.
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-unstable,
    }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems f;

      sky =
        system:
        import ./default.nix {
          inherit system nixpkgs nixpkgs-unstable;
        };

      # The commit `sky --version` names after the version:
      # `sky v0.27.5 (1a2b3c4)`, `(1a2b3c4-dirty)` for a dirty tree. The
      # version itself is rust/Cargo.toml's, read by default.nix.
      gitRev = self.shortRev or self.dirtyShortRev or "";

      package = system: (sky system).sky.override { inherit gitRev; };
    in
    {
      # The overlay from default.nix: `sky = final.callPackage package { }`.
      # Its value does not depend on the system; one is named only to reach it.
      overlays.default = (sky "x86_64-linux").overlay;

      packages = forAllSystems (system: {
        sky = package system;
        default = package system;
      });

      devShells = forAllSystems (system: {
        default = (sky system).shell;
      });

      apps = forAllSystems (system: {
        sky = {
          type = "app";
          program = "${package system}/bin/sky";
        };
        default = self.apps.${system}.sky;
      });
    };
}
