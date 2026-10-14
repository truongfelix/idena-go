# Idena Go

## Research disclaimer

This is experimental research software. I cannot guarantee its security, correctness, or fitness for any purpose. Use it at your own risk, take responsibility for your decisions, independently verify changes, and stay vigilant.

Go implementation of the Idena network node.

[![Build Idena](https://github.com/truongfelix/idena-go/actions/workflows/main.yml/badge.svg?branch=master)](https://github.com/truongfelix/idena-go/actions/workflows/main.yml)

> Community build of the Idena node, on the same network and consensus rules as
> the official node 1.1.2. Test builds (pre-releases) for Linux, macOS and Windows
> are on the [releases page](https://github.com/truongfelix/idena-go/releases), each
> file with its SHA-256; or build from a reviewed commit.

The coordinated candidate source, Wasm artifacts, toolchains, and immutable
chain identifiers are recorded in [`compatibility/stack-lock.json`](compatibility/stack-lock.json).
That lock remains a candidate until every listed legacy differential gate has
passed; ordinary unit tests do not by themselves authorize a release.
The evidence format and approval procedure are documented in
[`compatibility/README.md`](compatibility/README.md).

## Community build

Maintained at [github.com/truongfelix](https://github.com/truongfelix) since October 2026, after the
[ubiubi18](https://github.com/ubiubi18) fork's hardening work (below). It is the node of the community
[desktop app](https://github.com/truongfelix/idena-desktop) and of the Android app
[Idena Mobile](https://github.com/truongfelix/idena-mobile), and runs on its own too. Same network, same
consensus rules, no hard fork. What it adds:

- **RPC methods the apps use**: `dna_validationSummary` (a validation's results, kept by the node),
  `dna_ceremonySummary` (a validation's flips by result, qualified to reported),
  `dna_identityHistory` (an identity's states over the epochs, from the node's own records), the oracle votings
  (`contract_oracleVotings`, `contract_oracleVoting`, `contract_oracleVotingBalanceUpdates`, from a local
  index), `bcn_contractCalls` (a contract's calls in given blocks, with whether each was accepted).
  `bcn_txReceipt` answers `null` when there is no receipt.
- **Sync**: full sync between fast sync turns instead of waiting for a snapshot; bounded progress per pass with
  block answers matched to their requests; snapshots chosen by how many peers offer them, moving on from failing
  ones; a peer's height counted only up to the blocks it serves.
- **Peers**: peer slots and the IPFS connection manager set by flags (below); up to 3 direct peers, kept
  connected beyond the slots; a peer stays dialable while another connection to it is open.
- **Disk writes**: write buffers for the chain database and the IPFS datastore (`--dbwritebuffer`,
  `--ipfswritebuffer`).
- **Switching from the official node**: it opens the official node's data folder, upgrading its IPFS repository
  in place (the official node then no longer opens it: copy the folder first), and accepts a key file that ends
  with a line break.
- **Other**: the clock check ignores NTP replies without a time; the stored consensus version is read without
  writing to the chain database; the Android app can give the node its key in memory (`StartMobileNodeWithKey`)
  instead of a key file. The dependency forks (wasm binding, iavl, kad-dht) come from truongfelix.
- **Releases**: Linux binaries need glibc 2.35 or newer (Ubuntu 22.04, Debian 12); each file has a `.sha256`
  and the release a `SHA256SUMS.txt`.

## Fork status

This branch keeps the Idena consensus rules, chain data formats, transaction
encoding, and network identifiers unchanged. It is intended to remain on the
same network as nodes built from `idena-network/idena-go`. The hardening changes
reject malformed, corrupt, or excessively large inputs earlier; they do not
introduce a new chain or consensus version.

### What was updated

- Go and CI were moved to Go `1.26.9`, with native builds tested on Linux,
  macOS, and Windows, including ARM64 where supported.
- Kubo, libp2p, cryptography, database, compression, and supporting Go modules
  were refreshed while preserving the node's existing protocol behavior.
- The Wasm binding is pinned to this fork's checksum-verified static archives.
- RPC API-key files and Unix IPC sockets are restricted to the current user;
  malformed RPC, P2P, protobuf, database, and fast-sync state is handled
  fail-closed instead of panicking or silently advancing state.
- Compressed P2P messages are size-checked before decompression allocation.
- The node runs the IPFS DHT in Kubo's auto mode, as the official node does: a
  DHT server when reachable from the internet, a client otherwise.
- CI runs focused race-detector tests, static analysis, cross-platform builds,
  and a repository-specific vulnerability policy before releases.

### Benefits

- Fewer known vulnerable or abandoned dependencies and less native build
  surface.
- Better resistance to malformed network traffic, corrupt local metadata, and
  permissive local credential files.
- Reproducible toolchain and Wasm artifact pins across the desktop and contract
  repositories.

### Risks and tradeoffs

- Dependency upgrades can still change performance, peer discovery, IPFS
  behavior, or resource usage even when consensus serialization is unchanged.
- `GO-2024-3218` (the upstream DHT advisory) applies in every DHT mode and has no
  fix upstream; see
  [`docs/security/vulnerability-policy.md`](docs/security/vulnerability-policy.md).
- Back up the full data directory before changing versions or storage profiles:
  an IPFS repository upgraded by this node no longer opens in the official node.
- Mixing a node binary, Wasm binding, or database copied from another revision
  is unsupported. Keep the exact source and artifact pins together.

## Building the source

Building `idena-go` requires Go 1.26.9 and a C compiler. `idena-go` uses Go modules as a dependency manager.
Once the dependencies are installed, run

```shell
go build
```

Run the validation and vulnerability gates before starting the node:

```shell
go test ./...
./scripts/govulncheck-filter.sh
```

## Running `idena-go`

To connect to the Idena mainnet, run the executable without parameters.
`idena-go` uses Kubo and a private IPFS network to store data.

### CLI parameters

* `--config` Use custom configuration file
* `--datadir` Node data directory (default `datadir`)
* `--rpcaddr` RPC listening address (default `localhost`)
* `--rpcport` RPC listening port (default `9009`)
* `--ipfsport` IPFS P2P port (default `40405`)
* `--ipfsportstatic` Prevent changing IPFS port (default `false`)
* `--ipfsrouting` Routing value written to the IPFS config file (default `dhtclient`; other values require `IDENA_ALLOW_UNSAFE_IPFS_ROUTING=1`); it does not change the node's DHT mode, which is Kubo's auto mode
* `--ipfsbootnode` Set custom bootstrap node
* `--fast` Use fast sync (default `true`)
* `--dbwritebuffer` Chain database write buffer in MiB (default `4`): a bigger buffer writes much less to disk (16 MiB: 40-75% less in mainnet replays) and costs about 2-4 times the added buffer in RAM; takes effect at the next start (config file: `"Database": {"WriteBufferMiB": 16}`)
* `--ipfswritebuffer` IPFS datastore write buffer in MiB (default `4`): a node reachable from the internet is a DHT server and stores the provider records of other nodes in its IPFS LevelDB, where each flush at 4 MiB rewrites about 100 MiB (5-6 GiB a day on a phone); a bigger buffer cuts these writes and costs about 2 times the added buffer in RAM; takes effect at the next start (config file: `"IpfsConf": {"DatastoreWriteBufferMiB": 16}`)
* `--verbosity` Log verbosity (default `3` - `Info`)
* `--nodiscovery` Do not discover another nodes (default `false`)
* `--profile=lowpower` Reduce bandwidth usage
* `--maxinboundownshardpeers`, `--maxinboundpeers`, `--maxoutboundownshardpeers`, `--maxoutboundpeers` Peer slots,
  incoming and outgoing, for peers of the node's own shard and of other shards (default profile `8`, `4`, `4`, `2`;
  `--profile=lowpower` `3`, `1`, `2`, `1`); a flag given overrides the profile's value
* `--ipfslowwater`, `--ipfshighwater` IPFS connection manager: above the high water it closes the least used
  connections down to the low water (default profile `30`, `50`; `--profile=lowpower` `8`, `10`); a high water of
  `0` keeps every connection; a flag given overrides the profile's value
* `--directpeers` Up to 3 nodes to stay connected to beyond the peer slots, comma-separated: a peer id (its address
  comes from a DHT lookup) or a multiaddr ending in `/p2p/<peer id>`. The node redials them when the connection
  drops, never disconnects them to make room, and accepts them with no free slot; `net_peers` marks them
  `"direct": true`. In a config file: `P2P.DirectPeers`. An empty value clears the config file's list
* `--apikey` Set RPC API key
* `--logfilesize` Set maximum log file size in KB (default `10240`)



### JSON config


Custom json configuration can be used if `--config=<config file name>` parameter is specified. Use `server` IPFS profile if you run `idena-go` on VPS to prevent local network scanning.

New IPFS repositories use Kubo's supported `flatfs` datastore. Existing repositories keep their configured datastore. A repository made by the official node (repository version 12, Badger v1) is upgraded to the current repository version on the first start, keeping its Badger datastore and its old config as `config.v12.bak`; the official node no longer opens it afterwards. Operators with a Badger v1 repository should plan an export/import migration before Kubo removes Badger support; back up the IPFS data directory before any manual migration.

```json
{
  "DataDir": "datadir",
  "P2P": {
    "MaxInboundPeers": 12,
    "MaxOutboundPeers": 6
  },
  "RPC": {
    "HTTPHost": "localhost",
    "HTTPPort": 9009
  },
  "IpfsConf": {
    "Profile": "server",
    "IpfsPort": 40405,
    "BlockPinThreshold": 0.3,
    "FlipPinThreshold": 0.5
  },
  "Sync": {
    "FastSync": true
  }
}
```

By default, blocks and flips are pinned in local ipfs storage with 30% and 50% probability respectively. If you want to pin (save) locally all blocks and flips, set 1 for `BlockPinThreshold` and `FlipPinThreshold`.

#### Local automine node

##### Config
For debug purposes you can run local automine node with this config.

```json
{
  "IpfsConf": {
    "BootNodes": [],
    "Profile": "server",
    "IpfsPort": 60606
  },
  "RPC": {
    "HTTPHost": "localhost",
    "HTTPPort": 9111
  },
  "GenesisConf": {
    "GodAddress": "0x0000000000000000000000000000000000000000",
    "FirstCeremonyTime": 1700000000
  },
  "Consensus": {
    "Automine": true
  },
  "Validation": {
    "ValidationInterval": 300000000000,
    "FlipLotteryDuration": 10000000000,
    "ShortSessionDuration": 40000000000,
    "LongSessionDuration": 40000000000,
    "AfterLongSessionDuration": 10000000000
  },
  "Network": 3
}
```

##### Description

* `GodAddress` - the address which refers to private key in nodekey file. So, when you are running automine node, you should see log in console `Coinbase address addr=<addr>` with this address. **This address will mine coins if network has 0 valid identities**;
* `FirstCeremonyTime` - timestamp of first validation ceremony;
* `Validation section` - duration of each validation period in nanoseconds;
* `Network` - should be different from 1 or 2, any `uint32` number
* `Ipfs bootnodes` - array of bootstrap nodes in case of running multiple local nodes

For more detailed configuration, see the [config structure](config/config.go).
