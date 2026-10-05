# Dev tools come from the Nix dev shell (templ, sqlc, goose, tailwindcss, just).

default:
    @just --list

# Regenerate committed code: templ components and sqlc queries.
generate:
    templ generate
    sqlc generate

# Build Tailwind into internal/web/static/app.css (generated, gitignored).
assets:
    tailwindcss -i internal/web/static/input.css -o internal/web/static/app.css --minify

# Watch templ and Tailwind, and run the server. Needs F95_TRACKER_* env (see spec 5.1).
dev:
    #!/usr/bin/env bash
    set -euo pipefail
    trap 'kill 0' EXIT
    templ generate --watch &
    tailwindcss -i internal/web/static/input.css -o internal/web/static/app.css --watch &
    go run ./cmd/f95-tracker serve

test:
    go test ./...

# Fails when generated code is out of date.
check-drift: generate
    git diff --exit-code
