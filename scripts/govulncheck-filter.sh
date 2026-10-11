#!/usr/bin/env bash
set -euo pipefail

if go list -deps ./... | grep -Eq '^golang.org/x/crypto/openpgp($|/)'; then
  echo "govulncheck: forbidden OpenPGP package entered the dependency graph" >&2
  exit 1
fi

dht_module="github.com/libp2p/go-libp2p-kad-dht"
reviewed_dht_build="github.com/truongfelix/go-libp2p-kad-dht@v0.41.1-0.20260924080750-83f1403bcb17"
dht_review_deadline="2027-01-16"
if [[ "$(date -u +%F)" > "${dht_review_deadline}" ]]; then
  echo "govulncheck: GO-2024-3218 allowance expired on ${dht_review_deadline}; reassess the DHT exposure" >&2
  exit 1
fi
current_dht_build="$(go list -m -f '{{with .Replace}}{{.Path}}@{{.Version}}{{else}}{{.Path}}@{{.Version}}{{end}}' "${dht_module}")"
if [[ "${current_dht_build}" != "${reviewed_dht_build}" ]]; then
  echo "govulncheck: ${dht_module} build changed from reviewed ${reviewed_dht_build} to ${current_dht_build}; reassess GO-2024-3218 before updating the allowance" >&2
  exit 1
fi

# govulncheck looks a replaced module up under its replacement's path: the replaced modules' own advisories are
# checked here.
go mod edit -json |
  go run ./scripts -replaced-modules \
    -allow-replaced GO-2024-3218@${dht_module}

go tool govulncheck -format=json ./... |
  go run ./scripts \
    -ignore-unreachable GO-2026-5932@golang.org/x/crypto
