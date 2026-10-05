{
  description = "F95 Tracker: personal tracker for F95Zone games";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin" # nixpkgs 26.11 dropped x86_64-darwin, so it cannot be listed here
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});

      mkPackage =
        pkgs:
        pkgs.buildGoModule {
          pname = "f95-tracker";
          version = "0.0.0";
          src = srcFor pkgs;

          # Refresh after any go.mod/go.sum change: `just vendor-hash`.
          vendorHash = "sha256-tPNFt/uWnPtgIx9zy5r8svWuDLux7WVBq1chofW9R6w=";

          env.CGO_ENABLED = 0;
          subPackages = [ "cmd/f95-tracker" ];
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${self.shortRev or self.dirtyShortRev or "dirty"}"
          ];

          nativeBuildInputs = [ pkgs.tailwindcss_4 ];
          # app.css is generated, gitignored, and go:embed'ed.
          preBuild = ''
            tailwindcss -i internal/web/static/input.css -o internal/web/static/app.css --minify
          '';

          # subPackages would restrict the default check to cmd/; test everything.
          checkPhase = ''
            runHook preCheck
            go test ./...
            runHook postCheck
          '';

          meta = {
            description = "Personal tracker for F95Zone games";
            mainProgram = "f95-tracker";
          };
        };

      # Only what the build and the generator checks need; no docs, CSV or worktrees.
      srcFor =
        pkgs:
        let
          fs = pkgs.lib.fileset;
        in
        fs.toSource {
          root = ./.;
          fileset = fs.difference (fs.unions [
            ./go.mod
            ./go.sum
            ./sqlc.yaml
            ./cmd
            ./internal
          ]) (fs.maybeMissing ./internal/web/static/app.css);
        };

      # A check that works on a writable copy of the source with the vendored deps.
      mkCheck =
        pkgs: package: name: tools: script:
        pkgs.stdenv.mkDerivation {
          name = "f95-tracker-check-${name}";
          src = srcFor pkgs;
          nativeBuildInputs = [ pkgs.go ] ++ tools;
          dontConfigure = true;
          buildPhase = ''
            runHook preBuild
            export HOME=$TMPDIR GOCACHE=$TMPDIR/go-cache GOFLAGS=-mod=vendor CGO_ENABLED=0 GOTOOLCHAIN=local
            ${script}
            runHook postBuild
          '';
          installPhase = "touch $out";
        };

      withVendor = package: ''
        cp -r ${package.goModules} vendor
        chmod -R u+w vendor
      '';
    in
    {
      packages = forAllSystems (
        system: pkgs: {
          default = mkPackage pkgs;
        }
      );

      nixosModules.default = import ./nix/module.nix self;

      devShells = forAllSystems (
        system: pkgs: {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              gopls
              templ
              sqlc
              goose
              air
              tailwindcss_4
              sqlite
              gh
              just
              go-tools
            ];
          };
        }
      );

      checks = forAllSystems (
        system: pkgs:
        let
          package = self.packages.${system}.default;
          check = mkCheck pkgs package;
        in
        {
          # Includes `go test ./...` (checkPhase) and the Tailwind build (preBuild).
          inherit package;

          vet = check "vet" [ ] ''
            ${withVendor package}
            go vet ./...
          '';

          staticcheck = check "staticcheck" [ pkgs.go-tools ] ''
            ${withVendor package}
            staticcheck ./...
          '';

          gofmt = check "gofmt" [ ] ''
            unformatted=$(gofmt -l .)
            if [ -n "$unformatted" ]; then
              echo "gofmt needed:" >&2
              echo "$unformatted" >&2
              exit 1
            fi
          '';

          templ-drift = check "templ-drift" [ pkgs.templ ] ''
            templ generate
            diff -r ${srcFor pkgs} . || { echo "templ output is stale: run 'just generate'" >&2; exit 1; }
          '';

          sqlc-drift = check "sqlc-drift" [ pkgs.sqlc ] ''
            sqlc generate
            diff -r ${srcFor pkgs} . || { echo "sqlc output is stale: run 'just generate'" >&2; exit 1; }
          '';

          tailwind = check "tailwind" [ pkgs.tailwindcss_4 ] ''
            tailwindcss -i internal/web/static/input.css -o internal/web/static/app.css --minify
            test -s internal/web/static/app.css
          '';
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          vm-test = import ./nix/vm-test.nix {
            inherit pkgs;
            module = self.nixosModules.default;
          };
        }
      );
    };
}
