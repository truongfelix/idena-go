//go:build !idena_memory_ipfs

package ipfs

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/idena-network/idena-go/common/eventbus"
	ipfsConf "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/stretchr/testify/require"
)

// newOfficialRepo makes a repository as the official node leaves it: version 12, its config (testdata, taken
// from a v1.1.2 node; identity blanked there) and its Badger datastore. It returns the config bytes and peer id.
func newOfficialRepo(t *testing.T, dataDir string) ([]byte, string) {
	initialConfig, err := ipfsConf.Init(io.Discard, 2048)
	require.NoError(t, err)

	fixture, err := os.ReadFile(filepath.Join("testdata", "official-v12-config.json"))
	require.NoError(t, err)
	var official map[string]any
	require.NoError(t, json.Unmarshal(fixture, &official))
	official["Identity"] = map[string]any{
		"PeerID":  initialConfig.Identity.PeerID,
		"PrivKey": initialConfig.Identity.PrivKey,
	}

	datastore, err := json.Marshal(official["Datastore"])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(datastore, &initialConfig.Datastore))
	require.NoError(t, fsrepo.Init(dataDir, initialConfig))

	officialConfig, err := json.MarshalIndent(official, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "config"), officialConfig, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "version"), []byte("12\n"), 0600))
	return officialConfig, initialConfig.Identity.PeerID
}

func TestConfigureIpfsUpgradesOfficialRepo(t *testing.T) {
	dataDir := t.TempDir()
	officialConfig, peerID := newOfficialRepo(t, dataDir)

	configured, err := configureIpfs(testIpfsConfig(dataDir), eventbus.New())
	require.NoError(t, err)
	require.Equal(t, peerID, configured.Identity.PeerID)
	require.False(t, configured.Routing.AcceleratedDHTClient.WithDefault(true))

	version, err := os.ReadFile(filepath.Join(dataDir, "version"))
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(fsrepo.RepoVersion), string(version))
	backup, err := os.ReadFile(filepath.Join(dataDir, "config.v12.bak"))
	require.NoError(t, err)
	require.Equal(t, officialConfig, backup)

	// A second start finds the current version and changes nothing.
	configured, err = configureIpfs(testIpfsConfig(dataDir), eventbus.New())
	require.NoError(t, err)
	require.Equal(t, peerID, configured.Identity.PeerID)
	backup, err = os.ReadFile(filepath.Join(dataDir, "config.v12.bak"))
	require.NoError(t, err)
	require.Equal(t, officialConfig, backup)
}

func TestConfigureIpfsRefusesRepoOlderThanOfficial(t *testing.T) {
	dataDir := t.TempDir()
	officialConfig, _ := newOfficialRepo(t, dataDir)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "version"), []byte("11\n"), 0600))

	configured, err := configureIpfs(testIpfsConfig(dataDir), eventbus.New())
	require.Nil(t, configured)
	require.ErrorContains(t, err, "IPFS repository version 11 is older than 12")

	storedConfig, err := os.ReadFile(filepath.Join(dataDir, "config"))
	require.NoError(t, err)
	require.Equal(t, officialConfig, storedConfig)
	require.NoFileExists(t, filepath.Join(dataDir, "config.v11.bak"))
}

func TestMoveAcceleratedDHTClient(t *testing.T) {
	cases := []struct {
		name    string
		conf    string
		want    string
		wantErr string
	}{
		{name: "no Experimental", conf: `{"Routing":{"Type":"dht"}}`, want: `{"Routing":{"Type":"dht"}}`},
		{name: "no key", conf: `{"Experimental":{"FilestoreEnabled":true}}`,
			want: `{"Experimental":{"FilestoreEnabled":true}}`},
		{name: "off", conf: `{"Experimental":{"AcceleratedDHTClient":false},"Routing":{"Type":"dht"}}`,
			want: `{"Experimental":{},"Routing":{"AcceleratedDHTClient":false,"Type":"dht"}}`},
		{name: "on, no Routing", conf: `{"Experimental":{"AcceleratedDHTClient":true}}`,
			want: `{"Experimental":{},"Routing":{"AcceleratedDHTClient":true}}`},
		{name: "on, Routing null", conf: `{"Experimental":{"AcceleratedDHTClient":true},"Routing":null}`,
			want: `{"Experimental":{},"Routing":{"AcceleratedDHTClient":true}}`},
		{name: "Routing already set", conf: `{"Experimental":{"AcceleratedDHTClient":true},"Routing":{"AcceleratedDHTClient":false}}`,
			want: `{"Experimental":{},"Routing":{"AcceleratedDHTClient":false}}`},
		{name: "not a bool", conf: `{"Experimental":{"AcceleratedDHTClient":"yes"}}`,
			wantErr: "invalid type for .Experimental.AcceleratedDHTClient"},
		{name: "Experimental not a map", conf: `{"Experimental":[]}`, wantErr: "invalid type for .Experimental"},
		{name: "Routing not a map", conf: `{"Experimental":{"AcceleratedDHTClient":true},"Routing":"dht"}`,
			wantErr: "invalid type for .Routing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var conf map[string]any
			require.NoError(t, json.Unmarshal([]byte(c.conf), &conf))
			err := moveAcceleratedDHTClient(conf)
			if c.wantErr != "" {
				require.ErrorContains(t, err, c.wantErr)
				return
			}
			require.NoError(t, err)
			got, err := json.Marshal(conf)
			require.NoError(t, err)
			require.JSONEq(t, c.want, string(got))
		})
	}
}
