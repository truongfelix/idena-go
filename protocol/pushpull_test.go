package protocol

import (
	"fmt"
	"testing"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/pushpull"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestPushPullManager_PullsFromFirstMaxParallelPushers(t *testing.T) {
	tests := []struct {
		maxPulls      int
		expectedPulls int
	}{
		{maxPulls: -1, expectedPulls: 1},
		{maxPulls: 0, expectedPulls: 1},
		{maxPulls: 1, expectedPulls: 1},
		{maxPulls: 2, expectedPulls: 2},
		{maxPulls: 3, expectedPulls: 3},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("maxPulls=%d", tc.maxPulls), func(t *testing.T) {
			m := NewPushPullManager()
			m.AddEntryHolder(pushVote, pushpull.NewDefaultHolder(tc.maxPulls, nil))
			hash := pushPullHash{Type: pushVote, Hash: common.Hash128{0x1}}

			for i := 0; i < 5; i++ {
				m.addPush(peer.ID(fmt.Sprint(i)), hash)
			}

			require.Len(t, m.Requests(), tc.expectedPulls)
			for i := 0; i < tc.expectedPulls; i++ {
				req := <-m.Requests()
				require.Equal(t, peer.ID(fmt.Sprint(i)), req.peer)
				require.Equal(t, hash, req.hash)
			}
		})
	}
}

func TestPushPullManager_PullsFromNextPusherAfterDelay(t *testing.T) {
	const pullDelay = time.Millisecond * 100
	m := NewPushPullManager()
	holder := pushpull.NewDefaultHolder(1, pushpull.NewDefaultPushTracker(pullDelay))
	m.AddEntryHolder(pushVote, holder)
	m.Run()

	missing := pushPullHash{Type: pushVote, Hash: common.Hash128{0x1}}
	arrived := pushPullHash{Type: pushVote, Hash: common.Hash128{0x2}}
	start := time.Now()
	for _, hash := range []pushPullHash{missing, arrived} {
		m.addPush("1", hash)
		m.addPush("2", hash)
	}

	require.Equal(t, pullRequest{peer: "1", hash: missing}, requirePullRequest(t, m.Requests()))
	require.Equal(t, pullRequest{peer: "1", hash: arrived}, requirePullRequest(t, m.Requests()))
	holder.Add(arrived.Hash, 1, common.MultiShard, false)

	// Only the entry that did not arrive is pulled again, from the next pusher, once the delay has passed.
	require.Equal(t, pullRequest{peer: "2", hash: missing}, requirePullRequest(t, m.Requests()))
	require.GreaterOrEqual(t, time.Since(start), pullDelay)
	select {
	case req := <-m.Requests():
		require.FailNowf(t, "unexpected pull", "%v", req)
	case <-time.After(pullDelay * 2):
	}
}

func requirePullRequest(t *testing.T, requests <-chan pullRequest) pullRequest {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(time.Second):
		require.FailNow(t, "timed out waiting for pull request")
		return pullRequest{}
	}
}
