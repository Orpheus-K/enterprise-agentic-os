"""Independent Linux install test. Target binaries run with an empty PATH."""

import json
import re
import selectors
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path


def main(source: Path) -> None:
    if sys.platform != "linux" or not source.is_absolute():
        raise ValueError("Run on Linux with an absolute package directory")
    with tempfile.TemporaryDirectory(prefix="agentos-offline-") as temporary:
        root = Path(temporary)
        installed = root / "installed"
        shutil.copytree(source, installed, symlinks=True)
        env = {"PATH": "", "TMPDIR": temporary, "AGENTOS_HTTP_ADDR": "127.0.0.1:0"}

        def run(
            command: list[str | Path],
            expected: int = 0,
            extra: dict[str, str] | None = None,
        ) -> str:
            result = subprocess.run(
                command,
                cwd=root,
                env=env | (extra or {}),
                capture_output=True,
                text=True,
                timeout=120,
                check=False,
            )
            if result.returncode != expected:
                raise AssertionError(f"Unexpected exit {result.returncode}: {result.stderr}")
            return result.stdout

        def verify(expected: int = 0) -> None:
            run([sys.executable, installed / "verify.py"], expected)

        verify()
        manifest = json.loads((installed / "MANIFEST.json").read_text())
        bom = json.loads((installed / "SBOM.cdx.json").read_text())
        assert bom["bomFormat"] == "CycloneDX" and bom["specVersion"] == "1.6"
        for identity in manifest["binaries"]:
            binary = installed / "bin" / identity["component"]
            # Windows cross-build copies do not carry POSIX executable mode.
            binary.chmod(0o700)
            assert (
                json.loads(
                    run(
                        [binary, "--version"],
                        extra={
                            "AGENTOS_CHANNEL_CONFIG": "missing",
                            "AGENTOS_MIGRATION_DATABASE_URL": "invalid",
                        },
                    )
                )
                == identity
            )
        notice = installed / next(
            entry["path"] for entry in manifest["files"] if entry["path"].startswith("licenses/")
        )
        original = notice.read_bytes()
        notice.chmod(0o600)
        notice.write_text("tampered")
        verify(1)
        notice.write_bytes(original)
        missing = notice.with_suffix(".missing")
        notice.rename(missing)
        verify(1)
        missing.rename(notice)
        extra = installed / "unexpected"
        extra.write_text("unexpected")
        verify(1)
        extra.unlink()
        extra.symlink_to(notice)
        verify(1)
        extra.unlink()
        verify()
        migrate = installed / "bin/agentos-migrate"
        database = root / "runtime.db"
        for _ in range(2):
            assert "ready" in run([migrate, "--sqlite", database])
        run(
            [migrate, "--sqlite", database],
            1,
            {"AGENTOS_MIGRATION_DATABASE_URL": "invalid"},
        )
        backup, restored = root / "backup", root / "restored.db"
        assert "verified" in run([migrate, "--sqlite-backup", database, backup])
        assert "verified" in run([migrate, "--sqlite-restore", backup, restored])
        run([migrate, "--sqlite-restore", backup, restored], 1)
        config = {
            "storage_driver": "sqlite",
            "sqlite_path": str(restored),
            "tenant_id": "10000000-0000-0000-0000-000000000001",
            "app_id": "cli_test",
            "channel": {
                "type": "feishu",
                "tenant_key": "test",
                "mode": "webhook",
                "app_secret_env": "APP",
                "verification_token_env": "TOKEN",
                "encrypt_key_env": "KEY",
            },
            "model": {
                "endpoint": "http://127.0.0.1:1/chat/completions",
                "name": "synthetic",
                "max_tokens": 64,
                "allow_http": True,
            },
        }
        configuration = root / "channel.json"
        configuration.write_text(json.dumps(config))
        site = {
            "AGENTOS_CHANNEL_CONFIG": str(configuration),
            "APP": "synthetic",
            "TOKEN": "synthetic",
            "KEY": "synthetic",
        }
        binary = installed / "bin/agentos"
        with subprocess.Popen(
            [binary],
            cwd=root,
            env=env | site,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        ) as server:
            try:
                assert server.stdout is not None
                with selectors.DefaultSelector() as selector:
                    selector.register(server.stdout, selectors.EVENT_READ)
                    if not selector.select(15):
                        raise AssertionError("Core did not announce startup")
                    log = json.loads(server.stdout.readline())
                assert log["msg"] == "core listening", log
                base = "http://" + log["address"]
                # Never inherit host HTTP proxies for loopback verification.
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

                def get(route: str, expected: int) -> bytes:
                    try:
                        response = opener.open(base + route, timeout=5)
                    except urllib.error.HTTPError as error:
                        response = error
                    with response:
                        assert response.status == expected
                        return response.read()

                get("/api/health", 200)
                get("/api/ready", 503)
                html = get("/", 200).decode()
                assets = re.findall(r'(?:src|href)="(/assets/[^\"]+)"', html)
                assert len(assets) >= 2
                for asset in assets:
                    get(asset, 200)
                get("/api/missing", 404)
            finally:
                server.terminate()
                try:
                    server.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait(timeout=5)
        configuration.write_text(json.dumps(config | {"sqlite_path": str(root / "missing.db")}))
        run([binary], 1, site)
        configuration.write_text(json.dumps(config | {"tools": {}}))
        run([binary], 1, site)
        print(
            "Linux offline install PASS: manifests/SBOM, tamper/missing/extra/symlink rejection, "
            "binary identities, SQLite migration/backup/restore, embedded UI, "
            "readiness and invalid startup; binary PATH empty"
        )


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise ValueError(
            "Usage: python3 scripts/test-offline-linux.py <absolute package directory>"
        )
    main(Path(sys.argv[1]))
