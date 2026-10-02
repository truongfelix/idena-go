package pushpull

import (
	"github.com/idena-network/idena-go/common"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSortedPendingRequests_Add(t *testing.T) {
	sorted := newSortedPendingPushes()
	sorted.Add(pendingRequestTime{time: time.Unix(1, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(3, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(2, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(5, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(0, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(1, 0)})

	require.Equal(t, []int64{0, 1, 1, 2, 3, 5}, toInt64Array(sorted))
}

func TestSortedPendingRequests_Remove(t *testing.T) {
	sorted := newSortedPendingPushes()
	sorted.Add(pendingRequestTime{time: time.Unix(1, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(3, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(2, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(5, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(0, 0)})
	sorted.Remove(0)

	require.Equal(t, []int64{1, 2, 3, 5}, toInt64Array(sorted))

	sorted.Remove(3)

	require.Equal(t, []int64{1, 2, 3}, toInt64Array(sorted))
}

func TestSortedPendingRequests_MoveWithNewTime(t *testing.T) {
	sorted := newSortedPendingPushes()
	sorted.Add(pendingRequestTime{time: time.Unix(0, 0), req: PendingPulls{
		Hash: common.Hash128{0x1},
	}})
	sorted.Add(pendingRequestTime{time: time.Unix(3, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(2, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(5, 0)})
	sorted.Add(pendingRequestTime{time: time.Unix(1, 0)})
	sorted.MoveWithNewTime(0, time.Unix(6, 0))

	require.Equal(t, []int64{1, 2, 3, 5, 6}, toInt64Array(sorted))
	require.Equal(t, common.Hash128{0x1}, sorted.list[4].req.Hash)
}

func toInt64Array(list *sortedPendingPushes) []int64 {
	var times []int64
	for _, r := range list.list {
		times = append(times, r.time.Unix())
	}
	return times
}

func TestDefaultPushTracker_AddPendingRequest(t *testing.T) {
	tracker := NewDefaultPushTracker(time.Millisecond * 500)
	holder := NewDefaultHolder(2, tracker)

	hash1 := common.Hash128{0x1}
	tracker.RegisterPull(hash1)

	tracker.AddPendingPush("1", hash1)
	tracker.AddPendingPush("2", hash1)

	pull := requirePendingPull(t, tracker.Requests(), time.Second)
	require.Equal(t, peer.ID("1"), pull.Id)
	requireNoPendingPull(t, tracker.Requests(), time.Millisecond*200)

	pull = requirePendingPull(t, tracker.Requests(), time.Second)
	require.Equal(t, peer.ID("2"), pull.Id)

	pulls := make([]PendingPulls, 0, 2)

	tracker.AddPendingPush("3", hash1)
	tracker.AddPendingPush("4", hash1)
	tracker.AddPendingPush("5", hash1)
	tracker.AddPendingPush("6", hash1)

	pulls = append(pulls, requirePendingPull(t, tracker.Requests(), time.Second))
	pulls = append(pulls, requirePendingPull(t, tracker.Requests(), time.Second))

	holder.Add(hash1, 1, common.MultiShard, false)
	requireNoPendingPull(t, tracker.Requests(), time.Millisecond*600)

	require.Equal(t, peer.ID("3"), pulls[0].Id)
	require.Equal(t, peer.ID("4"), pulls[1].Id)
	require.Len(t, tracker.Requests(), 0)

	len := 0
	tracker.activePulls.Range(func(key, value interface{}) bool {
		len++
		return true
	})

	require.Equal(t, 0, len)

	require.Equal(t, 0, tracker.pendingPushes.Len())

	tracker.AddPendingPush("1", common.Hash128{})
	require.Equal(t, 0, tracker.pendingPushes.Len())
}

func TestDefaultPushTracker_IdleLoopWaitsForPendingPush(t *testing.T) {
	tracker := NewDefaultPushTracker(time.Millisecond * 50)
	NewDefaultHolder(1, tracker)

	// With no pending push, the loops block until one is added instead of waking up periodically.
	require.Eventually(t, func() bool {
		states := trackerLoopStates()
		if len(states) == 0 {
			return false
		}
		for _, state := range states {
			if !strings.HasPrefix(state, "chan receive") {
				return false
			}
		}
		return true
	}, time.Second, time.Millisecond*10)

	hash := common.Hash128{0x1}
	tracker.RegisterPull(hash)
	tracker.AddPendingPush("1", hash)

	pull := requirePendingPull(t, tracker.Requests(), time.Second)
	require.Equal(t, peer.ID("1"), pull.Id)
	require.Equal(t, hash, pull.Hash)
}

// trackerLoopStates returns the scheduler state of every goroutine running DefaultPushTracker.loop.
func trackerLoopStates() []string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var states []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if !strings.Contains(g, "pushpull.(*DefaultPushTracker).loop(") {
			continue
		}
		header := g[:strings.Index(g, "\n")]
		states = append(states, header[strings.Index(header, "[")+1:strings.LastIndex(header, "]")])
	}
	return states
}

func requirePendingPull(t *testing.T, requests <-chan PendingPulls, timeout time.Duration) PendingPulls {
	t.Helper()

	select {
	case pull := <-requests:
		return pull
	case <-time.After(timeout):
		require.FailNow(t, "timed out waiting for pending pull")
		return PendingPulls{}
	}
}

func requireNoPendingPull(t *testing.T, requests <-chan PendingPulls, timeout time.Duration) {
	t.Helper()

	select {
	case pull := <-requests:
		require.FailNowf(t, "unexpected pending pull", "received pull from peer %q", pull.Id)
	case <-time.After(timeout):
	}
}
