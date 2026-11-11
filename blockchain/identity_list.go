package blockchain

import (
	"bytes"

	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/core/appstate"
	"github.com/idena-network/idena-go/core/identitylist"
	"github.com/idena-network/idena-go/ipfs"
	"github.com/idena-network/idena-go/log"
	"github.com/ipfs/go-cid"
	"github.com/pkg/errors"
)

// isIdentityListBlock reports whether a block carries the identity list (consensus v13): each snapshot block
// (every SnapshotRange blocks outside the validation) and the block that applies the ceremony's results.
func isIdentityListBlock(cfg *config.ConsensusConf, flags types.BlockFlag) bool {
	return cfg.EnableUpgrade13 && (flags.HasFlag(types.Snapshot) || flags.HasFlag(types.ValidationFinished))
}

// buildIdentityList builds the identity list of the state after the block, once applyBlockOnState or
// applyEmptyBlockOnState ran on it; nil for a block that carries none. A list that cannot be built leaves the field
// empty, the same on every node: no state holds one identity twice, and the index's 256 KiB hold the CIDs of about
// 7,000 pages, 7 million identities.
func (chain *Blockchain) buildIdentityList(appState *appstate.AppState, block *types.Block) *identitylist.List {
	if !isIdentityListBlock(chain.consensusRules(appState), block.Header.Flags()) {
		return nil
	}
	list, err := identitylist.FromState(block.Height(), appState.State)
	if err != nil {
		log.Error("Identity list not built", "height", block.Height(), "err", err)
		return nil
	}
	return list
}

func identityListCidBytes(list *identitylist.List) []byte {
	if list == nil {
		return nil
	}
	return list.IndexCid.Bytes()
}

func validateIdentityListCid(list *identitylist.List, block *types.Block) error {
	if expected := identityListCidBytes(list); !bytes.Equal(expected, block.IdentityListCid()) {
		return errors.Errorf("invalid identity list cid. Expected=%x, actual=%x", expected, block.IdentityListCid())
	}
	return nil
}

// pinIdentityList stores and pins the list of a block just added. The block is valid whatever happens here: a list
// IPFS fails to store is logged, and any node can still build it from the state.
func (chain *Blockchain) pinIdentityList(block *types.Block, list *identitylist.List) {
	if list == nil {
		return
	}
	if err := chain.identityLists.Add(list, block.Header.Flags().HasFlag(types.ValidationFinished)); err != nil {
		log.Error("Identity list not pinned", "height", block.Height(), "err", err)
	}
}

// IdentityList reads the identity list a block carries, from IPFS (this node's pins or its peers), and checks each
// block read against the CIDs: the index's against the header, each page's against the index. Height 0 reads the
// last list this node pinned.
func (chain *Blockchain) IdentityList(height uint64) (cid.Cid, *identitylist.Index, []identitylist.Row, error) {
	indexCid, index, err := chain.IdentityListIndex(height)
	if err != nil {
		return cid.Cid{}, nil, nil, err
	}
	var rows []identitylist.Row
	for i := range index.PageCids {
		page, err := chain.IdentityListPage(index, i)
		if err != nil {
			return cid.Cid{}, nil, nil, err
		}
		rows = append(rows, page...)
	}
	if uint32(len(rows)) != index.Count {
		return cid.Cid{}, nil, nil, errors.Errorf("identity list holds %v identities, its index says %v", len(rows), index.Count)
	}
	return indexCid, index, rows, nil
}

// IdentityListIndex reads and checks the index of the identity list a block carries (height 0: the last one this
// node pinned on its chain).
func (chain *Blockchain) IdentityListIndex(height uint64) (cid.Cid, *identitylist.Index, error) {
	if height == 0 {
		// The last pinned list still on this chain: after a rollback, lists above the head or of another branch
		// stay recorded until the next list replaces them.
		lists, err := chain.identityLists.Lists()
		if err != nil {
			return cid.Cid{}, nil, err
		}
		for i := len(lists) - 1; i >= 0 && height == 0; i-- {
			if lists[i].Height > chain.Head.Height() {
				continue
			}
			if h := chain.GetBlockHeaderByHeight(lists[i].Height); h != nil && bytes.Equal(h.IdentityListCid(), lists[i].IndexCid.Bytes()) {
				height = lists[i].Height
			}
		}
		if height == 0 {
			return cid.Cid{}, nil, errors.New("no identity list pinned on this chain yet")
		}
	}
	header := chain.GetBlockHeaderByHeight(height)
	if header == nil {
		return cid.Cid{}, nil, errors.Errorf("no block at height %v", height)
	}
	indexCid, err := cid.Cast(header.IdentityListCid())
	if err != nil {
		return cid.Cid{}, nil, errors.Errorf("block %v carries no identity list", height)
	}
	data, err := chain.ipfs.Get(indexCid.Bytes(), ipfs.Block)
	if err != nil {
		return cid.Cid{}, nil, errors.Wrap(err, "identity list index")
	}
	if c, err := identitylist.Cid(data); err != nil || !c.Equals(indexCid) {
		return cid.Cid{}, nil, errors.New("identity list index does not match the header")
	}
	index, err := identitylist.DecodeIndex(data)
	if err != nil {
		return cid.Cid{}, nil, err
	}
	if index.Height != height {
		return cid.Cid{}, nil, errors.Errorf("identity list index is for block %v, not %v", index.Height, height)
	}
	return indexCid, index, nil
}

// IdentityListPage reads and checks page i of a list.
func (chain *Blockchain) IdentityListPage(index *identitylist.Index, i int) ([]identitylist.Row, error) {
	if i < 0 || i >= len(index.PageCids) {
		return nil, errors.Errorf("the list has %v pages, no page %v", len(index.PageCids), i)
	}
	pageCid := index.PageCids[i]
	data, err := chain.ipfs.Get(pageCid.Bytes(), ipfs.Block)
	if err != nil {
		return nil, errors.Wrapf(err, "identity list page %v", i)
	}
	if c, err := identitylist.Cid(data); err != nil || !c.Equals(pageCid) {
		return nil, errors.Errorf("identity list page %v does not match the index", i)
	}
	return identitylist.DecodePage(data)
}
