package validationsummary

import (
	"encoding/json"
	"testing"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/state"
	statsTypes "github.com/idena-network/idena-go/stats/types"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
)

// flipCid: a real CID, as the flips of a ceremony have.
func flipCid(t *testing.T, name string) []byte {
	c, err := cid.Prefix{Version: 1, Codec: cid.Raw, MhType: multihash.SHA2_256, MhLength: -1}.Sum([]byte(name))
	require.NoError(t, err)
	return c.Bytes()
}

func cidText(t *testing.T, raw []byte) string {
	c, err := cid.Cast(raw)
	require.NoError(t, err)
	return c.String()
}

// The ceremony of an own address: it solved flips 0-2 in the short session (2 an extra flip not used) and 0-1 in the
// long one, and made flip 3 (judged) and one the ceremony did not show. Another identity answered and reported flip 3.
func Test_Recorder_flipDetails(t *testing.T) {
	f := newFixture(t)
	own, other := common.Address{0x1}, common.Address{0x2}
	require.NoError(t, f.store.SetOwn(func() []common.Address { return []common.Address{own} }))
	cids := [][]byte{flipCid(t, "a"), flipCid(t, "b"), flipCid(t, "c"), flipCid(t, "mine")}
	unshown := flipCid(t, "unshown")

	f.appState.State.SetState(own, state.Verified)
	f.appState.State.AddFlip(own, cids[3], 0)
	f.appState.State.AddFlip(own, unshown, 1)
	f.appState.State.SetState(other, state.Verified)
	f.appState.State.AddFlip(other, flipCid(t, "theirs"), 0)

	r := f.recorder
	r.EnableCollecting()
	f.applyIdentity(own, state.Verified)
	f.applyIdentity(other, state.Verified)
	r.SetValidation(&statsTypes.ValidationStats{Shards: map[common.ShardId]*statsTypes.ValidationShardStats{1: {
		FlipCids: cids,
		IdentitiesPerAddr: map[common.Address]*statsTypes.IdentityStats{
			own:   {ShortFlipsToSolve: []int{0, 1, 2}, LongFlipsToSolve: []int{0, 1}, Approved: true},
			other: {LongFlipsToSolve: []int{3}, Approved: true},
		},
		FlipsPerIdx: map[int]*statsTypes.FlipStats{
			0: {Status: FlipQualified, Answer: types.Left,
				ShortAnswers: []statsTypes.FlipAnswerStats{{Respondent: own, Answer: types.Left, Point: 1, Considered: true}},
				LongAnswers:  []statsTypes.FlipAnswerStats{{Respondent: own, Answer: types.Left, Point: 1, Considered: true}}},
			1: {Status: FlipNotQualified, Answer: types.None, Grade: types.GradeReported,
				ShortAnswers: []statsTypes.FlipAnswerStats{{Respondent: own, Answer: types.Right, Considered: true}},
				LongAnswers:  []statsTypes.FlipAnswerStats{{Respondent: own, Answer: types.Right, Grade: types.GradeReported}}},
			2: {Status: FlipWeaklyQualified, Answer: types.Right,
				ShortAnswers: []statsTypes.FlipAnswerStats{{Respondent: own, Answer: types.Right, Considered: false}}},
			3: {Status: FlipQualified, Answer: types.Right, Grade: types.GradeReported,
				LongAnswers: []statsTypes.FlipAnswerStats{
					{Respondent: other, Answer: types.Right, Grade: types.GradeReported, Considered: true},
					{Respondent: common.Address{0x3}, Answer: types.Left, Considered: true},
				}},
		},
	}}})
	f.endBlock(types.ValidationFinished)

	s, err := f.store.Get(5, own)
	require.NoError(t, err)
	require.NotNil(t, s.FlipDetails)
	require.Equal(t, []SolvedFlip{
		{Cid: cidText(t, cids[0]), Session: "short", Answered: true, Answer: "Left", Considered: true, Point: 1,
			RightAnswer: "Left", Status: "Qualified"},
		{Cid: cidText(t, cids[1]), Session: "short", Answered: true, Answer: "Right", Considered: true,
			RightAnswer: "None", Status: "NotQualified", FlipReported: true},
		{Cid: cidText(t, cids[2]), Session: "short", Answered: true, Answer: "Right", RightAnswer: "Right",
			Status: "WeaklyQualified"},
		{Cid: cidText(t, cids[0]), Session: "long", Answered: true, Answer: "Left", Considered: true, Point: 1,
			RightAnswer: "Left", Status: "Qualified"},
		{Cid: cidText(t, cids[1]), Session: "long", Answered: true, Answer: "Right", Reported: true,
			RightAnswer: "None", Status: "NotQualified", FlipReported: true},
	}, s.FlipDetails.Solved)
	require.Equal(t, []MadeFlip{
		{Cid: cidText(t, cids[3]), Judged: true, RightAnswer: "Right", Status: "Qualified", Reported: true, Reports: 1,
			Answers: []FlipAnswer{
				{Respondent: other, Session: "long", Answer: "Right", Reported: true, Considered: true},
				{Respondent: common.Address{0x3}, Session: "long", Answer: "Left", Considered: true},
			}},
		{Cid: cidText(t, unshown), RightAnswer: "None", Status: "Unknown", Answers: []FlipAnswer{}},
	}, s.FlipDetails.Made)

	// Kept for good with the own summary, beyond the kept epochs.
	data, err := f.store.db.Get(ownKey(own, 5))
	require.NoError(t, err)
	require.Contains(t, string(data), `"flipDetails"`)

	// Another address: no details.
	s, err = f.store.Get(5, other)
	require.NoError(t, err)
	require.Nil(t, s.FlipDetails)
}

// Odd input never stops the node: indexes outside the flip list, a flip without stats, no answers.
func Test_flipDetails_oddInput(t *testing.T) {
	own := common.Address{0x1}
	d := &ceremonyData{validation: &statsTypes.ValidationStats{Shards: map[common.ShardId]*statsTypes.ValidationShardStats{
		1: {
			FlipCids: [][]byte{{0x1, 0x2}},
			IdentitiesPerAddr: map[common.Address]*statsTypes.IdentityStats{
				own: {ShortFlipsToSolve: []int{0, 7, -1}},
			},
			FlipsPerIdx: map[int]*statsTypes.FlipStats{7: {Status: FlipQualified}},
		},
		2: nil,
	}}}
	details, err := d.flipDetails(own, [][]byte{{0x9}})
	require.NoError(t, err)
	require.Equal(t, []SolvedFlip{
		// Not a CID: hex. No stats: unknown status, not answered.
		{Cid: "0x0102", Session: "short", Answer: "None", RightAnswer: "None", Status: "Unknown"},
		// Outside the flip list: no CID; its stats still read.
		{Session: "short", Answer: "None", RightAnswer: "None", Status: "Qualified"},
		{Session: "short", Answer: "None", RightAnswer: "None", Status: "Unknown"},
	}, details.Solved)
	require.Equal(t, []MadeFlip{{Cid: "0x09", RightAnswer: "None", Status: "Unknown", Answers: []FlipAnswer{}}}, details.Made)

	// A failed ceremony with no validation stats: empty lists, never nil.
	details, err = (&ceremonyData{}).flipDetails(own, nil)
	require.NoError(t, err)
	require.Equal(t, &FlipDetails{Solved: []SolvedFlip{}, Made: []MadeFlip{}}, details)
}

// A summary written by an older node (no flipDetails) still reads, and an older node reading a new one ignores the
// field (encoding/json skips unknown fields).
func Test_Summary_flipDetailsCompatible(t *testing.T) {
	var s Summary
	require.NoError(t, json.Unmarshal([]byte(`{"epoch":5,"participated":true}`), &s))
	require.Nil(t, s.FlipDetails)

	type olderSummary struct {
		Epoch        uint16 `json:"epoch"`
		Participated bool   `json:"participated"`
	}
	data, err := json.Marshal(Summary{Epoch: 5, Participated: true, FlipDetails: &FlipDetails{Solved: []SolvedFlip{{Cid: "x"}}}})
	require.NoError(t, err)
	var older olderSummary
	require.NoError(t, json.Unmarshal(data, &older))
	require.Equal(t, olderSummary{Epoch: 5, Participated: true}, older)
}
