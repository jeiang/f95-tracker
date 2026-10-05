# R-TEST-5: boots nixosModules.default against a fake OIDC provider.
{ pkgs, module }:
let
  oidcPort = 9000;
  issuer = "http://127.0.0.1:${toString oidcPort}";

  # Static discovery document and an empty JWKS: enough for startup issuer discovery.
  oidcRoot = pkgs.linkFarm "fake-oidc" [
    {
      name = ".well-known/openid-configuration";
      path = pkgs.writeText "openid-configuration" (builtins.toJSON {
        inherit issuer;
        authorization_endpoint = "${issuer}/authorize";
        token_endpoint = "${issuer}/token";
        jwks_uri = "${issuer}/jwks";
        response_types_supported = [ "code" ];
        subject_types_supported = [ "public" ];
        id_token_signing_alg_values_supported = [ "RS256" ];
      });
    }
    {
      name = "jwks";
      path = pkgs.writeText "jwks" (builtins.toJSON { keys = [ ]; });
    }
  ];
in
pkgs.testers.runNixOSTest {
  name = "f95-tracker";

  nodes.machine =
    { pkgs, ... }:
    {
      imports = [ module ];

      environment.systemPackages = [ pkgs.sqlite ];

      # Test-only credentials; real hosts use sops paths.
      environment.etc = {
        "f95-test/oidc-secret".text = "oidc-secret";
        "f95-test/session-secret".text = "session-secret-session-secret-session";
        "f95-test/ntfy-token".text = "ntfy-token";
      };

      systemd.services.fake-oidc = {
        wantedBy = [ "multi-user.target" ];
        before = [ "f95-tracker.service" ];
        serviceConfig = {
          ExecStart = "${pkgs.python3}/bin/python3 -m http.server ${toString oidcPort} --bind 127.0.0.1 --directory ${oidcRoot}";
          DynamicUser = true;
        };
      };
      systemd.services.f95-tracker.requires = [ "fake-oidc.service" ];
      systemd.services.f95-tracker.after = [ "fake-oidc.service" ];

      services.f95-tracker = {
        enable = true;
        baseUrl = "http://127.0.0.1:8470";
        oidc = {
          inherit issuer;
          clientId = "f95-tracker";
          clientSecretFile = "/etc/f95-test/oidc-secret";
          allowedSubjects = [ "test-subject" ];
        };
        sessionSecretFile = "/etc/f95-test/session-secret";
        ntfy = {
          url = "http://127.0.0.1:9001";
          topic = "f95";
          tokenFile = "/etc/f95-test/ntfy-token";
        };
      };
    };

  testScript = ''
    machine.wait_for_unit("fake-oidc.service")
    machine.wait_for_unit("f95-tracker.service")
    machine.wait_for_open_port(8470)

    assert machine.succeed("curl -sf http://127.0.0.1:8470/healthz").strip() == "ok"

    machine.wait_for_unit("f95-tracker-check.timer")
    machine.succeed("systemctl is-active f95-tracker-check.timer")

    assert machine.succeed("stat -c '%U:%G %a' /var/lib/f95-tracker").strip() == "f95-tracker:f95-tracker 700"
    assert machine.succeed("stat -c '%U %a' /var/lib/f95-tracker/f95-tracker.db").strip() == "f95-tracker 600"

    # Online backup produces a valid SQLite file with the schema applied.
    machine.succeed("systemctl start f95-tracker-backup.service")
    backup = "/var/lib/f95-tracker/backup/f95-tracker.db"
    assert machine.succeed(f"sqlite3 {backup} 'pragma integrity_check'").strip() == "ok"
    assert int(machine.succeed(f"sqlite3 {backup} \"select count(*) from sqlite_master where name='game'\"").strip()) == 1
    assert machine.succeed(f"stat -c '%U %a' {backup}").strip() == "f95-tracker 600"
    # A second run replaces the backup atomically instead of failing on the existing file.
    machine.succeed("systemctl start f95-tracker-backup.service")

    # Losing the live DB while a backup exists: restarting serve restores it (ExecStartPre), marker included.
    machine.succeed("systemctl stop f95-tracker.service")
    machine.succeed("runuser -u f95-tracker -- sqlite3 /var/lib/f95-tracker/f95-tracker.db 'create table restore_marker(x)'")
    machine.succeed("systemctl start f95-tracker-backup.service")
    machine.succeed("rm -f /var/lib/f95-tracker/f95-tracker.db*")
    machine.succeed("systemctl restart f95-tracker.service")
    machine.wait_for_open_port(8470)
    assert machine.succeed("curl -sf http://127.0.0.1:8470/healthz").strip() == "ok"
    assert machine.succeed("stat -c '%U %a' /var/lib/f95-tracker/f95-tracker.db").strip() == "f95-tracker 600"
    assert machine.succeed("sqlite3 /var/lib/f95-tracker/f95-tracker.db \"select count(*) from sqlite_master where name='restore_marker'\"").strip() == "1"

    # With neither live DB nor backup, the backup unit fails and creates nothing.
    machine.succeed("systemctl stop f95-tracker.service")
    machine.succeed("rm -f /var/lib/f95-tracker/f95-tracker.db* /var/lib/f95-tracker/backup/f95-tracker.db")
    machine.fail("systemctl start f95-tracker-backup.service")
    machine.fail("test -e /var/lib/f95-tracker/f95-tracker.db")
    machine.fail("test -e /var/lib/f95-tracker/backup/f95-tracker.db")
  '';
}
