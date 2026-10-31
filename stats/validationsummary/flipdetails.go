package validationsummary

import (
	"encoding/hex"
	"fmt"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	statsTypes "github.com/idena-network/idena-go/stats/types"
	"github.com/ipfs/go-cid"
)

// FlipDetails: each flip of the ceremony an own address solved or made, as the ceremony judged it. Recorded for the
// node's own addresses only (Store.SetOwn), from the ceremony block the node applies itself: nil for an older
// version's record, another address, or a ceremony block the node did not apply (fast sync, node off).
type FlipDetails struct {
	// Solved: one item per flip and session, the short session first, in the order the flips were shown.
	Solved []SolvedFlip `json:"solved"`
	// Made: the address's own flips of the ceremony.
	Made []MadeFlip `json:"made"`
}

// SolvedFlip: a flip the address had to solve in one session.
type SolvedFlip struct {
	Cid string `json:"cid"`
	// Session: "short" or "long".
	Session string `json:"session"`
	// Answered: the address's answers for this session were read; Answer is "None" otherwise.
	Answered bool   `json:"answered"`
	Answer   string `json:"answer"`
	// Reported: the address reported the flip (long session only).
	Reported bool `json:"reported"`
	// Considered: the flip counted for the address's score (an extra short flip not used does not).
	Considered bool    `json:"considered"`
	Point      float32 `json:"point"`
	// What the ceremony decided for the flip: its answer, status and whether it ended reported.
	RightAnswer  string `json:"rightAnswer"`
	Status       string `json:"status"`
	FlipReported bool   `json:"flipReported"`
}

// MadeFlip: one of the address's flips, with every answer given to it.
type MadeFlip struct {
	Cid string `json:"cid"`
	// Judged: the flip was in the ceremony; false for a flip the ceremony did not show (its author not a candidate).
	Judged      bool   `json:"judged"`
	RightAnswer string `json:"rightAnswer"`
	Status      string `json:"status"`
	Reported    bool   `json:"reported"`
	// Reports: how many long answers reported it.
	Reports uint32       `json:"reports"`
	Answers []FlipAnswer `json:"answers"`
}

// FlipAnswer: how one participant answered a flip in one session.
type FlipAnswer struct {
	Respondent common.Address `json:"respondent"`
	Session    string         `json:"session"`
	Answer     string         `json:"answer"`
	Reported   bool           `json:"reported"`
	Considered bool           `json:"considered"`
}

const (
	sessionShort = "short"
	sessionLong  = "long"
)

func answerName(a types.Answer) string {
	switch a {
	case types.None:
		return "None"
	case types.Left:
		return "Left"
	case types.Right:
		return "Right"
	default:
		return "Unknown"
	}
}

func flipStatusName(status byte) string {
	switch status {
	case FlipNotQualified:
		return "NotQualified"
	case FlipQualified:
		return "Qualified"
	case FlipWeaklyQualified:
		return "WeaklyQualified"
	case FlipQualifiedByNone:
		return "QualifiedByNone"
	default:
		return "Unknown"
	}
}

// cidString: a flip's CID as the flip RPCs write it; hex when it cannot be read as a CID.
func cidString(raw []byte) string {
	if c, err := cid.Cast(raw); err == nil {
		return c.String()
	}
	return "0x" + hex.EncodeToString(raw)
}

// flipDetails builds the details of addr; madeCids are the CIDs of its flips in the ending epoch. It never panics:
// it runs right after a ceremony block is added, and a failure only drops the details (the summary is kept).
func (d *ceremonyData) flipDetails(addr common.Address, madeCids [][]byte) (details *FlipDetails, err error) {
	defer func() {
		if p := recover(); p != nil {
			details, err = nil, fmt.Errorf("flip details: %v", p)
		}
	}()
	details = &FlipDetails{Solved: []SolvedFlip{}, Made: []MadeFlip{}}
	if d.validation == nil {
		return details, nil
	}
	for _, shard := range d.validation.Shards {
		if shard == nil {
			continue
		}
		stats := shard.IdentitiesPerAddr[addr]
		if stats == nil {
			continue
		}
		details.Solved = append(details.Solved, solvedFlips(shard, addr, stats.ShortFlipsToSolve, sessionShort)...)
		details.Solved = append(details.Solved, solvedFlips(shard, addr, stats.LongFlipsToSolve, sessionLong)...)
	}
	for _, raw := range madeCids {
		details.Made = append(details.Made, d.madeFlip(raw))
	}
	return details, nil
}

func solvedFlips(shard *statsTypes.ValidationShardStats, addr common.Address, indexes []int, session string) []SolvedFlip {
	solved := make([]SolvedFlip, 0, len(indexes))
	for _, idx := range indexes {
		item := SolvedFlip{Session: session, Answer: answerName(types.None), RightAnswer: answerName(types.None), Status: flipStatusName(255)}
		if idx >= 0 && idx < len(shard.FlipCids) {
			item.Cid = cidString(shard.FlipCids[idx])
		}
		if flip := shard.FlipsPerIdx[idx]; flip != nil {
			item.RightAnswer = answerName(flip.Answer)
			item.Status = flipStatusName(flip.Status)
			item.FlipReported = flip.Grade == types.GradeReported
			answers := flip.ShortAnswers
			if session == sessionLong {
				answers = flip.LongAnswers
			}
			for _, answer := range answers {
				if answer.Respondent != addr {
					continue
				}
				item.Answered = true
				item.Answer = answerName(answer.Answer)
				item.Reported = answer.Grade == types.GradeReported
				item.Considered = answer.Considered
				item.Point = answer.Point
				break
			}
		}
		solved = append(solved, item)
	}
	return solved
}

func (d *ceremonyData) madeFlip(raw []byte) MadeFlip {
	made := MadeFlip{Cid: cidString(raw), RightAnswer: answerName(types.None), Status: flipStatusName(255), Answers: []FlipAnswer{}}
	for _, shard := range d.validation.Shards {
		if shard == nil {
			continue
		}
		for idx, flipCid := range shard.FlipCids {
			if string(flipCid) != string(raw) {
				continue
			}
			flip := shard.FlipsPerIdx[idx]
			if flip == nil {
				continue
			}
			made.Judged = true
			made.RightAnswer = answerName(flip.Answer)
			made.Status = flipStatusName(flip.Status)
			made.Reported = flip.Grade == types.GradeReported
			for _, answer := range flip.ShortAnswers {
				made.Answers = append(made.Answers, flipAnswer(answer, sessionShort))
			}
			for _, answer := range flip.LongAnswers {
				if answer.Grade == types.GradeReported {
					made.Reports++
				}
				made.Answers = append(made.Answers, flipAnswer(answer, sessionLong))
			}
			return made
		}
	}
	return made
}

func flipAnswer(answer statsTypes.FlipAnswerStats, session string) FlipAnswer {
	return FlipAnswer{
		Respondent: answer.Respondent,
		Session:    session,
		Answer:     answerName(answer.Answer),
		Reported:   answer.Grade == types.GradeReported,
		Considered: answer.Considered,
	}
}
