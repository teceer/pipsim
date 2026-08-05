{
  description = "pipsim — polyglot simulation monorepo";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    rust-overlay.url = "github:oxalica/rust-overlay";
  };

  outputs = { self, nixpkgs, flake-utils, rust-overlay }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ rust-overlay.overlays.default ];
        };
        rust = pkgs.rust-bin.stable.latest.default.override {
          extensions = [ "rust-src" "rust-analyzer" "clippy" "rustfmt" ];
          targets = [ "wasm32-unknown-unknown" ];
        };
      in {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            # languages
            rust
            go
            elixir
            erlang
            bun
            nodejs_22
            zig

            # contracts
            buf
            protobuf
            protoc-gen-go
            protoc-gen-connect-go

            # infrastructure
            terraform
            kubectl
            kubernetes-helm
            k3d
            tilt
            docker-compose

            # tooling
            cargo-nextest
            wasm-pack
            golangci-lint
            just
            jq
          ];

          shellHook = ''
            echo "pipsim — rust $(rustc --version | cut -d' ' -f2) · go $(go version | cut -d' ' -f3) · elixir $(elixir --version | tail -1 | cut -d' ' -f2) · zig $(zig version)"
            echo "start:  make dev   |   cluster:  make infra-up && tilt up"
          '';
        };
      });
}
