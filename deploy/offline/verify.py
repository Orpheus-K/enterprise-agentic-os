"""Verify the unsigned Linux package using only Python 3's standard library."""

import hashlib
import json
import re
import stat
import sys
from pathlib import Path


def verify(directory: Path) -> int:
    if directory.is_symlink():
        raise ValueError("Package root is a symlink")
    root = directory.resolve(strict=True)
    manifest_path = root / "MANIFEST.json"
    if not stat.S_ISREG(manifest_path.lstat().st_mode):
        raise ValueError("Manifest is not a regular file")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if (
        manifest.get("format") != 1
        or manifest.get("profile") != "linux-amd64-sqlite-core"
        or not manifest.get("files")
    ):
        raise ValueError("Unsupported or empty package manifest")
    expected: set[str] = set()
    for entry in manifest["files"]:
        name, digest = entry["path"], entry["sha256"]
        if (
            not re.fullmatch(r"[a-zA-Z0-9@._/-]+", name)
            or any(part in ("", ".", "..") for part in name.split("/"))
            or name == "MANIFEST.json"
            or name in expected
            or not re.fullmatch(r"[0-9a-f]{64}", digest)
        ):
            raise ValueError("Invalid or duplicate manifest entry")
        expected.add(name)
    actual: set[str] = set()
    for item in root.rglob("*"):
        mode = item.lstat().st_mode
        if stat.S_ISDIR(mode):
            continue
        if not stat.S_ISREG(mode):
            raise ValueError("Package contains a nonregular file")
        name = item.relative_to(root).as_posix()
        if name != "MANIFEST.json":
            actual.add(name)
    if actual != expected:
        raise ValueError("Missing or unexpected package files")
    for entry in manifest["files"]:
        digest = hashlib.sha256()
        with (root / entry["path"]).open("rb") as source:
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(block)
        if digest.hexdigest() != entry["sha256"]:
            raise ValueError("Package checksum mismatch")
    return len(expected)


if __name__ == "__main__":
    try:
        if len(sys.argv) > 2:
            raise ValueError("Usage: python3 verify.py [package directory]")
        count = verify(Path(sys.argv[1]) if len(sys.argv) == 2 else Path(__file__).parent)
        print(f"Verified {count} files; profile=linux-amd64-sqlite-core")
    except (OSError, ValueError, TypeError, KeyError) as error:
        print(f"Package verification failed: {error}", file=sys.stderr)
        sys.exit(1)
