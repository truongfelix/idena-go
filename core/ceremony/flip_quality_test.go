package ceremony

import (
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/stats/collector"
	"github.com/stretchr/testify/require"
	dbm "github.com/tendermint/tm-db"
)

func TestFlipQualityPoints2(t *testing.T) {
	cases := []struct {
		status FlipStatus
		grade  types.Grade
		want   uint32
	}{
		{Qualified, types.GradeNone, 2},
		{WeaklyQualified, types.GradeA, 2},
		{NotQualified, types.GradeNone, 1},
		{QualifiedByNone, types.GradeNone, 0},
		{Qualified, types.GradeReported, 0},
		{WeaklyQualified, types.GradeReported, 0},
		{NotQualified, types.GradeReported, 0},
	}
	for _, c := range cases {
		require.Equal(t, c.want, flipQualityPoints2(FlipQualification{status: c.status, grade: c.grade}), "status %v grade %v", c.status, c.grade)
	}
}

func TestFlipQualityByAuthor(t *testing.T) {
	a, b, c, d := common.Address{1}, common.Address{2}, common.Address{3}, common.Address{4}
	q := []FlipQualification{
		{status: Qualified, answersCount: 4},
		{status: NotQualified, answersCount: 1},
		{status: Qualified, grade: types.GradeReported, answersCount: 5},
		{status: QualifiedByNone, answersCount: 3},
		{status: WeaklyQualified, answersCount: 3},
		// no answer at all: the status the ceremony gives it (NotQualified) must not count
		{status: NotQualified, answersCount: 0},
		{status: NotQualified, answersCount: 0},
		// left blank by its committee: counts, 0 points
		{status: QualifiedByNone, answersCount: 4},
	}
	got := flipQualityByAuthor(q, map[common.Address][]int{a: {0, 1, 2}, b: {3, 4, 5}, c: {6}, d: {7}})
	require.Equal(t, authorFlipQuality{flips: 3, points2: 3}, got[a])
	require.Equal(t, authorFlipQuality{flips: 2, points2: 2}, got[b], "a flip nobody answered was counted")
	_, ok := got[c]
	require.False(t, ok, "an author whose only flip got no answer has a result: %+v", got[c])
	require.Equal(t, authorFlipQuality{flips: 1, points2: 0}, got[d], "a flip its committee left blank must count, 0 points")
	require.Len(t, got, 3)
}

// Consensus v13 records the result of an identity's own flips in its window, whatever the ceremony made of the
// identity; v12 records nothing, and a ceremony without flips leaves the window as it is.
func Test_applyOnStateRecordsFlipQualityUnderV13(t *testing.T) {
	appState, _ := appstate.NewAppState(dbm.NewMemDB(), eventbus.New())
	addr := common.Address{0x1}
	appState.State.SetState(addr, state.Newbie)

	v12 := *blockchain.GetDefaultConsensusConfig()
	v12.EnableUpgrade13 = false
	value := cacheValue{prevState: state.Newbie, state: state.Verified, birthday: 3, flipQuality: authorFlipQuality{flips: 3, points2: 4}}
	applyOnState(&v12, appState, 0, collector.NewStatsCollector(), addr, value)
	flips, points2 := appState.State.GetFlipQuality(addr)
	require.Zero(t, flips, "v12 recorded a window")
	require.Zero(t, points2)

	v13 := v12
	v13.EnableUpgrade13 = true
	applyOnState(&v13, appState, 0, collector.NewStatsCollector(), addr, value)
	require.Equal(t, []byte{3, 4}, appState.State.GetIdentity(addr).FlipQualityWindow())

	value = cacheValue{prevState: state.Verified, state: state.Suspended, birthday: 3, missed: true, flipQuality: authorFlipQuality{flips: 2, points2: 0}}
	applyOnState(&v13, appState, 0, collector.NewStatsCollector(), addr, value)
	require.Equal(t, []byte{3, 4, 2, 0}, appState.State.GetIdentity(addr).FlipQualityWindow(), "a failed author's flips count too")

	applyOnState(&v13, appState, 0, collector.NewStatsCollector(), addr, cacheValue{prevState: state.Suspended, state: state.Suspended, birthday: 3, missed: true})
	require.Equal(t, []byte{3, 4, 2, 0}, appState.State.GetIdentity(addr).FlipQualityWindow(), "a ceremony without flips changed the window")
}
