#!/usr/bin/env python3
"""Refuses a Linux binary that requires a newer glibc than the oldest systems a release supports.

The loader refuses to start a binary when one of the glibc versions it requires is missing ("version `GLIBC_2.39'
not found"). Built on Ubuntu 24.04, the node requires GLIBC_2.39 (the wasm binding's optional pidfd_spawnp and
pidfd_getpid calls) and does not start on Ubuntu 22.04 or Debian 12; built in an Ubuntu 22.04 container it requires
at most 2.34.

Usage: check_glibc_floor.py <binary> [max version, default 2.35]
"""

import re
import subprocess
import sys

DEFAULT_MAX = "2.35"


def version_key(version):
    return tuple(int(part) for part in version.split("."))


def required_glibc_versions(readelf_output):
    """The glibc versions of `readelf -V` "Version needs" entries without the WEAK flag (a weak need only warns)."""
    section = readelf_output.split("Version needs section", 1)
    if len(section) < 2:
        return []
    versions = set()
    for match in re.finditer(r"Name: GLIBC_([0-9][0-9.]*)\s+Flags: (.*?)\s+Version:", section[1]):
        if "WEAK" not in match.group(2).split():
            versions.add(match.group(1))
    return sorted(versions, key=version_key)


def main(argv):
    if len(argv) not in (2, 3):
        print(__doc__.strip().splitlines()[-1], file=sys.stderr)
        return 2
    binary = argv[1]
    max_version = argv[2] if len(argv) == 3 else DEFAULT_MAX
    result = subprocess.run(["readelf", "-V", "--wide", binary], capture_output=True, text=True)
    if result.returncode != 0:
        print(f"readelf failed on {binary}: {result.stderr.strip()}", file=sys.stderr)
        return 2
    versions = required_glibc_versions(result.stdout)
    if not versions:
        print(f"no glibc version needs found in {binary}", file=sys.stderr)
        return 2
    too_new = [v for v in versions if version_key(v) > version_key(max_version)]
    if too_new:
        print(
            f"{binary} requires GLIBC_{', GLIBC_'.join(too_new)}: newer than {max_version}, "
            "so it does not start on older systems",
            file=sys.stderr,
        )
        return 1
    print(f"{binary} requires at most GLIBC_{versions[-1]} (limit {max_version})")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
