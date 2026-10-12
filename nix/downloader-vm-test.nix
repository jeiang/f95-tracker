# Boots nixosModules.downloader against a fake tracker that serves an empty job list.
{ pkgs, module }:
let
  port = 8470;

  fakeTracker = pkgs.writeText "fake-tracker.py" ''
    import json
    from http.server import BaseHTTPRequestHandler, HTTPServer

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            with open("/var/lib/fake-tracker/requests.log", "a") as log:
                log.write(f"{self.path} {self.headers.get('Authorization')}\n")
            body = json.dumps({"jobs": [], "cancelled": []}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    HTTPServer(("127.0.0.1", ${toString port}), Handler).serve_forever()
  '';
in
pkgs.testers.runNixOSTest {
  name = "f95-downloader";

  nodes.machine =
    { pkgs, ... }:
    {
      imports = [ module ];

      # Test-only credential; real hosts use sops paths.
      environment.etc."f95-test/downloader-token".text = "f95t_test-token";

      systemd.services.fake-tracker = {
        wantedBy = [ "multi-user.target" ];
        before = [ "f95-downloader.service" ];
        serviceConfig = {
          ExecStart = "${pkgs.python3}/bin/python3 ${fakeTracker}";
          DynamicUser = true;
          StateDirectory = "fake-tracker";
        };
      };

      services.f95-downloader = {
        enable = true;
        trackerUrl = "http://127.0.0.1:${toString port}";
        tokenFile = "/etc/f95-test/downloader-token";
        pollInterval = "1s";
      };
    };

  testScript = ''
    machine.wait_for_unit("fake-tracker.service")
    machine.wait_for_open_port(${toString port})
    machine.wait_for_unit("f95-downloader.service")

    machine.wait_until_succeeds(
        "grep -qxF '/api/v1/downloader/jobs Bearer f95t_test-token' /var/lib/fake-tracker/requests.log"
    )
    machine.succeed("systemctl is-active f95-downloader.service")

    games = "/home/f95-downloader/Games"
    assert machine.succeed(f"stat -c %U:%G {games}/.incoming").strip() == "f95-downloader:f95-downloader"
  '';
}
