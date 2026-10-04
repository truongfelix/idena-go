// Package validationsummary records, for every identity, what the validation ceremony did to it (answers, new
// state, penalty, rewards) while the node applies the ceremony block, and keeps it for the last epochs. It gives
// light clients (the desktop and phone apps) the validation report that only an indexer gave before
// (api.idena.io ValidationSummary), from their own node. Recording only reads the state: it is consensus-neutral.
package validationsummary

import (
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/state"
	"github.com/shopspring/decimal"
)

// Summary of one identity in one validation ceremony. Field names follow the indexer's ValidationSummary.
type Summary struct {
	Epoch   uint16         `json:"epoch"`
	Address common.Address `json:"address"`
	// Nobody was validated: identities stayed as they were and no reward was paid (states are then unknown).
	ValidationFailed bool `json:"validationFailed"`
	// The identity was a candidate of the ceremony (it could take part).
	Participated bool `json:"participated"`
	// State before the ceremony and after its block (as dna_identity shows it: a killed identity is cleared to
	// Undefined).
	PrevState string `json:"prevState"`
	State     string `json:"state"`
	// Approved: most participants saw the identity's short answers in time (evidence maps); otherwise its short
	// answers do not count. Missed: no answers in a session it had to answer, or not a candidate.
	Approved          bool           `json:"approved"`
	Missed            bool           `json:"missed"`
	Penalized         bool           `json:"penalized"`
	PenaltyReason     string         `json:"penaltyReason,omitempty"`
	WrongGrades       bool           `json:"wrongGrades"`
	MadeFlips         uint8          `json:"madeFlips"`
	ShortAnswers      AnswersSummary `json:"shortAnswers"`
	LongAnswers       AnswersSummary `json:"longAnswers"`
	ShortAnswersCount uint32         `json:"shortAnswersCount"`
	LongAnswersCount  uint32         `json:"longAnswersCount"`
	Rewards           Rewards        `json:"rewards"`
	// The part of the rewards paid to the identity's pool (delegatee) instead of the identity.
	DelegateeReward *DelegateeReward `json:"delegateeReward,omitempty"`
}

// AnswersSummary: points scored on the flips that counted (qualified flips).
type AnswersSummary struct {
	Point      float32 `json:"point"`
	FlipsCount uint32  `json:"flipsCount"`
}

// Rewards by type, in iDNA. Validation stays zero: since the staking upgrade the validation reward is paid as
// Staking (and Candidate for new identities).
type Rewards struct {
	Validation  Reward `json:"validation"`
	Flips       Reward `json:"flips"`
	ExtraFlips  Reward `json:"extraFlips"`
	Invitations Reward `json:"invitations"`
	Invitee     Reward `json:"invitee"`
	Reports     Reward `json:"reports"`
	Candidate   Reward `json:"candidate"`
	Staking     Reward `json:"staking"`
}

// Reward earned (balance + stake parts, whoever received the balance part) and missed. Missed is null when the
// node cannot tell (it is computed for Staking and Candidate); Reason says why a reward was lost when known:
// penalty, not_validated, missed (validation missed), grades_ignored (reports).
type Reward struct {
	Earned decimal.Decimal  `json:"earned"`
	Missed *decimal.Decimal `json:"missed"`
	Reason string           `json:"reason,omitempty"`
}

type DelegateeReward struct {
	Address common.Address  `json:"address"`
	Amount  decimal.Decimal `json:"amount"`
}

func stateName(s state.IdentityState) string {
	switch s {
	case state.Invite:
		return "Invite"
	case state.Candidate:
		return "Candidate"
	case state.Newbie:
		return "Newbie"
	case state.Verified:
		return "Verified"
	case state.Suspended:
		return "Suspended"
	case state.Zombie:
		return "Zombie"
	case state.Killed:
		return "Killed"
	case state.Human:
		return "Human"
	default:
		return "Undefined"
	}
}
