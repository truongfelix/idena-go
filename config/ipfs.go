package config

import "time"

type IpfsConfig struct {
	DataDir            string
	BootNodes          []string
	IpfsPort           int
	StaticPort         bool
	SwarmKey           string
	Routing            string
	LowWater           int
	HighWater          int
	GracePeriod        string
	ReproviderInterval string
	Profile            string
	BlockPinThreshold  float32
	FlipPinThreshold   float32
	PublishPeers       bool
	Gc                 IpfsGcConfig
	// DatastoreWriteBufferMiB is the write buffer (memtable) of the IPFS repo's LevelDB datastore in MiB; 0 keeps
	// the default (4 MiB). A node reachable from the internet is a DHT server and stores the provider records
	// of other nodes there, under random keys: every flushed level-0 table rewrites the whole of level 1, so a
	// bigger buffer cuts these writes. It costs about 2 times the added buffer in memory (the memtable and
	// the one being flushed) and applies when the datastore is opened: a change takes a restart.
	DatastoreWriteBufferMiB int
}

type IpfsGcConfig struct {
	Enabled                  bool
	Interval                 time.Duration
	Timeout                  time.Duration
	IntervalBeforeValidation time.Duration
	NotificationDelay        time.Duration
}

func GetDefaultIpfsConfig() *IpfsConfig {
	return &IpfsConfig{
		BlockPinThreshold: 0.3,
		FlipPinThreshold:  0.5,
		Profile:           "server",
		Gc: IpfsGcConfig{
			Enabled:                  true,
			Interval:                 time.Hour * 24,
			Timeout:                  time.Minute * 10,
			IntervalBeforeValidation: time.Hour * 12,
			NotificationDelay:        time.Minute,
		},
	}
}
