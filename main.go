package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof" // REPLAY TEST HARNESS ONLY: registers /debug/pprof handlers
	"os"
	"path/filepath"
	"runtime"

	"github.com/coreos/go-semver/semver"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/database"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/node"
	"github.com/urfave/cli"
)

const (
	VersionFile = "version"
	LogDir      = "logs"
	ChainDir    = "idenachain.db"
)

var (
	version = "0.0.1"
)

func main() {
	app := cli.NewApp()
	app.Version = version

	app.Flags = []cli.Flag{
		config.CfgFileFlag,
		config.DataDirFlag,
		config.TcpPortFlag,
		config.RpcHostFlag,
		config.RpcPortFlag,
		config.BootNodeFlag,
		config.AutomineFlag,
		config.IpfsBootNodeFlag,
		config.IpfsPortFlag,
		config.IpfsRoutingFlag,
		config.NoDiscoveryFlag,
		config.VerbosityFlag,
		config.GodAddressFlag,
		config.CeremonyTimeFlag,
		config.MaxNetworkDelayFlag,
		config.FastSyncFlag,
		config.ForceFullSyncFlag,
		config.ProfileFlag,
		config.IpfsPortStaticFlag,
		config.ApiKeyFlag,
		config.LogFileSizeFlag,
		config.LogColoring,
		config.AutoOnline,
	}

	app.Action = func(context *cli.Context) error {
		logLvl := log.Lvl(context.Int(config.VerbosityFlag.Name))
		logFileSize := context.Int(config.LogFileSizeFlag.Name)

		useLogColor := true
		if runtime.GOOS == "windows" {
			useLogColor = context.Bool(config.LogColoring.Name)
		}

		handler := log.LvlFilterHandler(logLvl, log.StreamHandler(os.Stdout, log.TerminalFormat(useLogColor)))

		log.Root().SetHandler(handler)

		// REPLAY TEST HARNESS ONLY: expose net/http/pprof when IDENA_PPROF_ADDR is set
		// (e.g. 127.0.0.1:6060). Local debugging aid; never enabled in production.
		if pprofAddr := os.Getenv("IDENA_PPROF_ADDR"); pprofAddr != "" {
			go func() {
				log.Info("HARNESS: pprof endpoint listening", "addr", pprofAddr)
				if err := http.ListenAndServe(pprofAddr, nil); err != nil {
					log.Error("HARNESS: pprof endpoint stopped", "err", err)
				}
			}()
		}

		var consensusVersionErr error
		cfg, err := config.MakeConfig(context, func(cfg *config.Config) {
			db, err := node.OpenDatabase(cfg.DataDir, "idenachain", 16, 16, false)
			if err != nil {
				consensusVersionErr = fmt.Errorf("open chain database: %w", err)
				log.Error("Cannot transform consensus config", "err", err)
				return
			}
			defer db.Close()
			repo := database.NewRepo(db)
			consVersion, err := repo.ReadConsensusVersionWithError()
			if err != nil {
				consensusVersionErr = fmt.Errorf("read consensus version: %w", err)
				return
			}
			if consVersion <= uint32(cfg.Consensus.Version) {
				return
			}
			for v := cfg.Consensus.Version + 1; v <= config.ConsensusVerson(consVersion); v++ {
				config.ApplyConsensusVersion(v, cfg.Consensus)
			}
			log.Info("Consensus config transformed to", "ver", consVersion)
		})

		if err != nil {
			return err
		}
		if consensusVersionErr != nil {
			return consensusVersionErr
		}
		/*
			err = dropOldDirOnFork(cfg)
			if err != nil {
				return err
			} */

		fileHandler, err := getLogFileHandler(cfg, logFileSize)

		if err != nil {
			return err
		}

		log.Root().SetHandler(log.LvlFilterHandler(logLvl, log.MultiHandler(handler, fileHandler)))

		log.Info("Idena node is starting", "version", version)

		n, err := node.NewNode(cfg, version)
		if err != nil {
			return err
		}
		if err := n.Start(); err != nil {
			return err
		}
		n.WaitForStop()
		return nil
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func getLogFileHandler(cfg *config.Config, logFileSize int) (log.Handler, error) {
	path := filepath.Join(cfg.DataDir, LogDir)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
	}

	return log.RotatingFileHandler(filepath.Join(path, "output.log"), uint32(logFileSize*1024), log.TerminalFormat(false))
}

func dropOldDirOnFork(cfg *config.Config) error {
	path := filepath.Join(cfg.DataDir, VersionFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return writeVersion(cfg)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	current := semver.New(version)
	old := semver.New(string(b))

	if old.Major < current.Major || old.Minor < current.Minor {
		log.Info("Network fork, removing db and logs folder...")
		err = os.RemoveAll(filepath.Join(cfg.DataDir, ChainDir))
		if err != nil {
			return err
		}
		err = os.RemoveAll(filepath.Join(cfg.DataDir, LogDir))
		if err != nil {
			return err
		}
	}

	if old.LessThan(*current) {
		return writeVersion(cfg)
	}
	return nil
}

func writeVersion(cfg *config.Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cfg.DataDir, VersionFile), []byte(version), 0600)
}
