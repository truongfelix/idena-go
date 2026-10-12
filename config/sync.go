package config

import "time"

const (
	// DefaultStaleHeadAge is how old the head block must be for the head to look stale: far more than any pause of
	// a running network (a block every round, an empty one when nobody proposes).
	DefaultStaleHeadAge = 24 * time.Hour
	// DefaultStaleHeadWait is how long a node with a stale head waits for a peer above it before it takes its head
	// as the top, so that the nodes of a network that did stop can start it again.
	DefaultStaleHeadWait = 30 * time.Minute
)

type SyncConfig struct {
	FastSync            bool
	ForceFullSync       uint64
	LoadAllFlips        bool
	AllFlipsLoadingTime time.Duration
	// A node whose head looks stale (its block older than StaleHeadAge, or below the headers of an unfinished fast
	// sync) and whose peers are none above it waits up to StaleHeadWait for a peer above it instead of taking part
	// in consensus at that height: its peers may be nodes that are syncing too. 0 turns the wait off.
	StaleHeadAge  time.Duration
	StaleHeadWait time.Duration
}
