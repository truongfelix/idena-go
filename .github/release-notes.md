Community build of the Idena node (idena-go), from https://github.com/truongfelix/idena-go. It runs on the same
Idena network as the official node 1.1.2, with the same consensus rules: no hard fork. A version with a hyphen
(`1.1.3-beta.1`) is a test build.

## Before you switch

Stop the official node and copy its whole data folder (`--datadir`). On its first start this node converts the IPFS
folder in it (`ipfs/`) to a newer format that the official node 1.1.2 does not open: going back needs that copy. The
chain database and the node key (`keystore/nodekey`) are kept. The folder holds your node key: keep the copy
private.

## Files

| System | File |
|---|---|
| Linux x64 (glibc 2.35 or newer: Ubuntu 22.04, Debian 12 or newer) | `idena-node-linux-<version>` |
| Linux arm64 (same glibc) | `idena-node-linux-aarch64-<version>` |
| Windows x64 | `idena-node-win-<version>.exe` |
| macOS Intel | `idena-node-mac-<version>` |
| macOS Apple Silicon | `idena-node-mac-arm64-<version>` |

Each file has its SHA-256 in a `.sha256` file beside it, and all of them are listed at the end of these notes
(Linux: `sha256sum <file>`; macOS: `shasum -a 256 <file>`; Windows PowerShell: `Get-FileHash <file>`).

The files are not signed with a publisher certificate. On Linux and macOS, make the file executable
(`chmod +x <file>`); on macOS, allow it once in System Settings > Privacy & Security, or remove the download mark
with `xattr -d com.apple.quarantine <file>`. On Windows, SmartScreen may ask: "More info", then "Run anyway".

## Run

The flags are the official node's: `./idena-node-linux-<version> --datadir <folder>` (see `--help`).
