package state

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendFlipQualityKeepsTheLastTenCeremoniesWithFlips(t *testing.T) {
	var w []byte
	w = appendFlipQuality(w, 0, 0)
	require.Empty(t, w, "a ceremony without flips counts")
	for i := 1; i <= 12; i++ {
		w = appendFlipQuality(w, uint32(i), uint32(i))
	}
	require.Len(t, w, 2*FlipQualityCeremonies)
	require.Equal(t, byte(3), w[0], "the oldest kept is the third ceremony")
	flips, points2 := sumFlipQuality(w)
	require.Equal(t, uint32(3+4+5+6+7+8+9+10+11+12), flips)
	require.Equal(t, flips, points2)

	w = appendFlipQuality(nil, 3, 99)
	require.Equal(t, []byte{3, 6}, w, "points above twice the flips are capped")
	w = appendFlipQuality(nil, 1000, 2000)
	require.Equal(t, []byte{maxFlipQualityFlips, 2 * maxFlipQualityFlips}, w)
}

func TestAppendFlipQualityDoesNotShareTheOldWindow(t *testing.T) {
	backing := []byte{2, 2, 0, 0}
	_ = appendFlipQuality(backing[:2], 3, 3)
	require.Equal(t, []byte{2, 2, 0, 0}, backing, "the new pair was written into the old window's array")
}

func TestFlipQualityRoundTripsAndStaysOutOfAnIdentityWithout(t *testing.T) {
	empty := Identity{State: Human, Scores: []byte{1}}
	before, err := empty.ToBytes()
	require.NoError(t, err)
	var back Identity
	require.NoError(t, back.FromBytes(before))
	again, err := back.ToBytes()
	require.NoError(t, err)
	require.Equal(t, before, again, "an identity without a window must keep its v12 bytes")
	flips, points2 := back.FlipQuality()
	require.Zero(t, flips)
	require.Zero(t, points2)

	with := Identity{State: Human, Scores: []byte{1}, flipQuality: []byte{4, 7, 2, 0}}
	b, err := with.ToBytes()
	require.NoError(t, err)
	require.NotEqual(t, before, b)
	var got Identity
	require.NoError(t, got.FromBytes(b))
	require.Equal(t, []byte{4, 7, 2, 0}, got.FlipQualityWindow())
	flips, points2 = got.FlipQuality()
	require.Equal(t, uint32(6), flips)
	require.Equal(t, uint32(7), points2)
}
