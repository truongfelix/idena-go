//go:build !idena_memory_ipfs

package ipfs

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/tink/go/subtle/random"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	ipfsConf "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/stretchr/testify/require"
)

var proxy Proxy

func init() {
	var err error
	dataDir, err := os.MkdirTemp("", "idena-ipfs-test-*")
	if err != nil {
		panic(err)
	}
	proxy, err = NewIpfsProxy(&config.IpfsConfig{
		SwarmKey:    "9ad6f96bb2b02a7308ad87938d6139a974b550cc029ce416641a60c46db2f530",
		BootNodes:   []string{},
		IpfsPort:    4012,
		DataDir:     dataDir,
		GracePeriod: "20s",
	}, eventbus.New())
	if err != nil {
		panic(err)
	}
}

func TestIpfsProxy_Cid(t *testing.T) {

	require := require.New(t)
	var data []byte

	proxy := ipfsProxy{}
	cid, err := proxy.Cid(data)

	require.Nil(err)
	require.Equal(EmptyCid, cid)
}

func TestIpfsProxyCidMatchesLegacyNode(t *testing.T) {
	cache := proxy.(*ipfsProxy).cidCache
	cache.Flush()
	t.Cleanup(cache.Flush)

	c, err := proxy.Cid([]byte("idena-legacy-cid-vector"))
	require.NoError(t, err)
	require.Equal(t, "bafkreidepbxikudfkop2lvelq6domhvde445qqfvmufp2pg3cckcclujfe", c.String())
}

func TestWriteSwarmKeyCreatesPrivateFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file mode bits")
	}

	dataDir := t.TempDir()

	require.NoError(t, writeSwarmKey(dataDir, "9ad6f96bb2b02a7308ad87938d6139a974b550cc029ce416641a60c46db2f530"))

	info, err := os.Stat(filepath.Join(dataDir, "swarm.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestWriteSwarmKeyReportsWriteFailure(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dataDir, "swarm.key"), 0700))

	err := writeSwarmKey(dataDir, "9ad6f96bb2b02a7308ad87938d6139a974b550cc029ce416641a60c46db2f530")
	require.ErrorContains(t, err, "failed to persist IPFS swarm key")
}

func TestWriteSwarmKeyRejectsMalformedKey(t *testing.T) {
	dataDir := t.TempDir()

	err := writeSwarmKey(dataDir, "not-a-32-byte-hex-key")
	require.ErrorContains(t, err, "32-byte hexadecimal string")
	require.NoFileExists(t, filepath.Join(dataDir, "swarm.key"))
}

func TestWriteSwarmKeyRestrictsExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file mode bits")
	}

	dataDir := t.TempDir()
	swarmPath := filepath.Join(dataDir, "swarm.key")
	require.NoError(t, os.WriteFile(swarmPath, []byte("old"), 0644))

	require.NoError(t, writeSwarmKey(dataDir, "9ad6f96bb2b02a7308ad87938d6139a974b550cc029ce416641a60c46db2f530"))
	info, err := os.Stat(swarmPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestConfigureIpfsUsesFlatfsForNewRepo(t *testing.T) {
	dataDir := t.TempDir()

	configured, err := configureIpfs(testIpfsConfig(dataDir), eventbus.New())
	require.NoError(t, err)
	require.Equal(t, "mount", configured.Datastore.Spec["type"])
	require.True(t, datastoreHasType(configured.Datastore.Spec, "flatfs"))
	require.Equal(t, ipfsConf.False, configured.AutoConf.Enabled)
	require.Equal(t, ipfsConf.False, configured.Swarm.Transports.Network.Websocket)
	require.Equal(t, ipfsConf.False, configured.AutoTLS.Enabled)
	require.True(t, configured.Provide.Enabled.WithDefault(ipfsConf.DefaultProvideEnabled))

	locked, err := fsrepo.LockedByOtherProcess(dataDir)
	require.NoError(t, err)
	require.False(t, locked)
}

func TestConfigureIpfsDisablesProvidingWhenReproviderIntervalIsZero(t *testing.T) {
	dataDir := t.TempDir()
	cfg := testIpfsConfig(dataDir)
	cfg.ReproviderInterval = "0"

	configured, err := configureIpfs(cfg, eventbus.New())
	require.NoError(t, err)
	require.Equal(t, ipfsConf.False, configured.Provide.Enabled)
	require.Zero(t, configured.Provide.DHT.Interval.WithDefault(ipfsConf.DefaultProvideDHTInterval))
	require.NoError(t, ipfsConf.ValidateProvideConfig(&configured.Provide))
}

func TestConfigureIpfsPreservesMalformedExistingConfig(t *testing.T) {
	dataDir := t.TempDir()
	initialConfig, err := ipfsConf.Init(io.Discard, 2048)
	require.NoError(t, err)
	require.NoError(t, fsrepo.Init(dataDir, initialConfig))

	configFile, err := ipfsConf.Filename(dataDir, "")
	require.NoError(t, err)
	malformedConfig := []byte(`{"Identity":`)
	require.NoError(t, os.WriteFile(configFile, malformedConfig, 0600))

	configured, err := configureIpfs(testIpfsConfig(dataDir), eventbus.New())
	require.Nil(t, configured)
	require.ErrorContains(t, err, "refusing automatic replacement")

	storedConfig, readErr := os.ReadFile(configFile)
	require.NoError(t, readErr)
	require.Equal(t, malformedConfig, storedConfig)
}

func TestGetNodeConfigReportsOpenFailure(t *testing.T) {
	_, err := getNodeConfig(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func testIpfsConfig(dataDir string) *config.IpfsConfig {
	return &config.IpfsConfig{
		DataDir:     dataDir,
		BootNodes:   []string{},
		SwarmKey:    "9ad6f96bb2b02a7308ad87938d6139a974b550cc029ce416641a60c46db2f530",
		GracePeriod: "20s",
	}
}

func datastoreHasType(spec map[string]any, datastoreType string) bool {
	if spec["type"] == datastoreType {
		return true
	}
	for _, child := range spec {
		switch value := child.(type) {
		case map[string]any:
			if datastoreHasType(value, datastoreType) {
				return true
			}
		case []any:
			for _, item := range value {
				if itemSpec, ok := item.(map[string]any); ok && datastoreHasType(itemSpec, datastoreType) {
					return true
				}
			}
		}
	}
	return false
}

func TestIpfsProxy_Get_Cid(t *testing.T) {
	require := require.New(t)

	cid, _ := proxy.Cid([]byte{0x1})
	cid2, _ := proxy.Cid([]byte{0x1})

	require.Equal(cid.Bytes(), cid2.Bytes())
	p := proxy.(*ipfsProxy)
	require.Len(p.cidCache.Items(), 1)

	cases := []int{1, 100, 500, 1024, 10000, 50000, 100000, 220000, 280000, 350000, 500000, 10000000}

	for _, item := range cases {
		data := random.GetRandomBytes(uint32(item))

		cid, err := proxy.Add(data, false)

		require.NoError(err)

		cid2, err = proxy.Add(data, true)
		require.NoError(err)

		require.Equal(cid.String(), cid2.String())

		localCid, err := proxy.Cid(data)

		require.NoError(err)

		require.Equal(cid.Bytes(), localCid.Bytes(), "n: %v", item)

		data2, err := proxy.Get(cid.Bytes(), Block)
		require.NoError(err)

		require.Equal(data, data2)
	}
}

func TestIpfsProxy_Get_Limit(t *testing.T) {
	require := require.New(t)

	data := random.GetRandomBytes(uint32(common.MaxFlipSize))
	cid, err := proxy.Add(data, false)
	require.NoError(err)

	_, err = proxy.Get(cid.Bytes(), Flip)
	require.NoError(err)

	_, err = proxy.Get(cid.Bytes(), Profile)
	require.NoError(err)

	_, err = proxy.Get(cid.Bytes(), Block)
	require.NoError(err)

	data = random.GetRandomBytes(uint32(common.MaxProfileSize))
	cid, err = proxy.Add(data, false)
	require.NoError(err)

	_, err = proxy.Get(cid.Bytes(), Flip)
	require.Equal(TooBigErr, err)

	_, err = proxy.Get(cid.Bytes(), Profile)
	require.NoError(err)

	data = random.GetRandomBytes(uint32(common.MaxProfileSize + 1))
	cid, err = proxy.Add(data, false)
	require.NoError(err)

	_, err = proxy.Get(cid.Bytes(), Flip)
	require.Equal(TooBigErr, err)

	_, err = proxy.Get(cid.Bytes(), Profile)
	require.Equal(TooBigErr, err)
	_, err = proxy.Get(cid.Bytes(), Block)
	require.NoError(err)
}
