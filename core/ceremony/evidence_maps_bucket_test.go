package ceremony

import (
	"bytes"
	"testing"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/database"
	"github.com/stretchr/testify/require"
	db "github.com/tendermint/tm-db"
)

// perShardFilter reproduces the original readEvidenceMaps behavior: for a given
// shard, scan all maps and keep those whose sender is a candidate of that shard,
// in the order the maps are given. bucketEvidenceMapsByShard must produce the
// same result for every shard so the change stays consensus-neutral.
func perShardFilter(maps []*database.DbEvidenceMap, shardCandidates map[common.ShardId][]common.Address, shardId common.ShardId) [][]byte {
	candidates := map[common.Address]struct{}{}
	for _, a := range shardCandidates[shardId] {
		candidates[a] = struct{}{}
	}
	var result [][]byte
	for _, m := range maps {
		if _, ok := candidates[m.Sender]; ok {
			result = append(result, m.Map)
		}
	}
	return result
}

func addr(b byte) common.Address {
	var a common.Address
	a[len(a)-1] = b
	return a
}

func TestBucketEvidenceMapsByShard_MatchesPerShardFilter(t *testing.T) {
	// Two shards, senders partitioned across them, plus one sender that is not a
	// candidate in any shard (its map must be dropped) and interleaved DB order.
	shardCandidates := map[common.ShardId][]common.Address{
		1: {addr(0x10), addr(0x11), addr(0x12)},
		2: {addr(0x20), addr(0x21)},
	}
	senderShards := map[common.Address][]common.ShardId{}
	for shardId, addrs := range shardCandidates {
		for _, a := range addrs {
			senderShards[a] = append(senderShards[a], shardId)
		}
	}

	// DB iterator order is ascending by sender address; mimic that here.
	maps := []*database.DbEvidenceMap{
		{Sender: addr(0x10), Map: []byte{1}},
		{Sender: addr(0x11), Map: []byte{2}},
		{Sender: addr(0x12), Map: []byte{3}},
		{Sender: addr(0x20), Map: []byte{4}},
		{Sender: addr(0x21), Map: []byte{5}},
		{Sender: addr(0x99), Map: []byte{6}}, // not a candidate in any shard
	}

	got := bucketEvidenceMapsByShard(maps, senderShards)

	for shardId := range shardCandidates {
		want := perShardFilter(maps, shardCandidates, shardId)
		require.Equalf(t, want, got[shardId], "shard %d bucket differs from per-shard filter", shardId)
	}

	// The non-candidate sender's map must not appear anywhere.
	for shardId, list := range got {
		for _, m := range list {
			require.Falsef(t, bytes.Equal(m, []byte{6}), "dropped map leaked into shard %d", shardId)
		}
	}
}

func TestBucketEvidenceMapsByShard_Empty(t *testing.T) {
	require.Empty(t, bucketEvidenceMapsByShard(nil, map[common.Address][]common.ShardId{}))
}

// oldReadEvidenceMaps reproduces the original per-shard readEvidenceMaps against
// a real ValidationCeremony's epoch DB and shard candidates.
func oldReadEvidenceMaps(vc *ValidationCeremony, shardId common.ShardId) [][]byte {
	maps := vc.epochDb.ReadEvidenceMaps()
	candidates := map[common.Address]struct{}{}
	for _, c := range vc.shardCandidates[shardId].candidates {
		candidates[c.Address] = struct{}{}
	}
	var result [][]byte
	for _, m := range maps {
		if _, ok := candidates[m.Sender]; ok {
			result = append(result, m.Map)
		}
	}
	return result
}

// TestReadEvidenceMapsByShard_MatchesOldPerShard drives the real
// readEvidenceMapsByShard on a real ValidationCeremony and asserts it returns,
// for every shard, exactly what the original per-shard readEvidenceMaps did.
func TestReadEvidenceMapsByShard_MatchesOldPerShard(t *testing.T) {
	edb := database.NewEpochDb(db.NewMemDB(), 1)
	shardCandidates := map[common.ShardId]*candidatesOfShard{
		1: {candidates: []*candidate{{Address: addr(0x10)}, {Address: addr(0x11)}, {Address: addr(0x12)}}},
		2: {candidates: []*candidate{{Address: addr(0x20)}, {Address: addr(0x21)}}},
	}
	// Write maps in ascending sender-address order (DB iterator order), including
	// a sender that is not a candidate in any shard (must be dropped).
	senders := []common.Address{addr(0x10), addr(0x11), addr(0x12), addr(0x20), addr(0x21), addr(0x99)}
	for i, s := range senders {
		edb.WriteEvidenceMap(s, []byte{byte(i)})
	}

	vc := &ValidationCeremony{epochDb: edb, shardCandidates: shardCandidates}
	got := vc.readEvidenceMapsByShard()

	want := map[common.ShardId][][]byte{}
	for shardId := range shardCandidates {
		want[shardId] = oldReadEvidenceMaps(vc, shardId)
	}
	require.Equal(t, want, got)
}

// candidatesFromAddrs builds a candidatesOfShard from a list of addresses.
func candidatesFromAddrs(addrs ...common.Address) *candidatesOfShard {
	c := &candidatesOfShard{}
	for _, a := range addrs {
		c.candidates = append(c.candidates, &candidate{Address: a})
	}
	return c
}

// writeEvidenceBitmap writes a well-formed evidence bitmap (sized to the shard's
// candidate count) approving the given candidate indices.
func writeEvidenceBitmap(edb *database.EpochDb, sender common.Address, size int, approved ...uint32) {
	bm := common.NewBitmap(uint32(size))
	for _, v := range approved {
		bm.Add(v)
	}
	var buf bytes.Buffer
	bm.WriteTo(&buf)
	edb.WriteEvidenceMap(sender, buf.Bytes())
}

// TestCalculateApprovedCandidates_SameForOldAndNewGrouping is the consensus-
// output guard: the approved-candidate set computed from the new per-shard
// grouping must equal the set computed from the original per-shard reads, for
// every shard. If a future change to the grouping altered results, this fails.
func TestCalculateApprovedCandidates_SameForOldAndNewGrouping(t *testing.T) {
	cand1 := []common.Address{addr(0x10), addr(0x11), addr(0x12), addr(0x13), addr(0x14)}
	cand2 := []common.Address{addr(0x20), addr(0x21), addr(0x22)}
	shardCandidates := map[common.ShardId]*candidatesOfShard{
		1: candidatesFromAddrs(cand1...),
		2: candidatesFromAddrs(cand2...),
	}

	edb := database.NewEpochDb(db.NewMemDB(), 1)
	// shard 1: three maps -> minScore 2; indices 0 and 1 clear the threshold.
	writeEvidenceBitmap(edb, addr(0x10), len(cand1), 0, 1, 2)
	writeEvidenceBitmap(edb, addr(0x11), len(cand1), 0, 1)
	writeEvidenceBitmap(edb, addr(0x12), len(cand1), 0, 3)
	// shard 2: two maps -> minScore 2; only index 1 clears it.
	writeEvidenceBitmap(edb, addr(0x20), len(cand2), 0, 1)
	writeEvidenceBitmap(edb, addr(0x21), len(cand2), 1, 2)
	// a non-candidate sender's map (must be ignored by both paths).
	writeEvidenceBitmap(edb, addr(0x99), len(cand1), 0, 1, 2)

	vc := &ValidationCeremony{epochDb: edb, shardCandidates: shardCandidates}
	em := &appstate.EvidenceMap{}
	newByShard := vc.readEvidenceMapsByShard()

	for shardId := common.ShardId(1); shardId <= common.ShardId(len(shardCandidates)); shardId++ {
		cands := vc.getCandidatesAddresses(shardId)
		oldApproved := em.CalculateApprovedCandidates(cands, oldReadEvidenceMaps(vc, shardId))
		newApproved := em.CalculateApprovedCandidates(cands, newByShard[shardId])
		require.ElementsMatchf(t, oldApproved, newApproved, "approved set differs for shard %d", shardId)
		require.NotEmptyf(t, newApproved, "expected some approved candidates for shard %d", shardId)
	}
}

// TestReadEvidenceMapsByShard_SenderInTwoShardsMatchesOld guards the
// consensus-neutrality of the grouping in the (in-practice-impossible) case of a
// sender listed as a candidate in more than one shard. The original per-shard
// reads kept such a sender's map in EVERY shard whose candidate set contained
// it, and the grouping must reproduce that exactly rather than collapsing the
// sender to a single shard. The result must also be identical across repeated
// calls (deterministic).
func TestReadEvidenceMapsByShard_SenderInTwoShardsMatchesOld(t *testing.T) {
	edb := database.NewEpochDb(db.NewMemDB(), 1)
	dup := addr(0x30)
	shardCandidates := map[common.ShardId]*candidatesOfShard{
		1: candidatesFromAddrs(dup, addr(0x11)),
		2: candidatesFromAddrs(dup, addr(0x21)),
	}
	edb.WriteEvidenceMap(dup, []byte{1})
	edb.WriteEvidenceMap(addr(0x11), []byte{2})
	edb.WriteEvidenceMap(addr(0x21), []byte{3})

	vc := &ValidationCeremony{epochDb: edb, shardCandidates: shardCandidates}
	first := vc.readEvidenceMapsByShard()

	// Must equal the original per-shard reads for every shard, including the
	// duplicated sender's map appearing in both shard 1 and shard 2.
	for shardId := range shardCandidates {
		require.Equalf(t, oldReadEvidenceMaps(vc, shardId), first[shardId],
			"shard %d differs from original per-shard read", shardId)
	}

	// The duplicated sender's map appears once per shard, i.e. twice in total.
	count := 0
	for _, list := range first {
		for _, m := range list {
			if bytes.Equal(m, []byte{1}) {
				count++
			}
		}
	}
	require.Equal(t, 2, count)

	// Deterministic across repeated calls.
	for i := 0; i < 100; i++ {
		require.Equal(t, first, vc.readEvidenceMapsByShard())
	}
}

// TestReadEvidenceMapsByShard_ShardWithoutMaps covers the nil-slice contract the
// ApplyNewEpoch call site relies on: a shard that has candidates but whose
// candidates submitted no evidence maps is absent from the grouping, so indexing
// it yields a nil slice. CalculateApprovedCandidates must treat that identically
// to the original per-shard read (empty approved set), with no panic.
func TestReadEvidenceMapsByShard_ShardWithoutMaps(t *testing.T) {
	cand1 := []common.Address{addr(0x10), addr(0x11), addr(0x12)}
	cand2 := []common.Address{addr(0x20), addr(0x21)}
	shardCandidates := map[common.ShardId]*candidatesOfShard{
		1: candidatesFromAddrs(cand1...),
		2: candidatesFromAddrs(cand2...), // no maps written for shard 2
	}

	edb := database.NewEpochDb(db.NewMemDB(), 1)
	writeEvidenceBitmap(edb, addr(0x10), len(cand1), 0, 1)
	writeEvidenceBitmap(edb, addr(0x11), len(cand1), 0, 1)

	vc := &ValidationCeremony{epochDb: edb, shardCandidates: shardCandidates}
	byShard := vc.readEvidenceMapsByShard()

	// Shard 2 received no maps: absent key -> nil slice, matching the old read.
	require.Nil(t, byShard[2])
	require.Equal(t, oldReadEvidenceMaps(vc, 2), byShard[2])

	em := &appstate.EvidenceMap{}
	oldApproved := em.CalculateApprovedCandidates(vc.getCandidatesAddresses(2), oldReadEvidenceMaps(vc, 2))
	newApproved := em.CalculateApprovedCandidates(vc.getCandidatesAddresses(2), byShard[2])
	require.Empty(t, newApproved)
	require.Equal(t, oldApproved, newApproved)
}

// TestReadEvidenceMapsByShard_SingleShard covers the common mainnet default: no
// sharding, a single shard holding every candidate. The grouping must return the
// full DB-ordered map slice for shard 1, matching the original per-shard read,
// and drive the same approved-candidate set.
func TestReadEvidenceMapsByShard_SingleShard(t *testing.T) {
	cand := []common.Address{addr(0x10), addr(0x11), addr(0x12), addr(0x13)}
	shardCandidates := map[common.ShardId]*candidatesOfShard{
		1: candidatesFromAddrs(cand...),
	}

	edb := database.NewEpochDb(db.NewMemDB(), 1)
	writeEvidenceBitmap(edb, addr(0x10), len(cand), 0, 1, 2)
	writeEvidenceBitmap(edb, addr(0x11), len(cand), 0, 1)
	writeEvidenceBitmap(edb, addr(0x12), len(cand), 0, 2)
	writeEvidenceBitmap(edb, addr(0x99), len(cand), 0, 1, 2) // non-candidate, dropped

	vc := &ValidationCeremony{epochDb: edb, shardCandidates: shardCandidates}
	byShard := vc.readEvidenceMapsByShard()

	require.Equal(t, oldReadEvidenceMaps(vc, 1), byShard[1])
	require.Len(t, byShard[1], 3) // the non-candidate map is dropped

	em := &appstate.EvidenceMap{}
	oldApproved := em.CalculateApprovedCandidates(vc.getCandidatesAddresses(1), oldReadEvidenceMaps(vc, 1))
	newApproved := em.CalculateApprovedCandidates(vc.getCandidatesAddresses(1), byShard[1])
	require.ElementsMatch(t, oldApproved, newApproved)
	require.NotEmpty(t, newApproved)
}

// TestApplyNewEpoch_GroupingMatchesPerShardReads is a call-site guard. Running
// the full ApplyNewEpoch requires machinery (qualification, appstate, config)
// far beyond this refactor, so instead this reproduces exactly the loop
// ApplyNewEpoch uses to consume the grouping -- iterating shard ids 1..N and
// calling CalculateApprovedCandidates(getCandidatesAddresses(shardId),
// evidenceMapsByShard[shardId]) -- and asserts it yields, for every shard, the
// same approved set the pre-refactor inline expression did
// (CalculateApprovedCandidates(getCandidatesAddresses(shardId),
// readEvidenceMaps(shardId))). It deliberately includes a shard with no maps so
// the nil-index path is exercised inside the loop.
func TestApplyNewEpoch_GroupingMatchesPerShardReads(t *testing.T) {
	cand1 := []common.Address{addr(0x10), addr(0x11), addr(0x12), addr(0x13)}
	cand2 := []common.Address{addr(0x20), addr(0x21), addr(0x22)}
	cand3 := []common.Address{addr(0x30), addr(0x31)} // submits no maps
	shardCandidates := map[common.ShardId]*candidatesOfShard{
		1: candidatesFromAddrs(cand1...),
		2: candidatesFromAddrs(cand2...),
		3: candidatesFromAddrs(cand3...),
	}

	edb := database.NewEpochDb(db.NewMemDB(), 1)
	writeEvidenceBitmap(edb, addr(0x10), len(cand1), 0, 1, 2)
	writeEvidenceBitmap(edb, addr(0x11), len(cand1), 0, 1)
	writeEvidenceBitmap(edb, addr(0x12), len(cand1), 0, 3)
	writeEvidenceBitmap(edb, addr(0x20), len(cand2), 0, 1)
	writeEvidenceBitmap(edb, addr(0x21), len(cand2), 1, 2)
	writeEvidenceBitmap(edb, addr(0x99), len(cand1), 0, 1, 2) // non-candidate

	vc := &ValidationCeremony{epochDb: edb, shardCandidates: shardCandidates}
	em := &appstate.EvidenceMap{}

	// Mirror the ApplyNewEpoch consumption loop.
	evidenceMapsByShard := vc.readEvidenceMapsByShard()
	for shardId := common.ShardId(1); shardId <= common.ShardId(len(vc.shardCandidates)); shardId++ {
		cands := vc.getCandidatesAddresses(shardId)
		newApproved := em.CalculateApprovedCandidates(cands, evidenceMapsByShard[shardId])
		oldApproved := em.CalculateApprovedCandidates(cands, oldReadEvidenceMaps(vc, shardId))
		require.ElementsMatchf(t, oldApproved, newApproved, "approved set differs for shard %d", shardId)
	}
	// Shard 3 had candidates but no maps: nil slice, empty approved set.
	require.Nil(t, evidenceMapsByShard[3])
	require.Empty(t, em.CalculateApprovedCandidates(vc.getCandidatesAddresses(3), evidenceMapsByShard[3]))
}

// TestReadEvidenceMapsByShard_PreservesDbOrder confirms the result follows the
// epoch DB iterator order (ascending sender address) regardless of the order in
// which the maps were written.
func TestReadEvidenceMapsByShard_PreservesDbOrder(t *testing.T) {
	edb := database.NewEpochDb(db.NewMemDB(), 1)
	shardCandidates := map[common.ShardId]*candidatesOfShard{
		1: candidatesFromAddrs(addr(0x10), addr(0x11), addr(0x12), addr(0x13)),
	}
	// Write out of address order on purpose.
	for _, e := range []struct {
		a common.Address
		m byte
	}{
		{addr(0x13), 0x13}, {addr(0x10), 0x10}, {addr(0x12), 0x12}, {addr(0x11), 0x11},
	} {
		edb.WriteEvidenceMap(e.a, []byte{e.m})
	}

	vc := &ValidationCeremony{epochDb: edb, shardCandidates: shardCandidates}
	got := vc.readEvidenceMapsByShard()[1]
	want := [][]byte{{0x10}, {0x11}, {0x12}, {0x13}}
	require.Equal(t, want, got)
}

// countingDB counts the range scans of the evidence table.
type countingDB struct {
	db.DB
	scans int
}

func (c *countingDB) Iterator(start, end []byte) (db.Iterator, error) {
	c.scans++
	return c.DB.Iterator(start, end)
}

func (c *countingDB) ReverseIterator(start, end []byte) (db.Iterator, error) {
	c.scans++
	return c.DB.ReverseIterator(start, end)
}

// TestReadEvidenceMapsByShard_NoShards: with no shards, ApplyNewEpoch's shard
// loop does not run, so the original per-shard reads never read the evidence
// table. The grouping must not read it either, nor touch the epoch DB at all.
func TestReadEvidenceMapsByShard_NoShards(t *testing.T) {
	for _, shardCandidates := range []map[common.ShardId]*candidatesOfShard{nil, {}} {
		evidence := &countingDB{DB: db.NewMemDB()}
		edb := database.NewEpochDb(evidence, 1)
		edb.WriteEvidenceMap(addr(0x10), []byte{1})
		scans := evidence.scans

		vc := &ValidationCeremony{epochDb: edb, shardCandidates: shardCandidates}
		require.Empty(t, vc.readEvidenceMapsByShard())
		require.Equal(t, scans, evidence.scans)
	}
	require.NotPanics(t, func() { (&ValidationCeremony{}).readEvidenceMapsByShard() })
}
