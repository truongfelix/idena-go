import hashlib
import json
from pathlib import Path
import re
import sys

from check_release_compatibility import (
    RELEASE_PLATFORMS,
    is_waived,
    validate,
    validate_release_artifacts,
)

INDEPENDENT_REBUILD_GATE = "independent-rebuild-digest-match"


ASSET_NAMES = {
    "linux-x64": "idena-node-linux-{version}",
    "linux-arm64": "idena-node-linux-aarch64-{version}",
    "windows-x64": "idena-node-win-{version}.exe",
    "macos-x64": "idena-node-mac-{version}",
    "macos-arm64": "idena-node-mac-arm64-{version}",
}


def fail(message):
    print(f"Release artifact verification failed: {message}", file=sys.stderr)
    raise SystemExit(1)


def file_digest(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_independent_evidence(lock_file, root_dir):
    with lock_file.open(encoding="utf-8") as handle:
        lock = json.load(handle)
    result = lock["gateResults"][INDEPENDENT_REBUILD_GATE]
    if is_waived(result):
        return None, None
    evidence_path = root_dir / result["evidence"]
    with evidence_path.open(encoding="utf-8") as handle:
        evidence = json.load(handle)
    return validate_release_artifacts(evidence, re.compile(r"^[0-9a-f]{64}$"))


def verify(lock_file, root_dir, builds_dir, release_tag):
    validate(lock_file, root_dir, release_tag)
    evidence_tag, expected_digests = load_independent_evidence(lock_file, root_dir)
    if expected_digests is not None and evidence_tag != release_tag:
        fail(f"evidence targets {evidence_tag!r}, not {release_tag!r}")

    version = release_tag.removeprefix("v")
    expected_files = set()
    for platform in RELEASE_PLATFORMS:
        asset_name = ASSET_NAMES[platform].format(version=version)
        expected_files.update((asset_name, f"{asset_name}.sha256"))
        asset_path = builds_dir / asset_name
        checksum_path = builds_dir / f"{asset_name}.sha256"
        if (
            asset_path.is_symlink()
            or checksum_path.is_symlink()
            or not asset_path.is_file()
            or not checksum_path.is_file()
        ):
            fail(f"missing release files for {platform}")
        if checksum_path.stat().st_size > 512:
            fail(f"oversized checksum file for {platform}")

        actual_digest = file_digest(asset_path)
        if expected_digests is not None and actual_digest != expected_digests[platform]:
            fail(f"{platform} digest does not match independent rebuild evidence")
        checksum_parts = checksum_path.read_text(encoding="utf-8").strip().split()
        if len(checksum_parts) != 2:
            fail(f"malformed checksum file for {platform}")
        checksum_name = checksum_parts[1]
        if checksum_parts[0] != actual_digest or checksum_name not in (
            asset_name,
            f"*{asset_name}",
        ):
            fail(f"checksum file does not match {asset_name}")

    actual_entries = {item.name for item in builds_dir.iterdir()}
    if actual_entries != expected_files:
        unexpected = sorted(actual_entries - expected_files)
        missing = sorted(expected_files - actual_entries)
        fail(f"unexpected release file set; missing={missing}, unexpected={unexpected}")
    if expected_digests is None:
        print("Release artifacts match their checksum files (independent rebuild waived by the owner)")
    else:
        print("Release artifacts match independent rebuild evidence")


if __name__ == "__main__":
    if len(sys.argv) != 5:
        fail("expected lock file, repository root, builds directory, and release tag")
    verify(
        Path(sys.argv[1]).resolve(),
        Path(sys.argv[2]).resolve(),
        Path(sys.argv[3]).resolve(),
        sys.argv[4],
    )
