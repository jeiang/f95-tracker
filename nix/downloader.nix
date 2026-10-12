self:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.f95-downloader;
  inherit (lib) mkOption mkEnableOption mkIf types;

  defaultUser = "f95-downloader";
  ownsDefaultUser = cfg.user == defaultUser;
  # StateDirectory= is relative to /var/lib; other locations must already be writable by the user.
  inVarLib = lib.hasPrefix "/var/lib/" cfg.stateDir;
in
{
  options.services.f95-downloader = {
    enable = mkEnableOption "the F95 Tracker download worker";

    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.downloader;
      defaultText = lib.literalExpression "f95-tracker.packages.\${system}.downloader";
      description = "The f95-downloader package.";
    };

    user = mkOption {
      type = types.str;
      default = defaultUser;
      description = ''
        User the worker runs as. The default is created as a system user;
        any other value must already exist and own `gamesDir`.
      '';
    };

    group = mkOption {
      type = types.str;
      default = if ownsDefaultUser then defaultUser else "users";
      defaultText = lib.literalExpression ''"f95-downloader" for the default user, otherwise "users"'';
      description = "Group the worker runs as.";
    };

    trackerUrl = mkOption {
      type = types.str;
      example = "https://f95.example.com";
      description = "Base URL of the F95 Tracker server.";
    };

    tokenFile = mkOption {
      type = types.path;
      description = "File holding a `downloader`-scope API token. Loaded with LoadCredential, never copied to the store.";
    };

    gamesDir = mkOption {
      type = types.path;
      default = "/home/${cfg.user}/Games";
      defaultText = lib.literalExpression ''"/home/''${config.services.f95-downloader.user}/Games"'';
      description = "Games library root; downloads land in `.incoming` inside it.";
    };

    stateDir = mkOption {
      type = types.path;
      default = "/var/lib/f95-downloader";
      description = "Resume bookkeeping and MEGAcmd home.";
    };

    pollInterval = mkOption {
      type = types.str;
      default = "60s";
      description = "How often to poll the tracker for jobs (Go duration).";
    };
  };

  config = mkIf cfg.enable {
    users.users = mkIf ownsDefaultUser {
      ${defaultUser} = {
        isSystemUser = true;
        group = cfg.group;
        home = cfg.stateDir;
      };
    };
    users.groups = mkIf ownsDefaultUser { ${defaultUser} = { }; };

    systemd.tmpfiles.rules = [
      "d ${cfg.gamesDir} - ${cfg.user} ${cfg.group} -"
      "d ${cfg.gamesDir}/.incoming - ${cfg.user} ${cfg.group} -"
    ];

    systemd.services.f95-downloader = {
      description = "F95 Tracker download worker";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      path = [ pkgs.megacmd pkgs.gallery-dl pkgs.unar pkgs._7zz ];
      environment = {
        HOME = cfg.stateDir;
        F95_DOWNLOADER_TRACKER_URL = cfg.trackerUrl;
        F95_DOWNLOADER_GAMES_DIR = cfg.gamesDir;
        F95_DOWNLOADER_STATE_DIR = cfg.stateDir;
        F95_DOWNLOADER_POLL_INTERVAL = cfg.pollInterval;
      };
      serviceConfig = {
        ExecStart = "${cfg.package}/bin/f95-downloader";
        Restart = "on-failure";
        RestartSec = "10s";
        User = cfg.user;
        Group = cfg.group;
        LoadCredential = "downloader-token:${cfg.tokenFile}";
        StateDirectory = mkIf inVarLib (lib.removePrefix "/var/lib/" cfg.stateDir);
        StateDirectoryMode = "0700";
        UMask = "0022";
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ReadWritePaths = [ cfg.gamesDir cfg.stateDir ];
        # tmpfs hides every home; gamesDir is bound back in so the worker sees only the library.
        ProtectHome = "tmpfs";
        BindPaths = [ cfg.gamesDir ];
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictSUIDSGID = true;
        RestrictNamespaces = true;
        LockPersonality = true;
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        CapabilityBoundingSet = "";
      };
    };
  };
}
