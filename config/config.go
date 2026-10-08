package config

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/fileutil"
	"github.com/idena-network/idena-go/crypto"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/rpc"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

const (
	datadirPrivateKey         = "nodekey" // Path within the datadir to the node's private key
	apiKeyFileName            = "api.key"
	LowPowerProfile           = "lowpower"
	SharedNodeProfile         = "shared"
	DefaultProfile            = "default"
	AllowUnsafeIpfsRoutingEnv = "IDENA_ALLOW_UNSAFE_IPFS_ROUTING"
	AllowIpfsDhtServerEnv     = "IDENA_ALLOW_IPFS_DHT_SERVER"
	ipfsUnsafeRoutingEnabled  = "1"
)

type Config struct {
	DataDir          string
	Network          uint32
	AutoOnline       bool
	IsDebug          bool
	Consensus        *ConsensusConf
	P2P              P2P
	RPC              *rpc.Config
	GenesisConf      *GenesisConf
	IpfsConf         *IpfsConfig
	Validation       *ValidationConfig
	Sync             *SyncConfig
	OfflineDetection *OfflineDetectionConfig
	Blockchain       *BlockchainConfig
	Mempool          *Mempool
	Database         *DatabaseConfig
}

func (c *Config) ProvideNodeKey(key string, password string, withBackup bool) error {
	instanceDir := filepath.Join(c.DataDir, "keystore")
	if err := fileutil.EnsurePrivateDir(instanceDir); err != nil {
		return err
	}

	keyfile := filepath.Join(instanceDir, datadirPrivateKey)

	currentKey, err := crypto.LoadECDSA(keyfile)
	if err != nil && !os.IsNotExist(err) {
		return errors.Errorf("failed to load existing key, err: %v", err.Error())
	}
	if !withBackup && currentKey != nil {
		return errors.New("key already exists")
	}

	keyBytes, err := hex.DecodeString(key)
	if err != nil {
		return errors.Errorf("error while decoding key, err: %v", err.Error())
	}

	decrypted, err := crypto.Decrypt(keyBytes, password)
	if err != nil {
		return errors.Wrap(err, "error while decrypting key")
	}
	defer clear(decrypted)

	ecdsaKey, err := crypto.ToECDSA(decrypted)
	if err != nil {
		return errors.Errorf("key is not valid ECDSA key, err: %v", err.Error())
	}

	if withBackup && currentKey != nil {
		backup, err := os.CreateTemp(instanceDir, "backup-*")
		if err != nil {
			return errors.Errorf("failed to reserve key backup, err: %v", err.Error())
		}
		backupFile := backup.Name()
		if err := backup.Close(); err != nil {
			_ = os.Remove(backupFile)
			return errors.Errorf("failed to close key backup, err: %v", err.Error())
		}
		if err := crypto.SaveECDSA(backupFile, currentKey); err != nil {
			_ = os.Remove(backupFile)
			return errors.Errorf("failed to backup key, err: %v", err.Error())
		}
	}

	if err := crypto.SaveECDSA(keyfile, ecdsaKey); err != nil {
		return errors.Errorf("failed to persist key, err: %v", err.Error())
	}
	return nil
}

func (c *Config) NodeKey() (*ecdsa.PrivateKey, error) {
	// Generate ephemeral key if no datadir is being used.
	if c.DataDir == "" {
		key, err := crypto.GenerateKey()
		return key, errors.Wrap(err, "failed to generate ephemeral node key")
	}

	instanceDir := filepath.Join(c.DataDir, "keystore")
	if err := fileutil.EnsurePrivateDir(instanceDir); err != nil {
		return nil, errors.Wrap(err, "failed to persist node key")
	}

	keyfile := filepath.Join(instanceDir, datadirPrivateKey)
	key, err := crypto.LoadECDSA(keyfile)
	if err == nil {
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, errors.Wrap(err, "failed to load node key")
	}

	// No persistent key found, generate and store a new one.
	key, err = crypto.GenerateKey()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate node key")
	}
	if err := crypto.SaveECDSA(keyfile, key); err != nil {
		return nil, errors.Wrap(err, "failed to persist node key")
	}
	return key, nil
}

// NodeDB returns the path to the discovery node database.
func (c *Config) NodeDB() string {
	if c.DataDir == "" {
		return "" // ephemeral
	}
	return filepath.Join(c.DataDir, "nodes")
}

func (c *Config) KeyStoreDataDir() (string, error) {
	instanceDir := filepath.Join(c.DataDir, "keystore")
	if err := fileutil.EnsurePrivateDir(instanceDir); err != nil {
		log.Error(fmt.Sprintf("Failed to create keystore datadir: %v", err))
		return "", err
	}
	return instanceDir, nil
}

func (c *Config) SetApiKey() error {
	shouldSaveKey := true
	apiKeyFile := filepath.Join(c.DataDir, apiKeyFileName)
	if c.RPC.APIKey == "" {
		data, exists, err := readPrivateFile(apiKeyFile)
		if err != nil {
			return err
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			randomKey, err := crypto.GenerateKey()
			if err != nil {
				return err
			}
			key = hex.EncodeToString(crypto.FromECDSA(randomKey)[:16])
		} else {
			shouldSaveKey = !exists
		}
		c.RPC.APIKey = key
	}

	if shouldSaveKey {
		return fileutil.WriteFileAtomic(apiKeyFile, []byte(c.RPC.APIKey), 0600)
	}
	return nil
}

func readPrivateFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.Errorf("secret path is not a regular file: %v", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, false, errors.Errorf("secret path changed while opening: %v", path)
	}
	if err := file.Chmod(0600); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(file)
	return data, true, err
}

func MakeMobileConfig(path string, cfg string) (*Config, error) {
	conf := getDefaultConfig(filepath.Join(path, DefaultDataDir))

	if cfg != "" {
		log.Info("using custom configuration")
		bytes := []byte(cfg)
		err := json.Unmarshal(bytes, &conf)
		if err != nil {
			return nil, errors.Errorf("Cannot parse JSON config")
		}
	} else {
		log.Info("using default config")
	}

	if conf.IpfsConf != nil && conf.IpfsConf.Routing == "" {
		conf.IpfsConf.Routing = DefaultIpfsRouting
	}
	if err := validateConfig(conf); err != nil {
		return nil, err
	}
	return conf, nil
}

func MakeConfig(ctx *cli.Context, cfgTransform func(cfg *Config)) (*Config, error) {
	cfg, err := MakeConfigFromFile(ctx.String(CfgFileFlag.Name))
	if err != nil {
		return nil, err
	}
	if ctx.IsSet(DataDirFlag.Name) {
		cfg.DataDir = ctx.String(DataDirFlag.Name)
	}
	cfgTransform(cfg)
	applyFlags(ctx, cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyProfile(ctx *cli.Context, cfg *Config) {
	if ctx.IsSet(ProfileFlag.Name) {
		switch ctx.String(ProfileFlag.Name) {
		case LowPowerProfile:
			applyLowPowerProfile(cfg)
		case SharedNodeProfile:
			applySharedNodeProfile(cfg)
		case DefaultProfile:
			applyDefaultProfile(cfg)
		default:
			println("unknown node profile")
		}
	} else {
		applyDefaultProfile(cfg)
	}
	if cfg.IpfsConf.GracePeriod == "" {
		cfg.IpfsConf.GracePeriod = "40s"
	}
	if cfg.IpfsConf.ReproviderInterval == "" {
		cfg.IpfsConf.ReproviderInterval = "12h"
	}
	if cfg.IpfsConf.Routing == "" {
		cfg.IpfsConf.Routing = DefaultIpfsRouting
	}
}

func MakeConfigFromFile(file string) (*Config, error) {
	cfg := getDefaultConfig(DefaultDataDir)
	if file != "" {
		if err := loadConfig(file, cfg); err != nil {
			log.Error(err.Error())
			return nil, err
		}
	}
	return cfg, nil
}

func getDefaultConfig(dataDir string) *Config {

	ipfsConfig := GetDefaultIpfsConfig()
	ipfsConfig.DataDir = filepath.Join(dataDir, DefaultIpfsDataDir)
	ipfsConfig.IpfsPort = DefaultIpfsPort
	ipfsConfig.BootNodes = DefaultIpfsBootstrapNodes
	ipfsConfig.SwarmKey = DefaultSwarmKey

	return &Config{
		DataDir: dataDir,
		Network: 0x1, // mainnet
		P2P: P2P{
			MaxInboundPeers:          DefaultMaxInboundNotOwnShardPeers,
			MaxOutboundPeers:         DefaultMaxOutboundNotOwnShardPeers,
			MaxInboundOwnShardPeers:  DefaultMaxInboundOwnShardPeers,
			MaxOutboundOwnShardPeers: DefaultMaxOutboundOwnShardPeers,
			DisableMetrics:           false,
		},
		Consensus: GetDefaultConsensusConfig(),
		RPC:       rpc.GetDefaultRPCConfig(DefaultRpcHost, DefaultRpcPort),
		GenesisConf: &GenesisConf{
			FirstCeremonyTime: DefaultCeremonyTime,
			GodAddress:        common.HexToAddress(DefaultGodAddress),
		},
		IpfsConf:   ipfsConfig,
		Validation: &ValidationConfig{},
		Sync: &SyncConfig{
			FastSync:            true,
			ForceFullSync:       DefaultForceFullSync,
			AllFlipsLoadingTime: time.Hour * 2,
		},
		OfflineDetection: GetDefaultOfflineDetectionConfig(),
		Blockchain: &BlockchainConfig{
			StoreCertRange: DefaultStoreCertRange,
			BurnTxRange:    DefaultBurntTxRange,
		},
		Mempool:  GetDefaultMempoolConfig(),
		Database: &DatabaseConfig{},
	}
}

func applyFlags(ctx *cli.Context, cfg *Config) {
	applyProfile(ctx, cfg)
	applyCommonFlags(ctx, cfg)
	applyP2PFlags(ctx, cfg)
	applyConsensusFlags(ctx, cfg)
	applyRpcFlags(ctx, cfg)
	applyGenesisFlags(ctx, cfg)
	applyIpfsFlags(ctx, cfg)
	applyValidationFlags(ctx, cfg)
	applySyncFlags(ctx, cfg)
	applyDatabaseFlags(ctx, cfg)
}

func applyDatabaseFlags(ctx *cli.Context, cfg *Config) {
	if ctx.IsSet(DbWriteBufferFlag.Name) {
		if cfg.Database == nil {
			cfg.Database = &DatabaseConfig{}
		}
		cfg.Database.WriteBufferMiB = ctx.Int(DbWriteBufferFlag.Name)
	}
}

func applyCommonFlags(ctx *cli.Context, cfg *Config) {
	if ctx.IsSet(AutoOnline.Name) {
		cfg.AutoOnline = ctx.Bool(AutoOnline.Name)
	}
}

func applySyncFlags(ctx *cli.Context, cfg *Config) {
	if ctx.IsSet(FastSyncFlag.Name) {
		cfg.Sync.FastSync = ctx.Bool(FastSyncFlag.Name)
	}
	if ctx.IsSet(ForceFullSyncFlag.Name) {
		cfg.Sync.ForceFullSync = ctx.Uint64(ForceFullSyncFlag.Name)
	}
}

func applyP2PFlags(ctx *cli.Context, cfg *Config) {
	if ctx.IsSet(MaxNetworkDelayFlag.Name) {
		cfg.P2P.MaxDelay = ctx.Int(MaxNetworkDelayFlag.Name)
	}
	// Applied after the profile, which sets all four.
	if ctx.IsSet(MaxInboundOwnShardPeersFlag.Name) {
		cfg.P2P.MaxInboundOwnShardPeers = ctx.Int(MaxInboundOwnShardPeersFlag.Name)
	}
	if ctx.IsSet(MaxInboundPeersFlag.Name) {
		cfg.P2P.MaxInboundPeers = ctx.Int(MaxInboundPeersFlag.Name)
	}
	if ctx.IsSet(MaxOutboundOwnShardPeersFlag.Name) {
		cfg.P2P.MaxOutboundOwnShardPeers = ctx.Int(MaxOutboundOwnShardPeersFlag.Name)
	}
	if ctx.IsSet(MaxOutboundPeersFlag.Name) {
		cfg.P2P.MaxOutboundPeers = ctx.Int(MaxOutboundPeersFlag.Name)
	}
	if ctx.IsSet(DirectPeersFlag.Name) {
		cfg.P2P.DirectPeers = splitDirectPeers(ctx.String(DirectPeersFlag.Name))
	}
}

func applyConsensusFlags(ctx *cli.Context, cfg *Config) {
	if ctx.IsSet(AutomineFlag.Name) {
		cfg.Consensus.Automine = ctx.Bool(AutomineFlag.Name)
	}
}

func applyRpcFlags(ctx *cli.Context, cfg *Config) {
	if ctx.IsSet(RpcHostFlag.Name) {
		cfg.RPC.HTTPHost = ctx.String(RpcHostFlag.Name)
	}
	if ctx.IsSet(RpcPortFlag.Name) {
		cfg.RPC.HTTPPort = ctx.Int(RpcPortFlag.Name)
	}
	if ctx.IsSet(ApiKeyFlag.Name) {
		cfg.RPC.APIKey = ctx.String(ApiKeyFlag.Name)
	}
}

func applyGenesisFlags(ctx *cli.Context, cfg *Config) {
	if ctx.IsSet(GodAddressFlag.Name) {
		cfg.GenesisConf.GodAddress = common.HexToAddress(ctx.String(GodAddressFlag.Name))
	}
	if ctx.IsSet(CeremonyTimeFlag.Name) {
		cfg.GenesisConf.FirstCeremonyTime = ctx.Int64(CeremonyTimeFlag.Name)
	}
}

func applyIpfsFlags(ctx *cli.Context, cfg *Config) {
	cfg.IpfsConf.DataDir = filepath.Join(cfg.DataDir, DefaultIpfsDataDir)

	if ctx.IsSet(IpfsPortFlag.Name) {
		cfg.IpfsConf.IpfsPort = ctx.Int(IpfsPortFlag.Name)
	}
	if ctx.IsSet(IpfsPortStaticFlag.Name) {
		cfg.IpfsConf.StaticPort = ctx.Bool(IpfsPortStaticFlag.Name)
	}
	if ctx.IsSet(IpfsRoutingFlag.Name) {
		cfg.IpfsConf.Routing = ctx.String(IpfsRoutingFlag.Name)
	}
	if ctx.IsSet(IpfsBootNodeFlag.Name) {
		cfg.IpfsConf.BootNodes = []string{ctx.String(IpfsBootNodeFlag.Name)}
	}
	if ctx.IsSet(IpfsWriteBufferFlag.Name) {
		cfg.IpfsConf.DatastoreWriteBufferMiB = ctx.Int(IpfsWriteBufferFlag.Name)
	}
	// Applied after the profile, which sets both.
	if ctx.IsSet(IpfsLowWaterFlag.Name) {
		cfg.IpfsConf.LowWater = ctx.Int(IpfsLowWaterFlag.Name)
	}
	if ctx.IsSet(IpfsHighWaterFlag.Name) {
		cfg.IpfsConf.HighWater = ctx.Int(IpfsHighWaterFlag.Name)
	}
}

func validateConfig(cfg *Config) error {
	if err := validateDatabaseConfig(cfg.Database); err != nil {
		return err
	}
	if err := validatePeerLimits(cfg.P2P); err != nil {
		return err
	}
	if _, err := cfg.P2P.DirectPeerInfos(); err != nil {
		return err
	}
	if cfg.IpfsConf == nil {
		return nil
	}
	if err := validateIpfsDatastoreWriteBuffer(cfg.IpfsConf.DatastoreWriteBufferMiB); err != nil {
		return err
	}
	if err := validateIpfsConnectionLimits(cfg.IpfsConf.LowWater, cfg.IpfsConf.HighWater); err != nil {
		return err
	}
	return validateIpfsRouting(cfg.IpfsConf.Routing)
}

func validatePeerLimits(p2p P2P) error {
	limits := []struct {
		name  string
		value int
	}{
		{"P2P.MaxInboundOwnShardPeers", p2p.MaxInboundOwnShardPeers},
		{"P2P.MaxInboundPeers", p2p.MaxInboundPeers},
		{"P2P.MaxOutboundOwnShardPeers", p2p.MaxOutboundOwnShardPeers},
		{"P2P.MaxOutboundPeers", p2p.MaxOutboundPeers},
	}
	for _, limit := range limits {
		if limit.value < 0 {
			return errors.Errorf("invalid %s %d: must not be negative", limit.name, limit.value)
		}
	}
	return nil
}

// validateIpfsConnectionLimits accepts 0 for either value: the connection manager then keeps every connection.
func validateIpfsConnectionLimits(lowWater, highWater int) error {
	if lowWater < 0 || highWater < 0 {
		return errors.Errorf("invalid IpfsConf.LowWater %d / HighWater %d: must not be negative", lowWater, highWater)
	}
	if highWater > 0 && lowWater > highWater {
		return errors.Errorf("invalid IpfsConf.LowWater %d: must not be above HighWater %d", lowWater, highWater)
	}
	return nil
}

func validateIpfsRouting(routing string) error {
	switch routing {
	case "", IpfsRoutingAutoClient, IpfsRoutingDelegated, IpfsRoutingDhtClient, IpfsRoutingNone:
		return nil
	case IpfsRoutingAuto, IpfsRoutingCustom, IpfsRoutingDht, IpfsRoutingDhtServer:
		if allowUnsafeIpfsRouting() {
			return nil
		}
		return errors.Errorf("IPFS routing mode %q is unsafe or ambiguous and disabled by default; set %s=%s to enable it intentionally", routing, AllowUnsafeIpfsRoutingEnv, ipfsUnsafeRoutingEnabled)
	default:
		return errors.Errorf("invalid IPFS routing mode %q; allowed values: autoclient, delegated, dhtclient, none; auto, custom, dht, and dhtserver require %s=%s", routing, AllowUnsafeIpfsRoutingEnv, ipfsUnsafeRoutingEnabled)
	}
}

func allowUnsafeIpfsRouting() bool {
	return strings.TrimSpace(os.Getenv(AllowUnsafeIpfsRoutingEnv)) == ipfsUnsafeRoutingEnabled ||
		strings.TrimSpace(os.Getenv(AllowIpfsDhtServerEnv)) == ipfsUnsafeRoutingEnabled
}

func applyValidationFlags(ctx *cli.Context, cfg *Config) {

}

func loadConfig(configPath string, conf *Config) error {
	if _, err := os.Stat(configPath); err != nil {
		return errors.Errorf("Config file cannot be found, path: %v", configPath)
	}

	jsonFile, err := os.Open(configPath)
	if err != nil {
		return errors.Errorf("Config file cannot be opened, path: %v", configPath)
	}
	defer jsonFile.Close()
	byteValue, err := io.ReadAll(jsonFile)
	if err != nil {
		return errors.Wrap(err, errors.Errorf("Cannot read JSON config, path: %v", configPath).Error())
	}
	if err := json.Unmarshal(byteValue, &conf); err != nil {
		return errors.Wrap(err, errors.Errorf("Cannot parse JSON config, path: %v", configPath).Error())
	}
	return nil
}
