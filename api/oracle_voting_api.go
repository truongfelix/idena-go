package api

import (
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/stats/oraclevotings"
	"github.com/pkg/errors"
)

// OracleVotings lists the oracle votings with the indexer's (api.idena.io OracleVotingContracts) filters and fields,
// from the node's own index: the votings deployed while the node added blocks, plus the live ones its state holds.
// A voting terminated in blocks the node did not add is not listed.
func (api *ContractApi) OracleVotings(args oraclevotings.ListArgs) (*oraclevotings.ListResult, error) {
	return api.oracleVotings.Votings(args)
}

// OracleVoting returns one oracle voting, relative to the oracle (its state Voted, isOracle).
func (api *ContractApi) OracleVoting(contract common.Address, oracle *common.Address) (*oraclevotings.Voting, error) {
	return api.oracleVotings.Voting(contract, oracle)
}

type OracleVotingBalanceUpdatesArgs struct {
	Address           common.Address `json:"address"`
	Contract          common.Address `json:"contract"`
	Limit             int            `json:"limit"`
	ContinuationToken string         `json:"continuationToken"`
}

// OracleVotingBalanceUpdates returns the address's transactions with the voting and the payments the voting made to
// it, newest first. The node knows them for its own addresses only: the transactions they sent (its history) and
// the payments recorded since it runs a version with the index.
func (api *ContractApi) OracleVotingBalanceUpdates(args OracleVotingBalanceUpdatesArgs) (*oraclevotings.BalanceUpdatesResult, error) {
	if !api.oracleVotings.IsVoting(api.baseApi.getReadonlyAppState(), args.Contract) {
		return nil, errors.New("oracle voting not found")
	}
	var sent []*oraclevotings.BalanceRow
	var token []byte
	for {
		txs, next := api.bc.ReadTxs(args.Address, 100, token)
		for _, item := range txs {
			tx := item.Tx
			if sender, _ := types.Sender(tx); sender != args.Address {
				continue
			}
			var receipt *types.TxReceipt
			switch tx.Type {
			case types.SendTx, types.CallContractTx, types.TerminateContractTx:
				if tx.To == nil || *tx.To != args.Contract {
					continue
				}
				if tx.Type != types.SendTx {
					receipt = api.bc.GetReceipt(tx.Hash())
				}
			case types.DeployContractTx:
				if receipt = api.bc.GetReceipt(tx.Hash()); receipt == nil || receipt.ContractAddress != args.Contract {
					continue
				}
			default:
				continue
			}
			row := oraclevotings.NewBalanceRow(args.Address, args.Contract, tx, receipt, item.FeePerGas, 1)
			row.Time = item.Timestamp
			if header := api.bc.GetBlockHeader(item.BlockHash); header != nil {
				row.Height = header.Height()
			}
			sent = append(sent, row)
		}
		if len(next) == 0 {
			break
		}
		token = next
	}
	return api.oracleVotings.BalanceUpdates(args.Address, args.Contract, sent, args.Limit, args.ContinuationToken)
}
