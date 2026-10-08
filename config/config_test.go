package config

import (
	"encoding/hex"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/idena-network/idena-go/crypto"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli"
)

func TestApplyProfileDefaultsToDhtClientRouting(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	ctx := newTestContext(t)

	applyProfile(ctx, cfg)

	require.Equal(t, DefaultIpfsRouting, cfg.IpfsConf.Routing)
}

func TestDefaultIpfsBootstrapNodesIncludeRefreshedPeers(t *testing.T) {
	expected := []string{
		"/ip4/49.12.192.149/tcp/40405/ipfs/QmNqkSwad5HTShxVzFcYLQkRCRjrs9ZhQykqrRTQcdR7xp",
		"/ip4/147.91.144.55/tcp/40406/ipfs/Qmbas7yV5Z41n9ZvvuDoVpaqMWNPxwrk2jSYPsUx3hVgaF",
		"/ip4/147.91.144.55/tcp/40405/ipfs/QmdiNHGUWc72ouo92mEUnPVWMFvESDHshWVqLnyAutdN7Q",
		"/ip4/51.178.138.211/tcp/40405/ipfs/QmTseSBwV9xPN2iEn6ViZbdPbk5MBk1HAD9SKy8B2EgSrY",
		"/ip4/212.28.76.68/tcp/40415/ipfs/QmVMxHMU7pFf475gQRA158unEQCbmhJz6u3k81nuRueAdp",
		"/ip6/2a01:4f8:1c17:fd5a::1/tcp/40405/ipfs/QmRH67cpeq5gZ4iUSEgarrmNuFA9axEw1DnJdc3hWUtZ3T",
	}

	for _, peer := range expected {
		require.Contains(t, DefaultIpfsBootstrapNodes, peer)
	}
	require.Equal(t, len(DefaultIpfsBootstrapNodes), len(uniqueStrings(DefaultIpfsBootstrapNodes)))
}

func uniqueStrings(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func TestApplyProfilePreservesConfiguredIpfsRouting(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	cfg.IpfsConf.Routing = "dht"
	ctx := newTestContext(t)

	applyProfile(ctx, cfg)

	require.Equal(t, "dht", cfg.IpfsConf.Routing)
}

func TestApplyIpfsFlagsOverridesRouting(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	ctx := newTestContext(t)

	require.NoError(t, ctx.Set(IpfsRoutingFlag.Name, IpfsRoutingDht))
	applyIpfsFlags(ctx, cfg)

	require.Equal(t, IpfsRoutingDht, cfg.IpfsConf.Routing)
}

func TestMakeConfigRejectsInvalidIpfsRouting(t *testing.T) {
	ctx := newTestContext(t)
	require.NoError(t, ctx.Set(IpfsRoutingFlag.Name, "bogus"))

	_, err := MakeConfig(ctx, func(cfg *Config) {})

	require.ErrorContains(t, err, `invalid IPFS routing mode "bogus"`)
}

func TestMakeConfigRejectsServerCapableIpfsRoutingByDefault(t *testing.T) {
	ctx := newTestContext(t)
	require.NoError(t, ctx.Set(IpfsRoutingFlag.Name, IpfsRoutingDht))

	_, err := MakeConfig(ctx, func(cfg *Config) {})

	require.ErrorContains(t, err, `IPFS routing mode "dht" is unsafe or ambiguous`)
}

func TestMakeConfigAllowsServerCapableIpfsRoutingWithOptIn(t *testing.T) {
	t.Setenv(AllowUnsafeIpfsRoutingEnv, ipfsUnsafeRoutingEnabled)
	ctx := newTestContext(t)
	require.NoError(t, ctx.Set(IpfsRoutingFlag.Name, IpfsRoutingDht))

	cfg, err := MakeConfig(ctx, func(cfg *Config) {})

	require.NoError(t, err)
	require.Equal(t, IpfsRoutingDht, cfg.IpfsConf.Routing)
}

func TestValidateIpfsRoutingAllowsSafeKuboRoutingModes(t *testing.T) {
	for _, routing := range []string{
		"",
		IpfsRoutingAutoClient,
		IpfsRoutingDelegated,
		IpfsRoutingDhtClient,
		IpfsRoutingNone,
	} {
		require.NoError(t, validateIpfsRouting(routing))
	}
}

func TestValidateIpfsRoutingRejectsServerCapableModesByDefault(t *testing.T) {
	for _, routing := range []string{
		IpfsRoutingAuto,
		IpfsRoutingCustom,
		IpfsRoutingDht,
		IpfsRoutingDhtServer,
	} {
		require.ErrorContains(t, validateIpfsRouting(routing), "unsafe or ambiguous")
	}
}

func TestValidateIpfsRoutingAllowsServerCapableModesWithOptIn(t *testing.T) {
	t.Setenv(AllowUnsafeIpfsRoutingEnv, ipfsUnsafeRoutingEnabled)

	for _, routing := range []string{
		IpfsRoutingAuto,
		IpfsRoutingCustom,
		IpfsRoutingDht,
		IpfsRoutingDhtServer,
	} {
		require.NoError(t, validateIpfsRouting(routing))
	}
}

func TestValidateIpfsRoutingAllowsLegacyDhtServerOptIn(t *testing.T) {
	t.Setenv(AllowIpfsDhtServerEnv, ipfsUnsafeRoutingEnabled)

	require.NoError(t, validateIpfsRouting(IpfsRoutingDht))
}

func TestSetApiKeyCreatesPrivateFile(t *testing.T) {
	cfg := getDefaultConfig(t.TempDir())

	require.NoError(t, cfg.SetApiKey())

	require.NotEmpty(t, cfg.RPC.APIKey)
	assertPrivateApiKeyFile(t, filepath.Join(cfg.DataDir, apiKeyFileName))
}

func TestSetApiKeyTightensExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file mode bits")
	}

	cfg := getDefaultConfig(t.TempDir())
	apiKeyFile := filepath.Join(cfg.DataDir, apiKeyFileName)
	require.NoError(t, os.WriteFile(apiKeyFile, []byte("existing-key\n"), 0644))

	require.NoError(t, cfg.SetApiKey())

	require.Equal(t, "existing-key", cfg.RPC.APIKey)
	assertPrivateApiKeyFile(t, apiKeyFile)
}

func TestSetApiKeyTightensExistingFileWhenConfigured(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file mode bits")
	}

	cfg := getDefaultConfig(t.TempDir())
	cfg.RPC.APIKey = "configured-key"
	apiKeyFile := filepath.Join(cfg.DataDir, apiKeyFileName)
	require.NoError(t, os.WriteFile(apiKeyFile, []byte("old-key\n"), 0644))

	require.NoError(t, cfg.SetApiKey())

	data, err := os.ReadFile(apiKeyFile)
	require.NoError(t, err)
	require.Equal(t, cfg.RPC.APIKey, string(data))
	assertPrivateApiKeyFile(t, apiKeyFile)
}

func TestSetApiKeyRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}

	cfg := getDefaultConfig(t.TempDir())
	target := filepath.Join(cfg.DataDir, "target")
	apiKeyFile := filepath.Join(cfg.DataDir, apiKeyFileName)
	require.NoError(t, os.WriteFile(target, []byte("target-data"), 0600))
	require.NoError(t, os.Symlink(target, apiKeyFile))

	err := cfg.SetApiKey()

	require.ErrorContains(t, err, "not a regular file")
	data, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	require.Equal(t, []byte("target-data"), data)
}

func TestProvideNodeKeyCreatesUniqueBackups(t *testing.T) {
	const password = "test-password"
	cfg := getDefaultConfig(t.TempDir())
	originalKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	replacementKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	provide := func(keyBytes []byte, withBackup bool) {
		encrypted, err := crypto.Encrypt(keyBytes, password)
		require.NoError(t, err)
		require.NoError(t, cfg.ProvideNodeKey(hex.EncodeToString(encrypted), password, withBackup))
	}
	provide(crypto.FromECDSA(originalKey), false)
	provide(crypto.FromECDSA(replacementKey), true)
	provide(crypto.FromECDSA(originalKey), true)

	backups, err := filepath.Glob(filepath.Join(cfg.DataDir, "keystore", "backup-*"))
	require.NoError(t, err)
	require.Len(t, backups, 2)
	firstBackup, err := crypto.LoadECDSA(backups[0])
	require.NoError(t, err)
	secondBackup, err := crypto.LoadECDSA(backups[1])
	require.NoError(t, err)
	require.ElementsMatch(t,
		[][]byte{crypto.FromECDSA(originalKey), crypto.FromECDSA(replacementKey)},
		[][]byte{crypto.FromECDSA(firstBackup), crypto.FromECDSA(secondBackup)},
	)
}

func TestProvideNodeKeyDoesNotOverwriteMalformedExistingKey(t *testing.T) {
	const password = "test-password"
	cfg := getDefaultConfig(t.TempDir())
	keystoreDir := filepath.Join(cfg.DataDir, "keystore")
	require.NoError(t, os.MkdirAll(keystoreDir, 0700))
	keyfile := filepath.Join(keystoreDir, datadirPrivateKey)
	malformed := []byte("not-a-private-key")
	require.NoError(t, os.WriteFile(keyfile, malformed, 0600))
	replacementKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	encrypted, err := crypto.Encrypt(crypto.FromECDSA(replacementKey), password)
	require.NoError(t, err)

	for _, withBackup := range []bool{false, true} {
		err := cfg.ProvideNodeKey(hex.EncodeToString(encrypted), password, withBackup)
		require.ErrorContains(t, err, "failed to load existing key")
		data, readErr := os.ReadFile(keyfile)
		require.NoError(t, readErr)
		require.Equal(t, malformed, data)
	}
	backups, err := filepath.Glob(filepath.Join(keystoreDir, "backup-*"))
	require.NoError(t, err)
	require.Empty(t, backups)
}

func TestProvideNodeKeyRejectsTruncatedCiphertext(t *testing.T) {
	cfg := getDefaultConfig(t.TempDir())
	keyfile := filepath.Join(cfg.DataDir, "keystore", datadirPrivateKey)

	err := cfg.ProvideNodeKey("00", "password", false)

	require.ErrorIs(t, err, crypto.ErrInvalidCiphertext)
	require.NoFileExists(t, keyfile)
}

func TestNodeKeyTightensLegacyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file mode bits")
	}

	cfg := getDefaultConfig(t.TempDir())
	keystoreDir := filepath.Join(cfg.DataDir, "keystore")
	require.NoError(t, os.MkdirAll(keystoreDir, 0755))
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	keyfile := filepath.Join(keystoreDir, datadirPrivateKey)
	require.NoError(t, os.WriteFile(keyfile, []byte(hex.EncodeToString(crypto.FromECDSA(key))), 0644))

	loaded, err := cfg.NodeKey()
	require.NoError(t, err)
	require.Equal(t, crypto.FromECDSA(key), crypto.FromECDSA(loaded))
	dirInfo, err := os.Stat(keystoreDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())
	keyInfo, err := os.Stat(keyfile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), keyInfo.Mode().Perm())
}

func assertPrivateApiKeyFile(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func newTestContext(t *testing.T) *cli.Context {
	t.Helper()

	app := cli.NewApp()
	app.Flags = []cli.Flag{
		CfgFileFlag,
		DataDirFlag,
		IpfsRoutingFlag,
		ProfileFlag,
		DbWriteBufferFlag,
		IpfsWriteBufferFlag,
		MaxInboundOwnShardPeersFlag,
		MaxInboundPeersFlag,
		MaxOutboundOwnShardPeersFlag,
		MaxOutboundPeersFlag,
		DirectPeersFlag,
		IpfsLowWaterFlag,
		IpfsHighWaterFlag,
	}
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, f := range app.Flags {
		f.Apply(flagSet)
	}
	return cli.NewContext(app, flagSet, nil)
}

func TestDatabaseWriteBufferDefaultsToZero(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	require.Zero(t, cfg.DatabaseWriteBufferMiB())
	require.Zero(t, (&Config{}).DatabaseWriteBufferMiB())
}

func TestApplyDatabaseFlagsSetsWriteBuffer(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	ctx := newTestContext(t)

	require.NoError(t, ctx.Set(DbWriteBufferFlag.Name, "16"))
	applyDatabaseFlags(ctx, cfg)

	require.Equal(t, 16, cfg.DatabaseWriteBufferMiB())
}

func TestMakeMobileConfigReadsDatabaseWriteBuffer(t *testing.T) {
	cfg, err := MakeMobileConfig(t.TempDir(), `{"Database":{"WriteBufferMiB":32}}`)
	require.NoError(t, err)
	require.Equal(t, 32, cfg.DatabaseWriteBufferMiB())

	cfg, err = MakeMobileConfig(t.TempDir(), `{"IpfsConf":{"Profile":""}}`)
	require.NoError(t, err)
	require.Zero(t, cfg.DatabaseWriteBufferMiB())
}

func TestMakeConfigRejectsInvalidDatabaseWriteBuffer(t *testing.T) {
	for _, value := range []string{"-1", "257"} {
		ctx := newTestContext(t)
		require.NoError(t, ctx.Set(DbWriteBufferFlag.Name, value))

		_, err := MakeConfig(ctx, func(cfg *Config) {})

		require.ErrorContains(t, err, "invalid Database.WriteBufferMiB")
	}
}

func TestIpfsDatastoreWriteBufferDefaultsToZero(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	require.Zero(t, cfg.IpfsConf.DatastoreWriteBufferMiB)
}

func TestApplyIpfsFlagsSetsDatastoreWriteBuffer(t *testing.T) {
	cfg := getDefaultConfig(DefaultDataDir)
	ctx := newTestContext(t)

	require.NoError(t, ctx.Set(IpfsWriteBufferFlag.Name, "16"))
	applyIpfsFlags(ctx, cfg)

	require.Equal(t, 16, cfg.IpfsConf.DatastoreWriteBufferMiB)
}

func TestMakeMobileConfigReadsIpfsDatastoreWriteBuffer(t *testing.T) {
	cfg, err := MakeMobileConfig(t.TempDir(), `{"IpfsConf":{"Profile":"","DatastoreWriteBufferMiB":32}}`)
	require.NoError(t, err)
	require.Equal(t, 32, cfg.IpfsConf.DatastoreWriteBufferMiB)

	cfg, err = MakeMobileConfig(t.TempDir(), `{"IpfsConf":{"Profile":""}}`)
	require.NoError(t, err)
	require.Zero(t, cfg.IpfsConf.DatastoreWriteBufferMiB)
}

func TestMakeConfigRejectsInvalidIpfsDatastoreWriteBuffer(t *testing.T) {
	for _, value := range []string{"-1", "257"} {
		ctx := newTestContext(t)
		require.NoError(t, ctx.Set(IpfsWriteBufferFlag.Name, value))

		_, err := MakeConfig(ctx, func(cfg *Config) {})

		require.ErrorContains(t, err, "invalid IpfsConf.DatastoreWriteBufferMiB")
	}

	_, err := MakeMobileConfig(t.TempDir(), `{"IpfsConf":{"DatastoreWriteBufferMiB":300}}`)
	require.ErrorContains(t, err, "invalid IpfsConf.DatastoreWriteBufferMiB")
}

func TestMakeConfigKeepsProfilePeerLimitsWithoutFlags(t *testing.T) {
	cfg, err := MakeConfig(newTestContext(t), func(cfg *Config) {})
	require.NoError(t, err)

	require.Equal(t, DefaultMaxInboundOwnShardPeers, cfg.P2P.MaxInboundOwnShardPeers)
	require.Equal(t, DefaultMaxInboundNotOwnShardPeers, cfg.P2P.MaxInboundPeers)
	require.Equal(t, DefaultMaxOutboundOwnShardPeers, cfg.P2P.MaxOutboundOwnShardPeers)
	require.Equal(t, DefaultMaxOutboundNotOwnShardPeers, cfg.P2P.MaxOutboundPeers)
	require.Equal(t, 30, cfg.IpfsConf.LowWater)
	require.Equal(t, 50, cfg.IpfsConf.HighWater)
}

func TestMakeConfigKeepsLowPowerProfileWithoutFlags(t *testing.T) {
	// The flags show the default profile's values: unset, they leave another profile's values.
	ctx := newTestContext(t)
	require.NoError(t, ctx.Set(ProfileFlag.Name, LowPowerProfile))

	cfg, err := MakeConfig(ctx, func(cfg *Config) {})
	require.NoError(t, err)

	require.Equal(t, LowPowerMaxInboundOwnShardPeers, cfg.P2P.MaxInboundOwnShardPeers)
	require.Equal(t, LowPowerMaxInboundNotOwnShardPeers, cfg.P2P.MaxInboundPeers)
	require.Equal(t, LowPowerMaxOutboundOwnShardPeers, cfg.P2P.MaxOutboundOwnShardPeers)
	require.Equal(t, LowPowerMaxOutboundNotOwnShardPeers, cfg.P2P.MaxOutboundPeers)
	require.Equal(t, 8, cfg.IpfsConf.LowWater)
	require.Equal(t, 10, cfg.IpfsConf.HighWater)
}

func TestMakeConfigAppliesPeerLimitFlagsOverTheProfile(t *testing.T) {
	for _, profile := range []string{"", LowPowerProfile, SharedNodeProfile, DefaultProfile} {
		ctx := newTestContext(t)
		if profile != "" {
			require.NoError(t, ctx.Set(ProfileFlag.Name, profile))
		}
		require.NoError(t, ctx.Set(MaxInboundOwnShardPeersFlag.Name, "16"))
		require.NoError(t, ctx.Set(MaxInboundPeersFlag.Name, "8"))
		require.NoError(t, ctx.Set(MaxOutboundOwnShardPeersFlag.Name, "3"))
		require.NoError(t, ctx.Set(MaxOutboundPeersFlag.Name, "1"))
		require.NoError(t, ctx.Set(IpfsLowWaterFlag.Name, "50"))
		require.NoError(t, ctx.Set(IpfsHighWaterFlag.Name, "100"))

		cfg, err := MakeConfig(ctx, func(cfg *Config) {})
		require.NoError(t, err, profile)

		require.Equal(t, 16, cfg.P2P.MaxInboundOwnShardPeers, profile)
		require.Equal(t, 8, cfg.P2P.MaxInboundPeers, profile)
		require.Equal(t, 3, cfg.P2P.MaxOutboundOwnShardPeers, profile)
		require.Equal(t, 1, cfg.P2P.MaxOutboundPeers, profile)
		require.Equal(t, 50, cfg.IpfsConf.LowWater, profile)
		require.Equal(t, 100, cfg.IpfsConf.HighWater, profile)
	}
}

func TestMakeConfigAppliesOnlyThePeerLimitFlagsSet(t *testing.T) {
	ctx := newTestContext(t)
	require.NoError(t, ctx.Set(MaxInboundOwnShardPeersFlag.Name, "16"))
	require.NoError(t, ctx.Set(IpfsHighWaterFlag.Name, "0"))

	cfg, err := MakeConfig(ctx, func(cfg *Config) {})
	require.NoError(t, err)

	require.Equal(t, 16, cfg.P2P.MaxInboundOwnShardPeers)
	require.Equal(t, DefaultMaxInboundNotOwnShardPeers, cfg.P2P.MaxInboundPeers)
	require.Equal(t, DefaultMaxOutboundOwnShardPeers, cfg.P2P.MaxOutboundOwnShardPeers)
	require.Equal(t, DefaultMaxOutboundNotOwnShardPeers, cfg.P2P.MaxOutboundPeers)
	// HighWater 0: the connection manager keeps every connection.
	require.Equal(t, 30, cfg.IpfsConf.LowWater)
	require.Zero(t, cfg.IpfsConf.HighWater)
}

func TestMakeConfigRejectsInvalidPeerLimits(t *testing.T) {
	for _, f := range []cli.IntFlag{MaxInboundOwnShardPeersFlag, MaxInboundPeersFlag, MaxOutboundOwnShardPeersFlag, MaxOutboundPeersFlag} {
		ctx := newTestContext(t)
		require.NoError(t, ctx.Set(f.Name, "-1"))

		_, err := MakeConfig(ctx, func(cfg *Config) {})

		require.ErrorContains(t, err, "must not be negative", f.Name)
	}
}

func TestMakeConfigRejectsInvalidIpfsConnectionLimits(t *testing.T) {
	ctx := newTestContext(t)
	require.NoError(t, ctx.Set(IpfsLowWaterFlag.Name, "-1"))
	_, err := MakeConfig(ctx, func(cfg *Config) {})
	require.ErrorContains(t, err, "must not be negative")

	ctx = newTestContext(t)
	require.NoError(t, ctx.Set(IpfsLowWaterFlag.Name, "60"))
	require.NoError(t, ctx.Set(IpfsHighWaterFlag.Name, "50"))
	_, err = MakeConfig(ctx, func(cfg *Config) {})
	require.ErrorContains(t, err, "must not be above HighWater")

	_, err = MakeMobileConfig(t.TempDir(), `{"IpfsConf":{"Profile":"","LowWater":12,"HighWater":10}}`)
	require.ErrorContains(t, err, "must not be above HighWater")
	_, err = MakeMobileConfig(t.TempDir(), `{"P2P":{"MaxInboundPeers":-1}}`)
	require.ErrorContains(t, err, "must not be negative")
}

func TestMakeMobileConfigReadsPeerAndIpfsConnectionLimits(t *testing.T) {
	cfg, err := MakeMobileConfig(t.TempDir(), `{"P2P":{"MaxInboundOwnShardPeers":16,"MaxInboundPeers":8,"MaxOutboundOwnShardPeers":4,"MaxOutboundPeers":2},"IpfsConf":{"Profile":"","LowWater":50,"HighWater":100}}`)
	require.NoError(t, err)

	require.Equal(t, 16, cfg.P2P.MaxInboundOwnShardPeers)
	require.Equal(t, 8, cfg.P2P.MaxInboundPeers)
	require.Equal(t, 4, cfg.P2P.MaxOutboundOwnShardPeers)
	require.Equal(t, 2, cfg.P2P.MaxOutboundPeers)
	require.Equal(t, 50, cfg.IpfsConf.LowWater)
	require.Equal(t, 100, cfg.IpfsConf.HighWater)
	require.Equal(t, DefaultIpfsPort, cfg.IpfsConf.IpfsPort)
}
