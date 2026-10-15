package protocol

import (
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/stretchr/testify/require"
)

// Mainnet, 2026-10-12: a phone in fast sync had its headers at 11,429,095; its only peer, another phone in fast
// sync, announced its chain at 4,875,200. bcn_syncing answered a highest block below the current one.
func TestSyncProgressHighestNotBelowHeaders(t *testing.T) {
	chain, _, _, _ := blockchain.NewTestBlockchain(false, nil)
	head := chain.Head.Height()
	d := &Downloader{chain: chain.Blockchain}

	for _, c := range []struct {
		name                  string
		headers, top          uint64
		wantHead, wantHighest uint64
	}{
		{"no headers, peers above", 0, head + 500, head, head + 500},
		{"headers, peers above them", head + 1000, head + 1200, head + 1000, head + 1200},
		{"headers above the peers' top", head + 1000, head + 300, head + 1000, head + 1000},
		{"no peer top yet", head + 1000, 0, head + 1000, head + 1000},
		{"no headers, no peer top", 0, 0, head, head},
	} {
		t.Run(c.name, func(t *testing.T) {
			chain.PreliminaryHead = nil
			if c.headers > 0 {
				chain.PreliminaryHead = headersAt(c.headers)
			}
			d.top = c.top
			current, highest := d.SyncProgress()
			require.Equal(t, c.wantHead, current)
			require.Equal(t, c.wantHighest, highest)
		})
	}
}
