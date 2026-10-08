package ipfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/idena-network/idena-go/log"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/ipfs/kubo/repo/fsrepo/migrations"
	"github.com/pkg/errors"
	"os"
	"path/filepath"
)

// The official node leaves its IPFS repository at version 12 (go-ipfs/kubo 0.15), with a config that kubo 0.42
// refuses to decode (Experimental.AcceleratedDHTClient). Kubo 0.42 runs the 16-to-17 and 17-to-18 migrations
// itself and would download the older ones from the IPFS network, which Idena's private swarm cannot reach, so
// versions 12 to 15 are converted here. Those four migrations (fs-repo-12-to-13 ... 15-to-16) change only the
// config file, and of their changes an Idena config needs one: the AcceleratedDHTClient key moved to Routing
// (13-to-14). The others rewrite QUIC addresses, which an Idena config does not hold, and kubo defaults that
// configureIpfs sets on every start.
const (
	officialRepoVersion           = 12
	firstEmbeddedMigrationVersion = 16
)

// upgradeLegacyRepo brings an existing repository to the version kubo expects, keeping its identity and data.
// The config as it was is kept next to it as config.v<version>.bak.
func upgradeLegacyRepo(repoDir string) error {
	version, err := migrations.RepoVersion(repoDir)
	if err != nil {
		return err
	}
	if version >= fsrepo.RepoVersion {
		return nil
	}
	if version < officialRepoVersion {
		return fmt.Errorf("IPFS repository version %d is older than %d: not upgraded", version, officialRepoVersion)
	}
	if version < firstEmbeddedMigrationVersion {
		if err := convertLegacyConfig(repoDir, version); err != nil {
			return err
		}
		if err := migrations.WriteRepoVersion(repoDir, firstEmbeddedMigrationVersion); err != nil {
			return err
		}
		log.Info("IPFS repository converted", "from", version, "to", firstEmbeddedMigrationVersion)
	}
	if err := migrations.RunEmbeddedMigrations(context.Background(), fsrepo.RepoVersion, repoDir, false); err != nil {
		return err
	}
	log.Info("IPFS repository upgraded", "from", version, "to", fsrepo.RepoVersion)
	return nil
}

func convertLegacyConfig(repoDir string, version int) error {
	configFile := filepath.Join(repoDir, "config")
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	backupFile := fmt.Sprintf("%s.v%d.bak", configFile, version)
	if _, err := os.Stat(backupFile); os.IsNotExist(err) {
		if err := writeFileAtomic(backupFile, raw); err != nil {
			return errors.Wrap(err, "cannot back up the IPFS config")
		}
	} else if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var conf map[string]any
	if err := decoder.Decode(&conf); err != nil {
		return errors.Wrap(err, "cannot decode the IPFS config")
	}
	if err := moveAcceleratedDHTClient(conf); err != nil {
		return err
	}
	converted, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(configFile, append(converted, '\n'))
}

// moveAcceleratedDHTClient applies fs-repo-13-to-14: Experimental.AcceleratedDHTClient becomes
// Routing.AcceleratedDHTClient, unless Routing already sets it.
func moveAcceleratedDHTClient(conf map[string]any) error {
	experimental, ok := conf["Experimental"]
	if !ok {
		return nil
	}
	experimentalMap, ok := experimental.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid type for .Experimental: %T", experimental)
	}
	value, ok := experimentalMap["AcceleratedDHTClient"]
	if !ok {
		return nil
	}
	enabled, ok := value.(bool)
	if !ok {
		return fmt.Errorf("invalid type for .Experimental.AcceleratedDHTClient: %T", value)
	}
	routingMap := map[string]any{}
	if routing, ok := conf["Routing"]; ok && routing != nil {
		if routingMap, ok = routing.(map[string]any); !ok {
			return fmt.Errorf("invalid type for .Routing: %T", routing)
		}
	}

	delete(experimentalMap, "AcceleratedDHTClient")
	if _, ok := routingMap["AcceleratedDHTClient"]; !ok {
		routingMap["AcceleratedDHTClient"] = enabled
	}
	conf["Routing"] = routingMap
	return nil
}

func writeFileAtomic(file string, data []byte) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
