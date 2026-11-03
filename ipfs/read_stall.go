package ipfs

import (
	"context"
	"sync"
	"time"

	"github.com/idena-network/idena-go/log"
	"github.com/libp2p/go-libp2p/core/host"
)

const (
	// Reads that time out in a row, with peers connected and no bitswap message in between, before the node
	// redials its peers.
	readStallTimeouts = 2
	// The shortest time from the first of those timeouts to the last: reads started together time out together.
	readStallWindow = 30 * time.Second
	// The shortest time between two redials.
	readStallRedialInterval = 5 * time.Minute
	readStallRedialTimeout  = 30 * time.Second
)

// readStall tells when the node's IPFS reads are stuck while it is connected to peers: every read times out and
// bitswap receives nothing at all, not even an answer that a peer lacks the data. A node was seen in that state
// for 20 minutes with 5 connected peers holding the data, until a restart (B-196); closing and redialing the
// peers does what the restart did.
type readStall struct {
	mu         sync.Mutex
	streak     int
	firstAt    time.Time
	messages   uint64
	lastRedial time.Time
}

// success ends a streak of timeouts.
func (s *readStall) success() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streak = 0
}

// timeout records a read that timed out, with the number of connected peers and bitswap's count of received
// messages; it returns true when the peers should be redialed.
func (s *readStall) timeout(now time.Time, peers int, messages uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streak == 0 || messages != s.messages {
		// A new streak, or bitswap heard from a peer since the streak began: it is not stuck.
		s.streak = 0
		s.firstAt = now
		s.messages = messages
	}
	s.streak++
	if peers == 0 || s.streak < readStallTimeouts || now.Sub(s.firstAt) < readStallWindow {
		return false
	}
	if !s.lastRedial.IsZero() && now.Sub(s.lastRedial) < readStallRedialInterval {
		return false
	}
	s.lastRedial = now
	s.streak = 0
	return true
}

// redialPeers closes the connections to every connected peer and dials each again at its known addresses.
func redialPeers(h host.Host, logger log.Logger) {
	peers := h.Network().Peers()
	logger.Warn("ipfs reads stalled with peers connected, redialing them", "peers", len(peers))
	for _, id := range peers {
		info := h.Peerstore().PeerInfo(id)
		if err := h.Network().ClosePeer(id); err != nil {
			logger.Debug("ipfs redial: close failed", "peer", id, "err", err)
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), readStallRedialTimeout)
			defer cancel()
			if err := h.Connect(ctx, info); err != nil {
				logger.Debug("ipfs redial: connect failed", "peer", info.ID, "err", err)
			}
		}()
	}
}
