{
  description = "shoulder: a TUI that watches coding agents work";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = "0.3.1";
    in
    {
      packages = forAllSystems (pkgs: {
        shoulder = pkgs.callPackage (
          {
            lib,
            buildGoModule,
            go_1_26,
            git,
          }:
          (buildGoModule.override { go = go_1_26; }) {
            pname = "shoulder";
            inherit version;

            # Only the Go sources, so edits to the site or demo don't rebuild.
            src = lib.fileset.toSource {
              root = ./.;
              fileset = lib.fileset.unions [
                ./go.mod
                ./go.sum
                ./main.go
                ./internal
              ];
            };

            vendorHash = "sha256-YXtjuXdo7kj7VSxZMm2d7P3MizftXcCJXCIb7ZCcX9M=";

            env.CGO_ENABLED = 0;
            ldflags = [
              "-s"
              "-w"
              "-X main.version=v${version}"
            ];

            # The repo tests run git.
            nativeCheckInputs = [ git ];

            meta = {
              description = "Watch over your agents' shoulder";
              homepage = "https://github.com/bjarneo/shoulder";
              license = lib.licenses.mit;
              mainProgram = "shoulder";
            };
          }
        ) { };
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.shoulder;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          name = "shoulder";
          packages = with pkgs; [
            go_1_26
            gopls
            gotools
            golangci-lint
            git
          ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-rfc-style);
    };
}
