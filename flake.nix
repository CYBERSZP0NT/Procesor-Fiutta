{
  description = "szmelc-router: one static Go binary in front of all your free LLM capacity (OpenAI-compatible, agentic task router, TUI, FreeLLMAPI live)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        version = "1.0.0-rc1-${self.shortRev or self.dirtyShortRev or "dirty"}";

        szmelc-router = pkgs.buildGoModule {
          pname = "szmelc-router";
          inherit version;
          src = self;

          # Hash of `go mod vendor` for go.sum; update it when dependencies
          # change (nix build prints the right one on a mismatch).
          vendorHash = "sha256-OFlGk8sk3AiDGLmKTJtAn3P/0zx2KiV40ThmI4b/zPE=";

          subPackages = [ "cmd/szmelc-router" ];
          env.CGO_ENABLED = 0;
          ldflags = [
            "-s" "-w"
            "-X main.version=${version}"
            "-X main.commit=${self.shortRev or "dirty"}"
          ];

          nativeBuildInputs = [ pkgs.installShellFiles ];

          # Tests use loopback httptest servers and temp dirs only.
          preCheck = ''
            export HOME=$TMPDIR
          '';

          postInstall = ''
            ln -s szmelc-router $out/bin/freellm-router
            installShellCompletion --cmd szmelc-router \
              --bash <($out/bin/szmelc-router completion bash) \
              --zsh  <($out/bin/szmelc-router completion zsh) \
              --fish <($out/bin/szmelc-router completion fish)
            install -Dm644 LICENSE -t $out/share/licenses/szmelc-router
            install -Dm644 README.md CHANGELOG.md MODELS.md FREE-MODELS.md .env.example -t $out/share/doc/szmelc-router
            install -Dm644 docs/*.md -t $out/share/doc/szmelc-router/docs
          '';

          meta = with pkgs.lib; {
            description = "OpenAI-compatible router over free LLM tiers, keyless endpoints, Ollama and sidecars, with an agentic task router and FreeLLMAPI live routing";
            homepage = "https://github.com/CYBERSZP0NT/szmelc-router";
            license = licenses.mit;
            mainProgram = "szmelc-router";
            platforms = platforms.unix;
          };
        };
      in
      {
        packages.default = szmelc-router;
        packages.szmelc-router = szmelc-router;

        apps.default = flake-utils.lib.mkApp { drv = szmelc-router; };

        devShells.default = pkgs.mkShell {
          inputsFrom = [ szmelc-router ];
          packages = with pkgs; [ go gopls gotools golangci-lint gnumake ];
        };
      });
}
