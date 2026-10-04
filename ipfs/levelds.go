//go:build !idena_memory_ipfs

package ipfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	levelds "github.com/ipfs/go-ds-leveldb"
	ipfsConf "github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/plugin"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/ipfs/kubo/repo"
	"github.com/ipfs/kubo/repo/fsrepo"
	ldbopts "github.com/syndtr/goleveldb/leveldb/opt"
)

// kuboLevelDsPlugin is the name of kubo's preloaded LevelDB datastore plugin, which leveldsPlugin replaces.
const kuboLevelDsPlugin = "ds-level"

// leveldsPlugin is kubo's LevelDB datastore plugin (plugin/plugins/levelds, kubo v0.42.0) with a write buffer:
// kubo's only takes the compression. It registers the same datastore type with the same disk spec, so a repo
// created by kubo's plugin opens unchanged, and back.
type leveldsPlugin struct {
	writeBufferMiB int
}

var _ plugin.PluginDatastore = (*leveldsPlugin)(nil)

func (*leveldsPlugin) Name() string {
	return "idena-ds-level"
}

func (*leveldsPlugin) Version() string {
	return "0.1.0"
}

func (*leveldsPlugin) Init(_ *plugin.Environment) error {
	return nil
}

func (*leveldsPlugin) DatastoreTypeName() string {
	return "levelds"
}

type leveldsConfig struct {
	path           string
	compression    ldbopts.Compression
	writeBufferMiB int
}

func (p *leveldsPlugin) DatastoreConfigParser() fsrepo.ConfigFromMap {
	return func(params map[string]any) (fsrepo.DatastoreConfig, error) {
		c := leveldsConfig{writeBufferMiB: p.writeBufferMiB}
		var ok bool

		c.path, ok = params["path"].(string)
		if !ok {
			return nil, fmt.Errorf("'path' field is missing or not string")
		}

		switch cm := params["compression"]; cm {
		case "none":
			c.compression = ldbopts.NoCompression
		case "snappy":
			c.compression = ldbopts.SnappyCompression
		case "", nil:
			c.compression = ldbopts.DefaultCompression
		default:
			return nil, fmt.Errorf("unrecognized value for compression: %s", cm)
		}

		return &c, nil
	}
}

// DiskSpec leaves out the write buffer: it is an open-time option, not stored on disk.
func (c *leveldsConfig) DiskSpec() fsrepo.DiskSpec {
	return map[string]any{
		"type": "levelds",
		"path": c.path,
	}
}

func (c *leveldsConfig) Create(path string) (repo.Datastore, error) {
	p := c.path
	if !filepath.IsAbs(p) {
		p = filepath.Join(path, p)
	}

	return levelds.NewDatastore(p, &levelds.Options{
		Compression: c.compression,
		WriteBuffer: c.writeBufferMiB * ldbopts.MiB, // 0: goleveldb's default (4 MiB)
	})
}

// newPluginLoader returns kubo's plugin loader for loaderRepo with kubo's LevelDB datastore plugin disabled, so that
// leveldsPlugin can register the datastore type. The loader reads the disabled plugins from <loaderRepo>/config, of
// which it reads only the Plugins section, while it is created: the file is written for that moment and removed,
// because an older idena-go reads the same file and would then have no LevelDB plugin at all (after a downgrade
// the node could not open its IPFS repo).
func newPluginLoader(loaderRepo string) (*loader.PluginLoader, error) {
	if err := os.MkdirAll(loaderRepo, 0o700); err != nil {
		return nil, err
	}
	file := filepath.Join(loaderRepo, ipfsConf.DefaultConfigFile)
	data, err := json.MarshalIndent(struct{ Plugins ipfsConf.Plugins }{ipfsConf.Plugins{
		Plugins: map[string]ipfsConf.Plugin{kuboLevelDsPlugin: {Disabled: true}},
	}}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return nil, err
	}
	plugins, err := loader.NewPluginLoader(loaderRepo)
	if removeErr := os.Remove(file); removeErr != nil && err == nil {
		err = removeErr
	}
	// The directory too when it holds nothing else, as before.
	_ = os.Remove(loaderRepo)
	return plugins, err
}
