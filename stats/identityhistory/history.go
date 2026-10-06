package identityhistory

import (
	"math/big"
	"sort"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/stats/validationsummary"
	"github.com/shopspring/decimal"
)

// History of an address as this node can tell it from its own records.
type History struct {
	Address common.Address `json:"address"`
	// Own: one of the node's addresses (its coinbase or a keystore account). Its validation summaries are kept for
	// good and its mining and transactions are recorded; another address has the summaries of the last
	// validationsummary.KeptEpochs epochs only.
	Own   bool   `json:"own"`
	Epoch uint16 `json:"epoch"`
	// CeremoniesComplete: every epoch since the node's first header has its ceremony block. False while the node is
	// still looking for them: the first time (about a minute), and after a fast sync (seconds).
	CeremoniesComplete bool `json:"ceremoniesComplete"`
	// Epochs: the current epoch first, then every epoch the node has something about, newest first.
	Epochs []*Epoch `json:"epochs"`
	// Scores: the identity's last validation scores, oldest first (dna_identity's totals are made of them). Epoch is
	// set only when the node can tell which ceremony gave the score.
	Scores []*Score `json:"scores"`
}

type Epoch struct {
	Epoch uint16 `json:"epoch"`
	// Ceremony: the block that ended the epoch; null for the current epoch and while the node has not placed it.
	Ceremony *Ceremony `json:"ceremony"`
	// Validated: whether the address was in the validator set after the ceremony block, as the block's identity
	// changes wrote it (a ceremony block writes every address it processed, unchanged ones included); null when the
	// block wrote nothing for the address, or the node does not know the block.
	Validated *bool `json:"validated"`
	// Summary: dna_validationSummary of the epoch, null when the node did not record it.
	Summary *validationsummary.Summary `json:"summary"`
	// Mining: the own address's block rewards in the epoch, null when none were recorded.
	Mining *MiningSummary `json:"mining"`
	// Transactions: what the own address sent in the epoch, null when the node has no transaction of it then.
	Transactions *Transactions `json:"transactions"`
}

// MiningSummary is Mining in iDNA.
type MiningSummary struct {
	ProposedBlocks  uint32          `json:"proposedBlocks"`
	ProposerReward  decimal.Decimal `json:"proposerReward"`
	CommitteeBlocks uint32          `json:"committeeBlocks"`
	CommitteeReward decimal.Decimal `json:"committeeReward"`
	PenaltyBurnt    decimal.Decimal `json:"penaltyBurnt"`
}

type Transactions struct {
	Sent         uint32 `json:"sent"`
	Flips        uint32 `json:"flips"`
	ShortAnswers bool   `json:"shortAnswers"`
	LongAnswers  bool   `json:"longAnswers"`
}

type Score struct {
	Epoch       *uint16 `json:"epoch"`
	ShortPoints float32 `json:"shortPoints"`
	ShortFlips  uint32  `json:"shortFlips"`
}

// History answers dna_identityHistory.
func (s *Service) History(address common.Address) (*History, error) {
	appState, err := s.chain.ReadonlyAppState()
	if err != nil {
		return nil, err
	}
	current := appState.State.Epoch()
	identity := appState.State.GetIdentity(address)
	own := s.own(address)

	ceremonies, err := s.store.Ceremonies()
	if err != nil {
		return nil, err
	}
	m, err := s.store.readMeta()
	if err != nil {
		return nil, err
	}
	// At epoch 0 (a new network) no epoch has ended yet: nothing to find.
	complete := current == 0 || m.Passed && ceremonies[current-1] != nil
	oldest := current
	for epoch := range ceremonies {
		oldest = min(oldest, epoch)
	}
	for epoch := oldest; complete && epoch < current; epoch++ {
		complete = ceremonies[epoch] != nil
	}
	if !complete {
		s.requestFill()
	}

	var mining map[uint16]*Mining
	var txs map[uint16]*Transactions
	if own {
		if mining, err = s.store.MiningEpochs(address); err != nil {
			return nil, err
		}
		txs = s.transactions(address)
	}
	if current >= validationsummary.KeptEpochs {
		oldest = min(oldest, current-validationsummary.KeptEpochs)
	} else {
		oldest = 0
	}
	for epoch := range mining {
		oldest = min(oldest, epoch)
	}
	for epoch := range txs {
		oldest = min(oldest, epoch)
	}

	h := &History{Address: address, Own: own, Epoch: current, CeremoniesComplete: complete}
	summaries := make(map[uint16]*validationsummary.Summary)
	for epoch := int(current); epoch >= int(oldest); epoch-- {
		e := &Epoch{Epoch: uint16(epoch), Ceremony: ceremonies[uint16(epoch)], Transactions: txs[uint16(epoch)]}
		if e.Ceremony != nil {
			e.Validated = s.validatedBy(e.Ceremony.Height, address)
		}
		if uint16(epoch) < current {
			if e.Summary, err = s.summaries.Get(uint16(epoch), address); err != nil {
				return nil, err
			}
			summaries[uint16(epoch)] = e.Summary
		}
		if record := mining[uint16(epoch)]; record != nil {
			e.Mining = &MiningSummary{
				ProposedBlocks:  record.ProposedBlocks,
				ProposerReward:  toDna(record.ProposerReward),
				CommitteeBlocks: record.CommitteeBlocks,
				CommitteeReward: toDna(record.CommitteeReward),
				PenaltyBurnt:    toDna(record.PenaltyBurnt),
			}
		}
		if e.Ceremony == nil && e.Summary == nil && e.Mining == nil && e.Transactions == nil && uint16(epoch) != current {
			continue
		}
		h.Epochs = append(h.Epochs, e)
	}
	h.Scores = labelScores(identity.Scores, current, summaries, txs)
	return h, nil
}

// validatedBy reads the identity changes of a ceremony block (kept for every block, fast sync included): a deleted
// entry is an address out of the validator set.
func (s *Service) validatedBy(height uint64, address common.Address) *bool {
	diff := s.chain.IdentityStateDiff(height)
	if diff == nil {
		return nil
	}
	for _, value := range diff.Values {
		if value.Address != address {
			continue
		}
		validated := false
		if !value.Deleted {
			approved := new(state.ApprovedIdentity)
			if err := approved.FromBytes(value.Value); err != nil {
				return nil
			}
			validated = approved.Validated
		}
		return &validated
	}
	return nil
}

// transactions groups what the address sent by the epoch the transactions carry.
func (s *Service) transactions(address common.Address) map[uint16]*Transactions {
	result := make(map[uint16]*Transactions)
	var token []byte
	for {
		saved, next := s.chain.SavedTxs(address, 500, token)
		for _, item := range saved {
			tx := item.Tx
			if sender, _ := types.Sender(tx); sender != address {
				continue
			}
			t := result[tx.Epoch]
			if t == nil {
				t = &Transactions{}
				result[tx.Epoch] = t
			}
			t.Sent++
			switch tx.Type {
			case types.SubmitFlipTx:
				t.Flips++
			case types.SubmitShortAnswersTx:
				t.ShortAnswers = true
			case types.SubmitLongAnswersTx:
				t.LongAnswers = true
			}
		}
		if next == nil || len(saved) == 0 {
			return result
		}
		token = next
	}
}

// labelScores gives each score the epoch of the ceremony that added it, newest first, as far as it is known
// exactly. A ceremony adds a score when the identity took part without missing it (ceremony.go AddNewScore). The
// summaries tell it per epoch; before them, the epochs where the address sent both answers count only when they
// match the remaining scores one for one (a validation can be missed with both answers sent, and the transaction
// history can have holes): else those scores stay without epoch.
func labelScores(scores []byte, current uint16, summaries map[uint16]*validationsummary.Summary,
	txs map[uint16]*Transactions) []*Score {
	result := make([]*Score, len(scores))
	for i, score := range scores {
		points, flips := common.DecodeScore(score)
		result[i] = &Score{ShortPoints: points, ShortFlips: flips}
	}
	next := len(result) - 1
	epoch := int(current) - 1
	for ; next >= 0 && epoch >= 0; epoch-- {
		summary := summaries[uint16(epoch)]
		if summary == nil || summary.ValidationFailed {
			break
		}
		if summary.Participated && !summary.Missed {
			e := uint16(epoch)
			result[next].Epoch = &e
			next--
		}
	}
	if next < 0 || epoch < 0 {
		return result
	}
	var answered []uint16
	for e, t := range txs {
		if int(e) <= epoch && t.ShortAnswers && t.LongAnswers {
			answered = append(answered, e)
		}
	}
	if len(answered) != next+1 {
		return result
	}
	sort.Slice(answered, func(i, j int) bool { return answered[i] > answered[j] })
	for _, e := range answered {
		e := e
		result[next].Epoch = &e
		next--
	}
	return result
}

// toDna: iDNA from the smallest unit, as blockchain.ConvertToFloat.
func toDna(amount *big.Int) decimal.Decimal {
	if amount == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(amount, 0).DivRound(decimal.NewFromBigInt(common.DnaBase, 0), 18)
}
