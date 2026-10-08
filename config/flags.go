package config

import "github.com/urfave/cli"

const (
	DefaultDataDir            = "datadir"
	DefaultPort               = 40404
	DefaultRpcHost            = "localhost"
	DefaultRpcPort            = 9009
	DefaultRpcPortBuiltInNode = 9119
	DefaultIpfsDataDir        = "ipfs"
	DefaultIpfsPort           = 40405
	DefaultIpfsRouting        = "dhtclient"
	DefaultGodAddress         = "0x4d60dc6a2cba8c3ef1ba5e1eba5c12c54cee6b61"
	DefaultCeremonyTime       = int64(1567171800)
	DefaultSwarmKey           = "00d6f96bb2b02a7308ad87938d6139a974b555cc029ce416641a60c46db2f531"
	DefaultForceFullSync      = 100
	DefaultStoreCertRange     = 2000

	DefaultMaxInboundOwnShardPeers     = 8
	DefaultMaxOutboundOwnShardPeers    = 4
	DefaultMaxInboundNotOwnShardPeers  = 4
	DefaultMaxOutboundNotOwnShardPeers = 2

	DefaultBurntTxRange = 4320

	IpfsRoutingAuto       = "auto"
	IpfsRoutingAutoClient = "autoclient"
	IpfsRoutingCustom     = "custom"
	IpfsRoutingDelegated  = "delegated"
	IpfsRoutingDht        = "dht"
	IpfsRoutingDhtClient  = "dhtclient"
	IpfsRoutingDhtServer  = "dhtserver"
	IpfsRoutingNone       = "none"

	LowPowerMaxInboundOwnShardPeers     = 3
	LowPowerMaxOutboundOwnShardPeers    = 2
	LowPowerMaxInboundNotOwnShardPeers  = 1
	LowPowerMaxOutboundNotOwnShardPeers = 1

	SharedNodeMaxInboundOwnShardPeers     = 11
	SharedNodeMaxOutboundOwnShardPeers    = 5
	SharedNodeMaxInboundNotOwnShardPeers  = 6
	SharedNodeMaxOutboundNotOwnShardPeers = 3
)

var (
	DefaultIpfsBootstrapNodes = []string{
		// Reachable mainnet peers observed through the legacy network on
		// 2026-09-24. Keep the historical seeds below as fallbacks.
		"/ip4/49.12.192.149/tcp/40405/ipfs/QmNqkSwad5HTShxVzFcYLQkRCRjrs9ZhQykqrRTQcdR7xp",
		"/ip4/147.91.144.55/tcp/40406/ipfs/Qmbas7yV5Z41n9ZvvuDoVpaqMWNPxwrk2jSYPsUx3hVgaF",
		"/ip4/147.91.144.55/tcp/40405/ipfs/QmdiNHGUWc72ouo92mEUnPVWMFvESDHshWVqLnyAutdN7Q",
		"/ip4/51.178.138.211/tcp/40405/ipfs/QmTseSBwV9xPN2iEn6ViZbdPbk5MBk1HAD9SKy8B2EgSrY",
		"/ip4/212.28.76.68/tcp/40415/ipfs/QmVMxHMU7pFf475gQRA158unEQCbmhJz6u3k81nuRueAdp",
		"/ip6/2a01:4f8:1c17:fd5a::1/tcp/40405/ipfs/QmRH67cpeq5gZ4iUSEgarrmNuFA9axEw1DnJdc3hWUtZ3T",
		"/ip4/135.181.40.10/tcp/40405/ipfs/QmNYWtiwM1UfeCmHfWSdefrMuQdg6nycY5yS64HYqWCUhD",
		"/ip4/157.230.61.115/tcp/40403/ipfs/QmQHYY49pWWFeXXdR9rKd31bHRqRi2E4tk4CXDgYJZq5ry",
		"/ip4/124.71.148.124/tcp/40405/ipfs/QmWH9D4DjSvQyWyRUw76AopCfRS5CPR2gRnRoxP3QFaefx",
		"/ip4/139.59.42.4/tcp/40405/ipfs/QmNagyEFFNMdkFT7W6HivNjJAmYB6zjrr7ussnC8ys9b7f",
	}
	CfgFileFlag = cli.StringFlag{
		Name:  "config",
		Usage: "JSON configuration file",
	}
	DataDirFlag = cli.StringFlag{
		Name:  "datadir",
		Usage: "datadir for blockchain",
	}
	TcpPortFlag = cli.IntFlag{
		Name:  "port",
		Usage: "Network listening port",
	}
	RpcHostFlag = cli.StringFlag{
		Name:  "rpcaddr",
		Usage: "RPC listening address",
	}
	RpcPortFlag = cli.IntFlag{
		Name:  "rpcport",
		Usage: "RPC listening port",
	}
	BootNodeFlag = cli.StringFlag{
		Name:  "bootnode",
		Usage: "Bootstrap node url",
	}
	AutomineFlag = cli.BoolFlag{
		Name:  "automine",
		Usage: "Mine blocks alone without peers",
	}
	IpfsBootNodeFlag = cli.StringFlag{
		Name:  "ipfsbootnode",
		Usage: "Ipfs bootstrap node (overrides existing)",
	}
	IpfsPortFlag = cli.IntFlag{
		Name:  "ipfsport",
		Usage: "Ipfs port",
	}
	IpfsRoutingFlag = cli.StringFlag{
		Name:  "ipfsrouting",
		Usage: "Ipfs routing mode (default dhtclient; auto, custom, dht, and dhtserver require IDENA_ALLOW_UNSAFE_IPFS_ROUTING=1)",
	}
	NoDiscoveryFlag = cli.BoolFlag{
		Name:  "nodiscovery",
		Usage: "NoDiscovery can be used to disable the peer discovery mechanism.",
	}
	VerbosityFlag = cli.IntFlag{
		Name:  "verbosity",
		Usage: "Log verbosity",
		Value: 3,
	}
	GodAddressFlag = cli.StringFlag{
		Name:  "godaddress",
		Usage: "Idena god address",
	}
	CeremonyTimeFlag = cli.Int64Flag{
		Name:  "ceremonytime",
		Usage: "First ceremony time (unix)",
	}
	MaxNetworkDelayFlag = cli.IntFlag{
		Name:  "maxnetdelay",
		Usage: "Max network delay for broadcasting",
	}
	FastSyncFlag = cli.BoolFlag{
		Name:  "fast",
		Usage: "Enable fast sync",
	}
	ForceFullSyncFlag = cli.Uint64Flag{
		Name:  "forcefullsync",
		Usage: "Force full sync on last blocks",
	}
	DbWriteBufferFlag = cli.IntFlag{
		Name:  "dbwritebuffer",
		Usage: "Chain database write buffer in MiB (default 4); a bigger buffer writes much less to disk and costs about 2-4 times the added buffer in RAM",
	}
	IpfsWriteBufferFlag = cli.IntFlag{
		Name:  "ipfswritebuffer",
		Usage: "IPFS datastore write buffer in MiB (default 4); a bigger buffer writes much less to disk on a node reachable from the internet (DHT records) and costs about 2 times the added buffer in RAM",
	}
	ProfileFlag = cli.StringFlag{
		Name:  "profile",
		Usage: "Configuration profile",
	}
	// The peer and IPFS connection limit flags show the default profile's values; given, a flag overrides the
	// value of any profile.
	MaxInboundOwnShardPeersFlag = cli.IntFlag{
		Name:  "maxinboundownshardpeers",
		Usage: "Incoming peer slots for peers of the node's own shard; overrides the profile",
		Value: DefaultMaxInboundOwnShardPeers,
	}
	MaxInboundPeersFlag = cli.IntFlag{
		Name:  "maxinboundpeers",
		Usage: "Incoming peer slots for peers of other shards; overrides the profile",
		Value: DefaultMaxInboundNotOwnShardPeers,
	}
	MaxOutboundOwnShardPeersFlag = cli.IntFlag{
		Name:  "maxoutboundownshardpeers",
		Usage: "Outgoing peer slots for peers of the node's own shard; overrides the profile",
		Value: DefaultMaxOutboundOwnShardPeers,
	}
	MaxOutboundPeersFlag = cli.IntFlag{
		Name:  "maxoutboundpeers",
		Usage: "Outgoing peer slots for peers of other shards; overrides the profile",
		Value: DefaultMaxOutboundNotOwnShardPeers,
	}
	DirectPeersFlag = cli.StringFlag{
		Name:  "directpeers",
		Usage: "Up to 3 nodes to stay connected to beyond the peer slots, comma-separated: peer ids, or multiaddrs ending in /p2p/<id>",
	}
	IpfsLowWaterFlag = cli.IntFlag{
		Name:  "ipfslowwater",
		Usage: "IPFS connections kept when the connection manager trims; overrides the profile",
		Value: 30,
	}
	IpfsHighWaterFlag = cli.IntFlag{
		Name:  "ipfshighwater",
		Usage: "IPFS connections above which the connection manager trims down to --ipfslowwater, 0 keeps every connection; overrides the profile",
		Value: 50,
	}
	IpfsPortStaticFlag = cli.BoolFlag{
		Name:  "ipfsportstatic",
		Usage: "Enable static ipfs port",
	}
	ApiKeyFlag = cli.StringFlag{
		Name:  "apikey",
		Usage: "Set RPC api key",
	}
	LogFileSizeFlag = cli.IntFlag{
		Name:  "logfilesize",
		Usage: "Set log file size in KB",
		Value: 1024 * 100,
	}
	LogColoring = cli.BoolFlag{
		Name:  "logcoloring",
		Usage: "Use log coloring",
	}
	AutoOnline = cli.BoolFlag{
		Name:  "autoonline",
		Usage: "Node will automatically turn on online mining status",
	}
)
