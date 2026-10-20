"""Validate the package entry point, not unavailable compute functionality."""

import subprocess
import sys


def test_installed_entrypoint_accepts_help() -> None:
    result = subprocess.run(
        [sys.executable, "-m", "agentos_compute", "--help"],
        capture_output=True,
        text=True,
        check=True,
    )
    assert result.stderr == ""


def test_unisolated_service_is_refused() -> None:
    result = subprocess.run(
        [sys.executable, "-m", "agentos_compute", "--serve"],
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode == 2
    assert "isolation_unavailable" in result.stderr
