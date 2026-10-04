# Research: hosting on `jeiang/.dotfiles` infra (ticket #5)

Question: how does a new service get deployed on the user's NixOS infra, and what must this repo's flake and NixOS module provide to fit?

Source: fresh shallow clone of `jeiang/.dotfiles` (flake name `cornn-flaek`) at `main` = `cd8239bfe8e1d5fe7f0e86704470ceb5e84ee71c` (merge of PR #280, 2026-10-04). All paths below are relative to that repo. `AGENTS.md` there is the house rulebook and is cited often. No secret values were read or copied; only secret names and shard paths appear.

## 1. Flake layout and how external flakes are consumed

- `flake.nix` is inputs plus one line: `outputs = inputs: inputs.flake-parts.lib.mkFlake {inherit inputs;} (inputs.import-tree ./modules);`. Every file under `modules/` is a flake-parts module (AGENTS.md "Layout").
- `modules/configurations.nix` defines the option sets `nixos.modules.<name>` / `darwin.modules.<name>` (deferred modules) and `nixos.configurations.<host>.module`, which becomes `flake.nixosConfigurations` through `nixpkgs.lib.nixosSystem`.
- Features contribute to roles, not their own names: `nixos.modules.base` (all hosts), `nixos.modules.artemis`, `nixos.modules.legion` (all four Hetzner nodes), plus one kebab-case `nixos.modules.<service>` per Legion service (AGENTS.md "Layout").
- **External app flakes are consumed as a flake input plus its `nixosModules`**, imported inside the feature's `nixos.modules.<svc>`:
  - `modules/portfolio/default.nix`: `imports = [inputs.portfolio.nixosModules.default];` then `services.portfolio = { enable = true; host; port; stateDir; trustedProxies; siteUrl; adminPasswordHashFile = config.sops.secrets."...".path; }`.
  - `modules/garret/default.nix`: `imports = [inputs.garret.nixosModules.pusher inputs.garret.nixosModules.puller];` (module attrs named per role; a `watcher` module deliberately not imported on the cache host).
  - Other first-party app flakes (`flake.nix` inputs): `website`, `portfolio`, `bill-splitter`, `character-randomizer`, `markdown-table-live-editor`, `garret`, `ripper`. Pattern: `inputs.<name>.url = "github:jeiang/<name>"; inputs.<name>.inputs.nixpkgs.follows = "nixpkgs";` The `follows` is dropped only with a written reason (`garret`, `ripper`: "ripper's release CI pushes to garret against this input's own nixpkgs pin, and a different pin here would miss that cache"). The comment style is: one line saying why there is no `follows`.
- Consumers use the app's package from the input (`inputs.website.packages.${system}.default`, in `modules/edge/default.nix`) or the module's `package` default. Portfolio's own module (`joshua-noel/portfolio` `nix/module.nix`, HEAD `b71da27`) wraps the module so `package` defaults to that flake's package (its `flake.nix` line ~39-44: "Wrapped so the module's `package` default comes from this flake"). No overlays are used for app flakes.
- Rules relevant to a new module (AGENTS.md "Code rules"): prefer the nixpkgs NixOS module for a service, custom module only when none fits; define a shared literal (port, secret name, hostname) once; add a typed option only when reused across modules/hosts, tuned per host, or a module boundary; no comments except workaround/context.
- Reference module shape from `jeiang/garret` (`flake.nix`, HEAD `d30a50f`): `nixosModules = { pusher = import ./nix/pusher.nix self; ... }` and a `checks.<system>.module = pkgs.testers.runNixOSTest ...` booting the module (service users, file modes, sockets).

## 2. Hosts and which fits a small web service

AGENTS.md host table:

| Host | Kind | Role |
| --- | --- | --- |
| `artemis` | NixOS | Home headless gaming/streaming box, impermanent btrfs root, mesh-only, also Hermes, llm-server, media stack (Radarr/Sonarr/Seerr/qBittorrent), Factorio |
| `zakkart` | nix-darwin | Operator's MacBook |
| `legion-node1`..`4` | NixOS | Hetzner Cloud service nodes |

Legion placement (`modules/hermes/SERVERS.md` "Fleet map", `modules/hosts/legion/services.nix`):

- node1: Caddy edge, CrowdSec, Anubis, tinyauth. node2: NetBird server/relay/proxy, Pocket ID, Blocky DNS, portfolio. node3: monitoring (VictoriaMetrics/Logs, Grafana, Alertmanager). node4: garret, Actual Budget, atuin, hath, glance, Gatus.
- **Small stateful web apps live on node4 (Actual Budget on a Volume, atuin on the root disk) or node2 (portfolio).** `modules/portfolio/default.nix` says it avoids node1 because "legion-node1 has no restic credentials".
- Service placement is declarative through `legion.services.<name>` (`modules/hosts/legion/services.nix`). Fields: `node`, `module`, `stateful`, `units`, `ports.<role>`, `firewall = [{port; proto; scope = "public"|"private";}]`, `firewallPortRanges`, `publicHostnames`, `edge`, `volume = {name; mountpoint; sizeGiB; hcloudVolumeId;}`, `backupSet = [paths]`. `modules/hosts/legion/default.nix` derives from it: the per-node module imports (`moduleNamesFor`, import order list `legionModuleOrder` where unlisted modules sort last), firewall openings, Volume `fileSystems` (mounted by ext4 label = service key, `nofail`), `backups.jobs`, and the node-exporter systemd unit allowlist. A `stateful` entry must declare a `volume` or a `backupSet` (assert in `services.nix`).
- `scope = "private"` is documentation only; `enp7s0` and the NetBird interface are trusted. Public ports additionally need a rule in the shared Hetzner Cloud Firewall `legion`, provisioned outside the flake (AGENTS.md "Decisions that constrain changes").
- **Where each host fits the tracker:**
  - *legion-node4 (default recommendation)*: exactly the home of single-user, SQLite-backed apps. Closest precedent `modules/atuin.nix` (root-disk SQLite, `backupSet = [dataDir]`, hourly restic timer override, a `atuin-restore` oneshot that restores from restic when the DB is missing, `MemoryMax = "128M"`, static user because DynamicUser would move state to `/var/lib/private`) and `modules/actual-budget.nix` (Volume + `mountGuard`). Decision rule from AGENTS.md: a Volume is for state that "cannot suffer loss if the server fails"; state that tolerates losing the interval since last backup lives on the root disk with a `backupSet` job as its durability story.
  - *artemis*: viable for the app too, and the natural place for the downloader (section 9). It has no `legion.services` machinery; features go in `nixos.modules.artemis`; it is mesh-only, behind a home IP, with an impermanent root (extra persistence steps, section 8). There is no precedent in the repo of the edge Caddy proxying to artemis; every edge upstream is a Legion private IP (`modules/edge/default.nix`), though monitoring already scrapes artemis over its NetBird peer IP (`self.lib.netbirdPeers.artemis` in `modules/netbird-peers.nix`, used in `modules/monitoring/default.nix:155`).
  - [INFERENCE] Egress IP matters for scraping F95zone: Hetzner datacenter IPs vs a residential home IP. This is a ResearchF95Access question; if datacenter IPs get blocked, either the update checker also runs on artemis or the app runs wholly on artemis. Flag for the deployment grilling.

## 3. Exposing a service: Caddy edge (and the "netbird proxy" pattern)

There are two exposure mechanisms; the user said "netbird proxy", and both are documented here.

### 3a. Public/browser via the edge on node1 (house default)
- `modules/edge/default.nix` (598 lines) is one hand-written Caddyfile in Nix. Vhosts are literal blocks: e.g. `budget.jeiang.dev { ${logLine}${crowdsecLine}${appsecLine}reverse_proxy ${node4}:${port "actual-budget" "app"} }`, with `port = svc: key: toString legionServices.${svc}.ports.${key}` and node IPs from `self.lib.legionNodes.<node>.privateIPv4`. So a new vhost is an edit in `modules/edge/default.nix`, not module-system config from the app's own module.
- Hostnames are listed in `legion.services.caddy.publicHostnames` (asserted unique). TLS: Caddy holds a DNS-01 `*.jeiang.dev` wildcard (comments around the `speed`/`cache` blocks and `modules/gatus.nix` "Caddy's own *.jeiang.dev wildcard cert"); the Cloudflare token is `caddy/cloudflare-dns-token` in `modules/edge/secrets.yaml`.
- DNS (`dns/dnsconfig.js` lines 30-33): `A/AAAA "*"` -> node1, `CF_PROXY_ON`. So a new `<name>.jeiang.dev` resolves with no DNS edit unless it must be DNS-only (`cache`, `speed` have explicit grey-cloud records). `dnsconfig.js` is applied by CI on merge with full purge (`.github/workflows/dns.yml`).
- Standard lines on every vhost: `crowdsec` (and `appsec`, skipped for burst/streaming traffic) via `crowdsecLine`/`appsecLine`.
- Auth patterns at the edge: the app does its own OIDC (Pocket ID), or `forward_auth 127.0.0.1:${port "tinyauth" "app"} { uri /api/auth/caddy }` (tinyauth, `modules/tinyauth.nix`, logs in through Pocket ID; used by `glance.jeiang.dev` and `speed.jeiang.dev`). AGENTS.md: Anubis gates only static content sites; never in front of machine clients (OIDC, Actual sync, Prometheus).
- Monitoring registrations a new service is expected to get: Gatus endpoint in `modules/gatus.nix` (`ok "Name" "Services" "https://...`), a Prometheus blackbox target against the private backend address (never the public URL; AGENTS.md: "Probes through the edge would get the prober banned by CrowdSec"), in `modules/monitoring/default.nix` ~lines 250-300, and optionally a Glance entry (`modules/glance.nix`).

### 3b. NetBird reverse proxy (`proxy.jeiang.dev`) and mesh-only
- `modules/netbird-server/proxy.nix`: `netbird-proxy` on legion-node2, public, terminates its own TLS with a `security.acme` DNS-01 cert for `proxy.jeiang.dev` + `*.proxy.jeiang.dev` (Cloudflare token via sops template `netbird-proxy-cloudflare-dns.env`), `NB_PROXY_PRIVATE = "true"` (enables "NetBird-Only Access" services), CrowdSec app-level bouncer against the LAPI on node1, nftables bouncer, TCP/UDP 40000-45000 open for ad hoc L4 services. DNS: `A/AAAA proxy` and `*.proxy` -> node2.
- **The proxied services themselves are not declared in Nix.** They are created in the NetBird dashboard. Evidence: `modules/factorio/default.nix` comment: "Public at proxy.jeiang.dev:44197/udp through a netbird-proxy L4 service (NetBird dashboard, CrowdSec enforce) targeting artemis's UDP 34197 over the trusted mesh; nothing opens a port on the home network." The `netbird-invariants` check (`modules/netbird-invariants/`) only reads the management API and asserts every reverse-proxy service has CrowdSec mode `enforce`; AGENTS.md says "start new services in observe" (comment in `proxy.nix`) but the invariant requires `enforce` (weekly CI job `.github/workflows/netbird.yml`, `NETBIRD_READ_TOKEN`).
- This is the one route to expose a service that lives **behind the home IP on artemis** without an edge vhost: dashboard-defined HTTP or L4 service targeting the artemis peer. `qbittorrent.nix` keeps its ports inside the 40000-45000 band "so publishing inbound peers through the proxy later needs no port change".
- **Mesh-only (no edge vhost, no DNS record)** is also an established pattern: `modules/atuin.nix` ("Mesh only: every client is a NetBird peer and that interface is already trusted, so the server needs no firewall opening, no edge vhost and no DNS record"), `modules/media.nix` and `modules/qbittorrent.nix` on artemis (no firewall port opened; `netbird` interface is `trustedInterfaces`, set in `modules/netbird.nix`). Mesh names: `artemis.jeiang.vpn` (AGENTS.md "Access"; a bare `artemis` hits a wildcard and does not reach the host); Legion mesh names carry collision suffixes, so raw peer IPs from `modules/netbird-peers.nix` (`self.lib.netbirdPeers.<host>`) are used, with mesh CIDR `self.lib.netbirdMeshCidrv4/v6`.

## 4. Pocket ID deployment and OIDC client registration

- `modules/pocket-id/default.nix`: `legion.services.pocket-id` on legion-node2, Volume `legion-pocket-id` at `/mnt/pocket-id`, port 1411, `backupSet = [dataDir]`, public at `https://auth.jeiang.dev` (edge vhost `auth.jeiang.dev { ... reverse_proxy ${node2}:${port "pocket-id" "app"} }`). Uses nixpkgs `services.pocket-id`, `UI_CONFIG_DISABLED = true` (settings declared in Nix; the admin UI settings page is locked), SMTP creds through `services.pocket-id.credentials`, `sops` template `pocket-id.env` with `ENCRYPTION_KEY` and `STATIC_API_KEY`. `MemoryMax = "256M"`.
- **OIDC clients are registered manually in Pocket ID's admin UI (state in its database), not declaratively.** Evidence: no module in the repo declares a client; consumers just receive client id/secret. [INFERENCE from absence, plus `STATIC_API_KEY` being set with no consumer in the repo.] The pattern for consumers:
  - `modules/tinyauth.nix`: endpoints `https://auth.jeiang.dev/authorize`, `/api/oidc/token`, `/api/oidc/userinfo`; callback `https://tinyauth.jeiang.dev/api/oauth/callback/pocketid`; scopes `openid email profile groups`; client id and secret both in sops (`tinyauth/pocket-id-client-id`, `tinyauth/pocket-id-client-secret`), injected through a sops template env file with `restartUnits`.
  - `modules/monitoring/default.nix:395-410` (Grafana): `client_id` is hardcoded in Nix (a UUID, not secret), secret in sops (`grafana/oauth-client-secret`); role mapping from the `groups` claim (`monitoring_admin|editor|reader`).
  - `modules/garret/default.nix`: Pocket ID as an OIDC issuer, client id as audience (`pocketIdIssuer`).
- Pocket ID admin work therefore sits outside Nix: create the client, set the callback URL (`https://<host>/<callback>`), copy id+secret into the consumer's sops shard with `just sops-edit`. Group claims (`groups` scope) are the mechanism for authorization; user groups are referenced by UUID in `SIGNUP_DEFAULT_USER_GROUP_IDS`.

## 5. Secrets mechanism and runtime-mutable state

- **sops-nix** on every host, including zakkart (`modules/sops/default.nix`): `age.sshKeyPaths = ["/etc/ssh/ssh_host_ed25519_key"]`, `useSystemdActivation = true`, **no `defaultSopsFile`** (a secret without a shard fails evaluation). Each secret sets `sopsFile = ./secrets.yaml` beside its consumer; a second shard in a directory is `secrets.<consumer>.yaml`.
- `.sops.yaml`: one `creation_rules` entry per shard, `path_regex` anchored on the full path, recipients = admin keys (`user_aidanp`, two YubiKey identities, one Secure Enclave key) plus exactly the hosts that run the consumer (`server_artemis`, `server_legion_node1..4`, `server_zakkart`; host keys through ssh-to-age). **A new shard needs a new rule in `.sops.yaml` and `just sops-edit` / `sops-create <path>`; `just sops-updatekeys` only after recipient changes.**
- Secret-to-service conventions: file path with `owner` + `restartUnits` (`modules/factorio/default.nix`: `owner = "factorio"; restartUnits = ["factorio.service"];`), or `sops.templates."<x>.env"` with `config.sops.placeholder."<secret>"` lines, `owner`, and `restartUnits` ("An EnvironmentFile is read once at start-up; without this a rotated key never reaches the running process", `modules/pocket-id/default.nix`). Portfolio's module takes `adminPasswordHashFile` and loads it via systemd `LoadCredential`, so the file never needs to be readable by the service user.
- Secret names are `<service>/<kebab-name>` (`portfolio/admin-password-hash`, `pocket-id/smtp-password`, `garret/signing-key`, `netbird/setup-key`).
- **Runtime-mutable state** (the pattern across services):
  - SQLite or data dir under a path owned by a static system user, never DynamicUser when the path is a Volume or an impermanence bind mount (`atuin.nix`, `media.nix`, `factorio/default.nix` all `lib.mkForce false` DynamicUser with a comment why).
  - On Legion: either a labelled ext4 Volume with `self.lib.mountGuard dataDir {inherit pkgs; owner; mode;}` (`modules/mount-guard.nix`; `RequiresMountsFor` + `ConditionPathIsMountPoint` + an `ExecStartPre` `install -d`), or root disk + restic as the only durability (atuin, portfolio at `/var/lib/portfolio` with a 12-hourly timer override).
  - Backups pause the service's units by default so SQLite is consistent (`backups.jobs.<name>.pauseUnits`, default `map (u: "${u}.service") s.units`); a service can set `pauseUnits = []` plus a `prepareCommand` for an online copy (garret's `garret-admin backup`).
  - On artemis: `persistence.directories = [stateDir]` (module `modules/impermanence.nix`, option `persistence.{directories,files,data.*,cache.*}`).
  - Stateless by choice where possible (Gatus in-memory, tinyauth DB holds sessions only).

## 6. ntfy

- **No ntfy anywhere in the repo.** `git grep -il ntfy` matches only a JPEG (binary noise). Nothing self-hosts ntfy, no `ntfy.jeiang.dev` hostname in `modules/edge/default.nix` `publicHostnames`, `dns/dnsconfig.js`, or `modules/gatus.nix`.
- Current notification channels: Alertmanager -> Discord webhook (`alertmanager/discord-webhook`), a healthchecks.io dead-man webhook, Hermes/Jev webhook (`modules/monitoring/default.nix` ~lines 610-690); Hermes assistant uses Telegram (`TELEGRAM_HOME_CHANNEL` in `hermes/env`).
- So the ntfy digest target is either the public `https://ntfy.sh` (topic as secret) or a newly self-hosted ntfy. [INFERENCE] If self-hosted, the nixpkgs `services.ntfy-sh` module fits the house rule "prefer the nixpkgs module": a `legion.services.ntfy` entry on node4 (small, stateless-ish, mesh or public edge vhost `ntfy.jeiang.dev`). Auth tokens would live in a sops shard. This is an open question for the grill, not something the repo decides yet. The app should accept `ntfy.url` + `ntfy.topic` + optional token file so either works.

## 7. Backup conventions

- **restic**, repo on S4 (Mega S3-compatible, `https://s3.eu-central-1.s4.mega.io`), `modules/backups/default.nix`.
- Legion: bucket `legion-restic-backups`, repo per service `s3:.../legion-restic-backups/<hostname>/<service>`, one shared password for all of Legion, secrets in `modules/backups/secrets.yaml` (`restic/password`, `restic/s4-env`, an env file with the S3 key pair), daily with `RandomizedDelaySec = "4h"`, `Persistent = true`; separate weekly `restic-maintenance-<name>` unit does `forget --prune --keep-daily 7 --keep-weekly 4 --keep-monthly 6` and `check --read-data-subset=5%`. Jobs are derived automatically from `legion.services.<name>.backupSet` (only needs paths); a service can override its timer (`systemd.timers.restic-backups-<name>.timerConfig` with `lib.mkForce`, as atuin hourly and portfolio 12-hourly). If the service has a Volume, every `backupSet` path must be under its mountpoint (assert in `services.nix`).
- artemis: a separate bucket `artemis-restic-backups`, single repo `persist`, own shard `modules/backups/secrets.artemis.yaml`, 02:00 daily; backs up an **explicit allowlist** (`etc/ssh var/lib/netbird var/lib/hermes var/lib/factorio var/lib/radarr var/lib/sonarr var/lib/seerr` + selected `$HOME` data) from a read-only btrfs snapshot of `/persist`. An assertion requires every backup path also be a `persistence.*` path. A nested btrfs subvolume would back up empty (AGENTS.md "artemis persistence"). The media library is intentionally not backed up.
- Recovery procedure: `docs/runbooks/restore.md` (per-node `restic-<service>` wrapper; restore to a scratch dir first).
- Fit for the tracker: its state (SQLite with Play status, Play log, tags, ratings) is user-authored and cannot be rebuilt by scraping, so it needs a restic job. On node4 it is `backupSet = ["/var/lib/f95-tracker"]` + `pauseUnits` default (service stops during the daily backup) or an online SQLite backup via `backups.jobs.f95-tracker.prepareCommand` (`sqlite3 .backup`/VACUUM INTO) with `pauseUnits = []`. [INFERENCE: the app should offer a way to produce a consistent online copy so no downtime is needed.]

## 8. artemis (future downloader host)

- OS: **NixOS**, x86_64, systemd-boot with boot counting (2 tries), CachyOS Zen4 full-LTO kernel; `modules/hosts/artemis/default.nix`. Impermanent btrfs root: root subvolume reset every boot, only `persistence.*` survives (AGENTS.md "artemis persistence"). btrfs RAID0 across three NVMe (`modules/hosts/artemis/disko.nix`; loss of one drive loses the pool).
- NetBird member: yes, via `modules/netbird.nix` (base module), mesh name `artemis.jeiang.vpn`, peer IP `100.89.148.91` (`modules/netbird-peers.nix`; changes on re-enrollment). Mesh interface is a trusted firewall interface, so services bound on all addresses with no `openFirewall` are reachable from Legion nodes and the Mac over the mesh. Backup tunnel: `wg-backup` WireGuard `10.100.0.2` when NetBird is down.
- Storage paths: user `aidanp` (`flake.lib.facts.userName`), persisted data dirs under `$HOME` include `Games`, `Downloads`, `Videos`, `Projects`, `.renpy`. Media stack keeps one tree `/var/lib/media` (`self.lib.mediaDownloadDir = /var/lib/media/downloads`, group `media`, hardlink-friendly). For a game downloader, a persisted dir such as `~/Games` (`persistence.data.directories`) or a new `/var/lib/f95-downloader`.
- To add a service on artemis:
  1. Put `nixos.modules.artemis = {...}: { ... }` in the feature file; static user + `lib.mkForce false` DynamicUser; `persistence.directories = [stateDir]` (set `user`/`group`/`mode` through the attrset form, as `media.nix`).
  2. If persisting a new path: AGENTS.md says run `just migrate-persist <checkout>` on artemis as root, deploy with `--boot`, then reboot. Removing a service that owns an impermanence bind mount cannot switch live either (`--boot`).
  3. Add the path to `backupSet` in `modules/backups/default.nix` (explicit allowlist, must be a persisted path).
  4. Add unit to the `prometheus.exporters.node` `--collector.systemd.unit-include` regex in `modules/hosts/artemis/default.nix`.
  5. If it should pause while gaming: `gaming.pauseUnits = ["<unit>.service"]` (as `qbittorrent.nix`).
  6. Secrets: shard beside the feature with a `.sops.yaml` rule including `server_artemis`.
- artemis deploys: deploy-rs `hostname = "artemis.jeiang.vpn"`, `sshUser = "aidanp"`, `sudo = "doas -u"`; `boot-health.service` blesses the new generation only when sshd is up and NetBird reports connected, else auto-reboots into the previous generation.
- artemis has no secrets in common with Legion: artemis and Legion back up to separate buckets and keys; a host with no backup job must not be a recipient of the backups shard (AGENTS.md "Decisions").

## 9. CI and flake check conventions

- `.github/workflows/ci.yml`: `discover` job evaluates `.#checks.x86_64-linux` attr names; a matrix job builds each check; `all-checks` is the only required status (merge `gh pr merge --auto --merge`, signed commits, never push to `main`). Pure evaluation in CI (never `--impure`).
- `modules/checks.nix`: checks are `toplevel-<host>` for every nixosConfiguration plus `statix`, `legion-nodes-json`, `netbird-invariants`, `hcloud-drift` (fixture-driven scripts), and `zakkart-system` on darwin. Adding a service to a host is covered by that host's `toplevel-*` check, no extra check needed.
- Formatting: treefmt (alejandra, deadnix, stylua) + statix (`just fmt`), `just check` = `nix flake check --impure --keep-going`.
- On `main`, `.ci/garret.sh` pushes every built path to the garret cache (OIDC from GitHub Actions; `modules/garret/default.nix` allowlists pushing repos by repository id and workflows by `job_workflow_refs`). Deploys only after the PR has merged and CI pushed closures, so targets substitute. A new app repo whose CI should push to garret needs its repository id and workflow ref added to garret's `oidc` entry first.
- Deploy: `just deploy <host> --skip-checks --remote-build`, fleet `just deploy-legion --remote-build`; "Deploy... need the user's explicit approval" (AGENTS.md "Guardrails"). Legion deploy user `deploy` (restricted key, sudo only for `activate-rs`).
- **Private-input caveat (not handled anywhere in the repo today):** every existing flake input (including `jeiang/website`, `garret`, `ripper`) is public, and `grep` found no `access-tokens`, netrc, or PAT in `modules/nix.nix`, `.github/`, `.ci/`, or the justfile; `ci.yml` uses the default `GITHUB_TOKEN` (`permissions: contents: read`, scoped to the dotfiles repo). `jeiang/f95-tracker` is private, so `inputs.f95-tracker.url = "github:jeiang/f95-tracker"` would fail to fetch in CI, on the Mac, and on any host that evaluates (with `--remote-build`, the Mac evaluates; targets get the .drv or substitute). Options: make the repo public (secrets are in sops, nothing sensitive in the code), use `git+ssh://git@github.com/jeiang/f95-tracker` with a deploy key (CI needs an SSH key secret), or add `access-tokens = github.com=<PAT>` to the Mac's `nix.custom.conf` and a fine-grained PAT secret to dotfiles CI. This is the single biggest friction point and must be decided before wiring.

## 10. Integration surface (what this repo must provide)

### 10.1 Flake outputs (`jeiang/f95-tracker`)
- `packages.<system>.default` (the server binary/closure; Go or Rust, built reproducibly, `x86_64-linux` required, `aarch64-darwin` for local use) and, if the downloader is separate, `packages.<system>.downloader`.
- `nixosModules.default` (the tracker server), `nixosModules.downloader` (the artemis agent), named per role like garret's `pusher`/`puller`/`watcher`. The default module should wrap so `package` defaults from this flake (portfolio's pattern), and should import nothing from nixpkgs beyond standard modules; it must not assume sops-nix or impermanence (those are the host's business).
- `checks.<system>`: build + a `pkgs.testers.runNixOSTest` booting the module (garret/portfolio both do), so the dotfiles `toplevel-<host>` check only has to evaluate it.
- Input stanza suggestion for dotfiles' `flake.nix`:
  ```nix
  f95-tracker.url = "github:jeiang/f95-tracker";
  f95-tracker.inputs.nixpkgs.follows = "nixpkgs";
  ```
  Name `f95-tracker` (repo name, kebab-case, matches `bill-splitter`, `character-randomizer`; module key `nixos.modules.f95-tracker` and `legion.services.f95-tracker`, sops prefix `f95-tracker/`). Drop `follows` only with a one-line reason (e.g. if CI pushes to garret against its own pin).
- If a release workflow pushes closures to garret: add `jeiang/f95-tracker` repository id and `release.yml@refs/tags/v*` to `job_workflow_refs` in `modules/garret/default.nix` (ripper's precedent).

### 10.2 `services.f95-tracker` module options (house style, modelled on `services.portfolio`)
Required by house style: `enable`, `package` (`mkPackageOption`), `user`/`group` (default `f95-tracker`, static, never DynamicUser), `host` (default `127.0.0.1`), `port` (type `port`), `stateDir` (default `/var/lib/f95-tracker`), `trustedProxies` (`listOf str`, for `X-Forwarded-For`), `environmentFile` (nullOr path, escape hatch for sops templates), `memoryMaxMB` (positive int).
App-specific:
- `baseUrl` (str; the public origin, used for OIDC redirect URI and absolute links), e.g. `https://f95.jeiang.dev`.
- `oidc.issuer` (str, `https://auth.jeiang.dev`), `oidc.clientId` (str, not secret, like Grafana's), `oidc.clientSecretFile` (path, loaded via `LoadCredential`), `oidc.scopes` (default `["openid" "email" "profile" "groups"]`), optional `oidc.allowedGroups`/`oidc.allowedSubject` to restrict to the single user.
- `sessionSecretFile` (path) for cookie signing/encryption, `LoadCredential`.
- `ntfy.url` (str, default `https://ntfy.sh`), `ntfy.topic` (str or path-to-file since a public-topic name is effectively a secret: prefer `ntfy.topicFile`), `ntfy.tokenFile` (nullOr path).
- `checks.schedule` (systemd `OnCalendar`, default `daily`; the module ships the `systemd.timers` + oneshot, or the app runs its own scheduler: pick one and expose only this knob), `checks.randomizedDelaySec`.
- `f95.cookieFile` / `f95.credentialsFile` (nullOr path) if the access research concludes login is required (see ResearchF95Access); `itch.apiKeyFile` (nullOr path) if itch.io needs it.
- Secrets: always `*File` options (path), read through systemd `LoadCredential` (portfolio's convention) so files stay root-readable; never inline values.
- Module must also provide: `systemd.services.f95-tracker` with `MemoryMax`, `Restart`, `StateDirectory`/ReadWritePaths hardening, a `/healthz` (or documented health endpoint) that returns an unauthenticated 2xx for blackbox/Gatus probes (Pocket ID `/healthz` precedent returns 204), and an online-consistent backup hook: a CLI subcommand or script (`f95-tracker backup <path>`) so dotfiles can set `backups.jobs.f95-tracker.prepareCommand` and `pauseUnits = []`.
- Downloader module (`services.f95-tracker-downloader`): `serverUrl` (the tracker over the mesh), `tokenFile`, `downloadDir`, `user`/`group`, `memoryMaxMB`; nothing opened on the firewall (mesh-only).

### 10.3 Example dotfiles feature file `modules/f95-tracker/default.nix` (prose, NOT applied)
```nix
{self, inputs, config, ...}: let
  port = 8087;                                   # must not collide: 8086 gatus, 8081/8082 garret, 8889 atuin, 5006 actual, 4321 portfolio (check every `ports.` literal before choosing)
  stateDir = "/var/lib/f95-tracker";
in {
  legion.services.f95-tracker = {
    node = "legion-node4";
    module = "f95-tracker";
    stateful = true;
    units = ["f95-tracker"];
    ports.app = port;
    firewall = [{inherit port; proto = "tcp"; scope = "private";}];
    backupSet = [stateDir];                      # root-disk + restic, like atuin/portfolio
  };

  nixos.modules.f95-tracker = {config, lib, ...}: let
    sopsFile = ./secrets.yaml;
  in {
    imports = [inputs.f95-tracker.nixosModules.default];

    services.f95-tracker = {
      enable = true;
      host = "0.0.0.0";                          # edge on node1 reaches it over the Hetzner private net
      inherit port stateDir;
      trustedProxies = [self.lib.legionNodes.${config.legion.services.caddy.node}.privateIPv4];
      baseUrl = "https://f95.jeiang.dev";
      oidc = {
        issuer = "https://auth.jeiang.dev";
        clientId = "<uuid from Pocket ID admin UI>";
        clientSecretFile = config.sops.secrets."f95-tracker/oidc-client-secret".path;
      };
      sessionSecretFile = config.sops.secrets."f95-tracker/session-secret".path;
      ntfy = { url = "https://ntfy.sh"; topicFile = config.sops.secrets."f95-tracker/ntfy-topic".path; };
    };

    sops.secrets = {
      "f95-tracker/oidc-client-secret" = {inherit sopsFile; restartUnits = ["f95-tracker.service"];};
      "f95-tracker/session-secret"     = {inherit sopsFile; restartUnits = ["f95-tracker.service"];};
      "f95-tracker/ntfy-topic"         = {inherit sopsFile; restartUnits = ["f95-tracker.service"];};
    };

    # Interval lost on node failure = history lost; keep it short like atuin/portfolio.
    systemd.timers.restic-backups-f95-tracker.timerConfig = {
      OnCalendar = lib.mkForce "hourly";
      RandomizedDelaySec = lib.mkForce "5m";
    };
    systemd.services.f95-tracker.serviceConfig.MemoryMax = "256M";
  };
}
```
Plus the surrounding edits (each is a file in `jeiang/.dotfiles`, via PR, signed commits, merge after `all-checks`):
1. `flake.nix`: add the input stanza.
2. `modules/hosts/legion/default.nix`: add `"f95-tracker"` to `legionModuleOrder` (optional; unlisted sorts last).
3. `modules/edge/default.nix`: add `f95.jeiang.dev` to `publicHostnames` and a vhost block `f95.jeiang.dev { ${logLine}${crowdsecLine}${appsecLine}reverse_proxy ${node4}:${port "f95-tracker" "app"} }` (node4 = `self.lib.legionNodes.<node>.privateIPv4` of the placement node); DNS needs nothing (wildcard `*` -> node1, proxied).
4. `.sops.yaml`: new rule `modules/f95-tracker/secrets\.yaml$` with admin keys + `*server_legion_node4`; create the shard with `just sops-create modules/f95-tracker/secrets.yaml`.
5. Pocket ID admin UI (manual): new OIDC client, callback URL = the app's callback under `baseUrl`; copy id into the Nix literal and secret into the shard.
6. `modules/gatus.nix` (`ok "F95 Tracker" "Services" "https://f95.jeiang.dev/healthz"`) and `modules/monitoring/default.nix` blackbox target `http://${node4}:${port}/healthz`; optional `modules/glance.nix`.
7. `modules/hermes-ops.nix`/`modules/hermes/SERVERS.md`: tier lists mention `legion.services` units; the repo says tier-2 sudoers cover "the other literal `legion.services` units", so check whether the new unit is picked up automatically and update the SERVERS.md table by hand (it mirrors the sudoers lists).
8. `docs/topology/*.svg`: re-run `just topology` and commit (AGENTS.md: hosts, networks, or services changed).
9. Deploy after merge + CI garret push: `just deploy legion-node4 --skip-checks --remote-build` with the user's explicit approval; first deploy may initialize the restic repo (`initialize = true`).
- If instead mesh-only: set `firewall = []`, skip steps 3 and the public DNS, reach the app at `http://100.89.<node4>:<port>` or put an NetBird-Only reverse-proxy service for it in the NetBird dashboard (not in Nix); but OIDC redirect URIs and browser access from a phone still want an HTTPS hostname, which favours the edge vhost.

## Open questions for the deployment grilling

1. **Private repo as flake input**: make `f95-tracker` public, or add a deploy key / PAT to dotfiles CI + the Mac's `nix` config? (Section 9; unresolved in infra today.)
2. **Placement**: legion-node4 (house default for small SQLite apps, has restic, edge reaches over private net) vs artemis (home IP for F95/itch egress, colocated with the downloader, but no edge-to-artemis precedent, impermanence + `--boot` deploys). Depends on ResearchF95Access findings about datacenter IPs/Cloudflare.
3. **Durability tier**: root disk + hourly restic (atuin pattern, data-loss budget = 1 h) vs a Volume (`legion-f95-tracker`, label = service key, ext4, needs hcloud Volume created and formatted by hand; name must be <=16 bytes: `legion.services` key `f95-tracker` is 11 bytes, OK) — Volumes cost money and need `hcloudVolumeId`.
4. **Auth mode**: app-native OIDC against Pocket ID (needs a client in the admin UI by hand) vs tinyauth `forward_auth` at the edge (zero app auth code, but then the app must trust the proxy and has no per-user identity; for a single-user app this may be enough). AGENTS.md puts tinyauth for apps "with no login of their own".
5. **Hostname and exposure**: `f95.jeiang.dev` (edge, proxied by Cloudflare, CrowdSec + appsec) vs mesh-only (atuin pattern) vs a NetBird dashboard service on `*.proxy.jeiang.dev` (NetBird-Only access). Does any client (the downloader on artemis, a phone) need to reach it off-mesh?
6. **ntfy**: public `ntfy.sh` with a secret topic, or self-host `services.ntfy-sh` (new `legion.services.ntfy`, new hostname, sops token, edge vhost or mesh-only)? The repo has no ntfy at all, and alerts currently go to Discord/Telegram.
7. **Scheduler ownership**: does the app own the daily check (in-process scheduler) or does the module ship a `systemd.timer`? Affects `Persistent=true`-style catch-up after downtime and where the schedule option lives.
8. **Downloader transport**: artemis downloader polls the tracker over the mesh (peer IP `100.89.148.91`/Legion nodes reachable only via NetBird, since Hetzner private net is not reachable from artemis per `modules/hermes/SERVERS.md`) with a token; where does it store files (`~/Games` persisted data dir vs `/var/lib/media`-style tree), and should it `gaming.pauseUnits`?
9. **CI/garret**: should the app repo's release CI push to garret (needs id + workflow ref in `modules/garret/default.nix` oidc, "A new pushing repository or workflow needs its entries there first")? And should `follows` be dropped for the cache-hit reason ripper gives?
10. **Pocket ID group**: restrict login to one Pocket ID group via the `groups` claim (Grafana pattern), or to a single subject?

## Recommendation

Deploy the tracker as a first-class `legion.services.f95-tracker` on **legion-node4** (the node for small single-user SQLite apps; precedent `modules/atuin.nix` and `modules/actual-budget.nix`), fronted by an edge vhost on `legion-node1` (`f95.jeiang.dev`, covered by the existing wildcard DNS and Caddy wildcard cert), with durability = root disk + `backupSet` restic job (hourly timer override), and the downloader as a separate module on **artemis** reaching the tracker over NetBird. Do not model the tracker's own OIDC clients in Nix: create the Pocket ID client by hand in the admin UI and put the secret in a new sops shard. The integration surface is:

- This repo exports `packages.<system>.default`, `nixosModules.default` (tracker; `services.f95-tracker`) and `nixosModules.downloader`, plus a `runNixOSTest` check, modelled on `portfolio`/`garret`. Options: `enable`, `package`, `user`/`group`, `host`, `port`, `stateDir`, `trustedProxies`, `baseUrl`, `oidc.{issuer,clientId,clientSecretFile,scopes}`, `sessionSecretFile`, `ntfy.{url,topicFile,tokenFile}`, `checks.schedule`, `environmentFile`, `memoryMaxMB`; every secret is a `*File` path read via `LoadCredential`; static system user; a `/healthz`; an online backup command.
- Dotfiles input name `f95-tracker` (`github:jeiang/f95-tracker`, `inputs.nixpkgs.follows = "nixpkgs"`); the dotfiles-side work is the PR file list in 10.3 (input, feature file, edge vhost + hostname, `.sops.yaml` rule + shard, Gatus/blackbox entries, SERVERS.md, topology SVGs).
- Biggest blocker to resolve first: the repo is private and nothing in dotfiles CI or the Mac's Nix config fetches private inputs today. Make it public, or add a deploy key or PAT.
- Second: ntfy is not self-hosted today; default to `https://ntfy.sh` with a secret topic file and leave the URL an option, unless the grill chooses to self-host.
- Placement could flip to artemis if ResearchF95Access shows F95zone blocks Hetzner datacenter IPs; the module is host-agnostic so only the dotfiles feature file changes (then `nixos.modules.artemis`, `persistence.directories`, `just migrate-persist`, `--boot` deploy, backup allowlist entry).
