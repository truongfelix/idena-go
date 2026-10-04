package config

import "github.com/pkg/errors"

// MaxDatabaseWriteBufferMiB bounds DatabaseConfig.WriteBufferMiB and IpfsConfig.DatastoreWriteBufferMiB.
const MaxDatabaseWriteBufferMiB = 256

// DatabaseConfig holds the chain database (LevelDB) settings.
type DatabaseConfig struct {
	// WriteBufferMiB is the chain database's write buffer (memtable) in MiB; 0 keeps the default (4 MiB).
	// A bigger buffer flushes fewer level-0 tables, so compactions rewrite less data per block; it costs
	// about 2-4 times the added buffer in memory (memtable index, the memtable being flushed, Go heap).
	// It applies when the database is opened: a change takes a restart.
	WriteBufferMiB int
}

// DatabaseWriteBufferMiB returns the configured chain database write buffer in MiB, 0 for the default.
func (c *Config) DatabaseWriteBufferMiB() int {
	if c == nil || c.Database == nil {
		return 0
	}
	return c.Database.WriteBufferMiB
}

func validateDatabaseConfig(cfg *DatabaseConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.WriteBufferMiB < 0 || cfg.WriteBufferMiB > MaxDatabaseWriteBufferMiB {
		return errors.Errorf("invalid Database.WriteBufferMiB %d; allowed: 0 (default, 4 MiB) to %d",
			cfg.WriteBufferMiB, MaxDatabaseWriteBufferMiB)
	}
	return nil
}

func validateIpfsDatastoreWriteBuffer(mib int) error {
	if mib < 0 || mib > MaxDatabaseWriteBufferMiB {
		return errors.Errorf("invalid IpfsConf.DatastoreWriteBufferMiB %d; allowed: 0 (default, 4 MiB) to %d",
			mib, MaxDatabaseWriteBufferMiB)
	}
	return nil
}
