package protocol

// HARNESS (benchmark only, never for a PR): gossip counters for the single-pull A/B.
// Every minute: "HARNESS: gossip <type>=pullNow/pullFallback/arrived/dup/rejected ... lat<type>=b0/.../b7"
// (cumulative). Latency = time from the first push of an entry to its arrival, buckets
// <50ms <100 <200 <300 <500 <1s <2s >=2s.

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/pushpull"
	"github.com/idena-network/idena-go/log"
)

var harnessLatencyBounds = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
	300 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}

var harnessTypeNames = [pushTx + 1]string{"", "vote", "block", "proof", "flip", "key", "tx"}

type harnessGossipStats struct {
	pullNow  [pushTx + 1]uint64
	pullFb   [pushTx + 1]uint64
	arrived  [pushTx + 1]uint64
	dup      [pushTx + 1]uint64
	rejected [pushTx + 1]uint64
	latency  [pushTx + 1][8]uint64
}

var harnessGossip harnessGossipStats

func harnessInc(c *uint64) {
	atomic.AddUint64(c, 1)
}

func (m *PushPullManager) harnessArrived(t pushType, hash common.Hash128) {
	harnessInc(&harnessGossip.arrived[t])
	key := pushPullHash{Type: t, Hash: hash}
	v, ok := m.pendingPushes.Get(key.String())
	if !ok {
		return
	}
	d := time.Since(v.(*pendingPush).created)
	i := 0
	for i < len(harnessLatencyBounds) && d >= harnessLatencyBounds[i] {
		i++
	}
	harnessInc(&harnessGossip.latency[t][i])
}

func harnessLogGossip() {
	logger := log.New("component", "harness")
	logger.Info(fmt.Sprintf("HARNESS: gossip mode oldPulls=%v oldTracker=%v", pushpull.HarnessOldPulls, pushpull.HarnessOldTracker))
	for range time.Tick(time.Minute) {
		var b strings.Builder
		b.WriteString("HARNESS: gossip")
		for t := pushVote; t <= pushTx; t++ {
			fmt.Fprintf(&b, " %s=%d/%d/%d/%d/%d", harnessTypeNames[t],
				atomic.LoadUint64(&harnessGossip.pullNow[t]), atomic.LoadUint64(&harnessGossip.pullFb[t]),
				atomic.LoadUint64(&harnessGossip.arrived[t]), atomic.LoadUint64(&harnessGossip.dup[t]),
				atomic.LoadUint64(&harnessGossip.rejected[t]))
		}
		for _, t := range []pushType{pushVote, pushBlock, pushProof} {
			fmt.Fprintf(&b, " lat%s=", harnessTypeNames[t])
			for i := range harnessGossip.latency[t] {
				if i > 0 {
					b.WriteString("/")
				}
				fmt.Fprintf(&b, "%d", atomic.LoadUint64(&harnessGossip.latency[t][i]))
			}
		}
		logger.Info(b.String())
	}
}
