package protocol

import (
	"errors"
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/config"
)

// ErrNoPeerAhead is SyncBlockchain's answer while the head looks stale and no connected peer is above it: the node
// waits for a peer above it instead of taking part in consensus at that height. A node in fast sync announces its
// chain head, not its headers, so the peers of a new node may all be at its height while the network is far above.
var ErrNoPeerAhead = errors.New("the head looks stale and no peer is above it")

// staleHeadWaitLogInterval spaces the log lines of a wait.
const staleHeadWaitLogInterval = time.Minute

// staleHeadWait follows one wait of a stale head for a peer above it.
type staleHeadWait struct {
	since    time.Time
	loggedAt time.Time
	gaveUp   bool
}

// headLooksStale tells whether the head cannot be the top of the network: its block is older than StaleHeadAge, or
// the headers of an unfinished fast sync lie above it. Never with Automine, nor with StaleHeadWait 0.
func headLooksStale(cfg *config.Config, head, preliminaryHead *types.Header, now time.Time) bool {
	if cfg == nil || cfg.Sync == nil || cfg.Sync.StaleHeadWait <= 0 {
		return false
	}
	if cfg.Consensus != nil && cfg.Consensus.Automine {
		return false
	}
	if preliminaryHead != nil && preliminaryHead.Height() > head.Height() {
		return true
	}
	return cfg.Sync.StaleHeadAge > 0 && now.Sub(time.Unix(head.Time(), 0)) > cfg.Sync.StaleHeadAge
}

// waitForPeerAhead tells whether the node, whose peers are none above its head, must wait for a peer above it: the
// head looks stale and the wait has lasted less than StaleHeadWait. After that wait the node takes its head as the
// top, as before, until its head no longer looks stale: the nodes of a network that stopped can start it again.
func (d *Downloader) waitForPeerAhead(head, preliminaryHead *types.Header, now time.Time) bool {
	if !headLooksStale(d.cfg, head, preliminaryHead, now) {
		d.staleWait = staleHeadWait{}
		return false
	}
	w := &d.staleWait
	if w.gaveUp {
		return false
	}
	if w.since.IsZero() {
		w.since = now
	}
	waited := now.Sub(w.since)
	if waited >= d.cfg.Sync.StaleHeadWait {
		w.gaveUp = true
		d.log.Warn("No peer above the stale head: taking the head as the top", "height", head.Height(),
			"headTime", time.Unix(head.Time(), 0).UTC(), "waited", waited.Round(time.Second))
		return false
	}
	if w.loggedAt.IsZero() || now.Sub(w.loggedAt) >= staleHeadWaitLogInterval {
		w.loggedAt = now
		d.log.Info("Waiting for a peer above the head", "height", head.Height(),
			"headTime", time.Unix(head.Time(), 0).UTC(), "waited", waited.Round(time.Second))
	}
	return true
}
