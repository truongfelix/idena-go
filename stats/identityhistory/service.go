// Package identityhistory gives an address's history from the node's own records (dna_identityHistory): the block
// that ended each epoch since the node's first header, what each ceremony did to the address (the node's validation
// summaries, and the identity changes of the ceremony block), the mining rewards of the node's own addresses and
// their transactions by epoch. Nothing comes from outside the node; recording only reads the state: it is
// consensus-neutral.
package identityhistory

import (
	"time"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/state"
	"github.com/idena-network/idena-go/log"
	"github.com/idena-network/idena-go/stats/validationsummary"
	dbm "github.com/tendermint/tm-db"
)

// Chain is what the history reads from the node.
type Chain interface {
	Head() *types.Header
	ReadonlyAppState() (*appstate.AppState, error)
	HeaderByHeight(height uint64) *types.Header
	CanonicalHash(height uint64) common.Hash
	// IdentityStateDiff: the identity changes of the block (nil: none, or the node does not have them).
	IdentityStateDiff(height uint64) *state.IdentityStateDiff
	// SavedTxs: the transactions of the node's own address (sent and received), newest first (bcn_transactions).
	SavedTxs(address common.Address, count int, token []byte) ([]*types.SavedTransaction, []byte)
}

// Service fills the ceremony index in the background and answers dna_identityHistory.
type Service struct {
	store     *Store
	chain     Chain
	db        dbm.DB
	summaries *validationsummary.Store
	own       func(common.Address) bool
	log       log.Logger

	wake chan struct{}
}

// NewService: db is the chain database (the first fill reads its stored headers).
func NewService(store *Store, chain Chain, db dbm.DB, summaries *validationsummary.Store, own func(common.Address) bool) *Service {
	return &Service{
		store:     store,
		chain:     chain,
		db:        db,
		summaries: summaries,
		own:       own,
		log:       log.New("component", "identityhistory"),
		wake:      make(chan struct{}, 1),
	}
}

// Start fills the index now and again whenever gaps signals one (blocks added after a fast sync).
func (s *Service) Start(gaps <-chan struct{}) {
	go func() {
		s.runFill()
		for {
			select {
			case <-gaps:
			case <-s.wake:
			}
			s.runFill()
		}
	}()
}

func (s *Service) requestFill() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) runFill() {
	start := time.Now()
	if err := s.fill(); err != nil {
		s.log.Error("Cannot find the ceremony blocks", "err", err, "duration", time.Since(start))
	}
}
