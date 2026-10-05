self:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.f95-tracker;
  inherit (lib) mkOption mkEnableOption mkIf types;

  bin = "${cfg.package}/bin/f95-tracker";
  # StateDirectory= is relative to /var/lib.
  stateName = lib.removePrefix "/var/lib/" cfg.stateDir;
  listen = (if lib.hasInfix ":" cfg.host then "[${cfg.host}]" else cfg.host) + ":${toString cfg.port}";

  environment = {
    F95_TRACKER_STATE_DIR = cfg.stateDir;
    F95_TRACKER_LISTEN = listen;
    F95_TRACKER_BASE_URL = cfg.baseUrl;
    F95_TRACKER_OIDC_ISSUER = cfg.oidc.issuer;
    F95_TRACKER_OIDC_CLIENT_ID = cfg.oidc.clientId;
    F95_TRACKER_OIDC_ALLOWED_SUBJECTS = lib.concatStringsSep "," cfg.oidc.allowedSubjects;
    F95_TRACKER_NTFY_URL = cfg.ntfy.url;
    F95_TRACKER_NTFY_TOPIC = cfg.ntfy.topic;
  };

  hardening = {
    User = cfg.user;
    Group = cfg.group;
    StateDirectory = stateName;
    StateDirectoryMode = "0700";
    UMask = "0077";
    NoNewPrivileges = true;
    ProtectSystem = "strict";
    ReadWritePaths = [ cfg.stateDir ];
    PrivateTmp = true;
    PrivateDevices = true;
    ProtectHome = true;
    ProtectKernelTunables = true;
    ProtectKernelModules = true;
    ProtectControlGroups = true;
    RestrictSUIDSGID = true;
    RestrictNamespaces = true;
    LockPersonality = true;
    RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
    CapabilityBoundingSet = "";
    MemoryMax = cfg.memoryMax;
    # Restores the last backup when the live database is missing, before anything can create an empty one.
    ExecStartPre = restoreScript;
  };

  db = "${cfg.stateDir}/f95-tracker.db";
  backupFile = "${cfg.stateDir}/backup/f95-tracker.db";

  # `f95-tracker backup` refuses an existing destination, so write a temp name and rename.
  backupScript = pkgs.writeShellScript "f95-tracker-backup" ''
    set -eu
    # Never replace the backup with a freshly created empty database.
    [ -e ${db} ] || { echo "live database ${db} is missing; keeping existing backup" >&2; exit 1; }
    tmp=${backupFile}.new
    rm -f "$tmp"
    ${bin} backup "$tmp" --state-dir ${lib.escapeShellArg cfg.stateDir}
    mv -f "$tmp" ${backupFile}
  '';

  restoreScript = pkgs.writeShellScript "f95-tracker-restore" ''
    set -eu
    if [ ! -e ${db} ] && [ -e ${backupFile} ]; then
      cp ${backupFile} ${db}.restore
      chmod 0600 ${db}.restore
      mv ${db}.restore ${db}
    fi
  '';
in
{
  options.services.f95-tracker = {
    enable = mkEnableOption "F95 Tracker";
    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "self.packages.<system>.default";
    };
    user = mkOption { type = types.str; default = "f95-tracker"; };
    group = mkOption { type = types.str; default = "f95-tracker"; };
    host = mkOption { type = types.str; default = "127.0.0.1"; };
    port = mkOption { type = types.port; default = 8470; };
    stateDir = mkOption {
      type = types.path;
      default = "/var/lib/f95-tracker";
      description = "State directory; must be under /var/lib (it is managed with StateDirectory=).";
    };
    baseUrl = mkOption { type = types.str; };
    oidc = {
      issuer = mkOption { type = types.str; };
      clientId = mkOption { type = types.str; };
      clientSecretFile = mkOption { type = types.path; };
      allowedSubjects = mkOption { type = types.listOf types.str; };
    };
    sessionSecretFile = mkOption { type = types.path; };
    ntfy = {
      url = mkOption { type = types.str; };
      topic = mkOption { type = types.str; };
      tokenFile = mkOption { type = types.path; };
    };
    checks.schedule = mkOption {
      type = types.str;
      default = "*-*-* 08:00:00";
      description = "systemd calendar expression for the daily check.";
    };
    memoryMax = mkOption { type = types.str; default = "512M"; };

    backupUnit = mkOption {
      type = types.str;
      readOnly = true;
      internal = true;
      default = "f95-tracker-backup.service";
      description = "Oneshot that writes <stateDir>/backup/f95-tracker.db; run it as the backup prepare step.";
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.oidc.allowedSubjects != [ ];
        message = "services.f95-tracker.oidc.allowedSubjects must not be empty.";
      }
      {
        assertion = lib.hasPrefix "/var/lib/" cfg.stateDir;
        message = "services.f95-tracker.stateDir must be under /var/lib/.";
      }
    ];

    users.users.${cfg.user} = {
      isSystemUser = true;
      group = cfg.group;
    };
    users.groups.${cfg.group} = { };

    systemd.services.f95-tracker = {
      description = "F95 Tracker";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      inherit environment;
      serviceConfig = hardening // {
        ExecStart = "${bin} serve";
        LoadCredential = [
          "oidc-client-secret:${cfg.oidc.clientSecretFile}"
          "session-secret:${cfg.sessionSecretFile}"
          "ntfy-token:${cfg.ntfy.tokenFile}"
        ];
        Restart = "on-failure";
        RestartSec = 5;
      };
    };

    systemd.services.f95-tracker-check = {
      description = "F95 Tracker check";
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      inherit environment;
      serviceConfig = hardening // {
        Type = "oneshot";
        ExecStart = "${bin} check";
        LoadCredential = [ "ntfy-token:${cfg.ntfy.tokenFile}" ];
      };
    };

    systemd.timers.f95-tracker-check = {
      description = "F95 Tracker daily check";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnCalendar = cfg.checks.schedule;
        Persistent = true;
      };
    };

    systemd.services.f95-tracker-backup = {
      description = "F95 Tracker database backup";
      serviceConfig = hardening // {
        Type = "oneshot";
        ExecStart = backupScript;
      };
    };
  };
}
