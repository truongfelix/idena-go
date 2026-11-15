#!/usr/bin/env bash
set -euo pipefail

packages=(
  ./ipfs
  ./events
  ./blockchain
  ./blockchain/...
  ./core/appstate
  ./core/mempool
  ./core/state
  ./core/upgrade
  ./keystore
  ./secstore
  ./rpc
  ./vm/...
)

go test -tags=idena_memory_ipfs "${packages[@]}"

blocked_deps="$(
  go list -deps -tags=idena_memory_ipfs "${packages[@]}" |
    grep -E '^(github.com/ipfs/kubo|github.com/libp2p/go-libp2p-kad-dht)(/|$)' || true
)"

if [[ -n "${blocked_deps}" ]]; then
  echo "memory IPFS build unexpectedly imports network IPFS/DHT dependencies:" >&2
  echo "${blocked_deps}" >&2
  exit 1
fi

# Every other package must still compile with the tag, tests included. The node
# program and its launcher are left out: they start the network IPFS node,
# which this build does not have.
module="$(go list -m)"
vet_packages=()
while IFS= read -r pkg; do
  case "${pkg}" in
    "${module}" | "${module}/node") ;;
    *) vet_packages+=("${pkg}") ;;
  esac
done < <(go list -tags=idena_memory_ipfs ./...)

if [[ "${#vet_packages[@]}" -lt 50 ]]; then
  echo "memory IPFS build: only ${#vet_packages[@]} packages listed for go vet" >&2
  exit 1
fi

echo "memory IPFS build: go vet on ${#vet_packages[@]} packages"
go vet -tags=idena_memory_ipfs "${vet_packages[@]}"
