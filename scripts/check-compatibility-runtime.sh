#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCK_FILE="$ROOT_DIR/compatibility/stack-lock.json"

if [[ -n "$(git -C "$ROOT_DIR" status --porcelain --untracked-files=normal)" ]]; then
  echo "Compatibility runtime boundary requires a clean worktree" >&2
  exit 1
fi

runtime_base="$({
  python3 - "$LOCK_FILE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    payload = json.load(handle)
for component in payload.get("components", []):
    if component.get("name") == "idena-go":
        print(component.get("runtimeCodeCommit", ""))
        break
PY
} || true)"

if [[ ! "$runtime_base" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Compatibility lock does not contain a valid idena-go runtime code commit" >&2
  exit 1
fi
git -C "$ROOT_DIR" cat-file -e "$runtime_base^{commit}"
git -C "$ROOT_DIR" merge-base --is-ancestor "$runtime_base" HEAD

unexpected=0
while IFS= read -r -d '' path; do
  case "$path" in
    README.md|compatibility/*|scripts/check-compatibility-runtime.sh|scripts/check-release-compatibility.sh|scripts/check_release_compatibility.py|scripts/check_release_compatibility_test.go|scripts/verify_release_artifacts.py|.github/*)
      ;;
    *_test.go|testdata/*|*/testdata/*)
      ;;
    *)
      echo "Runtime-affecting path changed after locked source commit: $path" >&2
      unexpected=1
      ;;
  esac
done < <(git -C "$ROOT_DIR" diff --name-only -z "$runtime_base..HEAD")

if [[ "$unexpected" != "0" ]]; then
  echo "Update the compatibility lock and rerun every required legacy gate before accepting runtime changes" >&2
  exit 1
fi

echo "Compatibility runtime boundary passed"
