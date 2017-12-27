package state

import (
	"bytes"
	"errors"
	"fmt"

	fail "github.com/ebuchman/fail-test"
	abci "github.com/tendermint/abci/types"
	crypto "github.com/tendermint/go-crypto"
	"github.com/tendermint/tendermint/proxy"
	"github.com/tendermint/tendermint/types"
	"github.com/tendermint/tmlibs/log"
)

//--------------------------------------------------
// Execute the block

// ValExecBlock executes the block, but does NOT mutate State.
// + validates the block
// + executes block.Txs on the proxyAppConn
func (s *State) ValExecBlock(txEventPublisher types.TxEventPublisher, proxyAppConn proxy.AppConnConsensus, block *types.Block) (*ABCIResponses, error) {
	// Validate the block.
	if err := s.validateBlock(block); err != nil {
		return nil, ErrInvalidBlock(err)
	}

	// Execute the block txs
	abciResponses, err := execBlockOnProxyApp(txEventPublisher, proxyAppConn, block, s.logger, s.LastValidators)
	if err != nil {
		// There was some error in proxyApp
		// TODO Report error and wait for proxyApp to be available.
		return nil, ErrProxyAppConn(err)
	}

	return abciResponses, nil
}

// Executes block's transactions on proxyAppConn.
// Returns a list of transaction results and updates to the validator set
// TODO: Generate a bitmap or otherwise store tx validity in state.
func execBlockOnProxyApp(txEventPublisher types.TxEventPublisher, proxyAppConn proxy.AppConnConsensus, block *types.Block, logger log.Logger, lastValidators *types.ValidatorSet) (*ABCIResponses, error) {
	var validTxs, invalidTxs = 0, 0

	txIndex := 0
	abciResponses := NewABCIResponses(block)

	// Execute transactions and get hash
	proxyCb := func(req *abci.Request, res *abci.Response) {
		switch r := res.Value.(type) {
		case *abci.Response_DeliverTx:
			// TODO: make use of res.Log
			// TODO: make use of this info
			// Blocks may include invalid txs.
			// reqDeliverTx := req.(abci.RequestDeliverTx)
			txResult := r.DeliverTx
			if txResult.Code == abci.CodeTypeOK {
				validTxs++
			} else {
				logger.Debug("Invalid tx", "code", txResult.Code, "log", txResult.Log)
				invalidTxs++
			}

			// NOTE: if we count we can access the tx from the block instead of
			// pulling it from the req
			txEventPublisher.PublishEventTx(types.EventDataTx{types.TxResult{
				Height: block.Height,
				Index:  uint32(txIndex),
				Tx:     types.Tx(req.GetDeliverTx().Tx),
				Result: *txResult,
			}})

			abciResponses.DeliverTx[txIndex] = txResult
			txIndex++
		}
	}
	proxyAppConn.SetResponseCallback(proxyCb)

	// determine which validators did not sign last block
	absentVals := make([]int32, 0)
	for valI, vote := range block.LastCommit.Precommits {
		if vote == nil {
			absentVals = append(absentVals, int32(valI))
		}
	}

	// Begin block
	_, err := proxyAppConn.BeginBlockSync(abci.RequestBeginBlock{
		Hash:                block.Hash(),
		Header:              types.TM2PB.Header(block.Header),
		AbsentValidators:    absentVals,
		ByzantineValidators: nil,
	})
	if err != nil {
		logger.Error("Error in proxyAppConn.BeginBlock", "err", err)
		return nil, err
	}

	// Run txs of block
	for _, tx := range block.Txs {
		proxyAppConn.DeliverTxAsync(tx)
		if err := proxyAppConn.Error(); err != nil {
			return nil, err
		}
	}

	// End block
	abciResponses.EndBlock, err = proxyAppConn.EndBlockSync(abci.RequestEndBlock{block.Height})
	if err != nil {
		logger.Error("Error in proxyAppConn.EndBlock", "err", err)
		return nil, err
	}

	logger.Info("Executed block", "height", block.Height, "validTxs", validTxs, "invalidTxs", invalidTxs)

	valUpdates := abciResponses.EndBlock.ValidatorUpdates
	if len(valUpdates) > 0 {
		logger.Info("Updates to validators", "updates", abci.ValidatorsString(valUpdates))
	}

	return abciResponses, nil
}

func updateValidators(currentSet *types.ValidatorSet, updates []*abci.Validator) error {
	// If more or equal than 1/3 of total voting power changed in one block, then
	// a light client could never prove the transition externally. See
	// ./lite/doc.go for details on how a light client tracks validators.
	vp23, err := changeInVotingPowerMoreOrEqualToOneThird(currentSet, updates)
	if err != nil {
		return err
	}
	if vp23 {
		return errors.New("the change in voting power must be strictly less than 1/3")
	}

	for _, v := range updates {
		pubkey, err := crypto.PubKeyFromBytes(v.PubKey) // NOTE: expects go-wire encoded pubkey
		if err != nil {
			return err
		}

		address := pubkey.Address()
		power := int64(v.Power)
		// mind the overflow from int64
		if power < 0 {
			return fmt.Errorf("Power (%d) overflows int64", v.Power)
		}

		_, val := currentSet.GetByAddress(address)
		if val == nil {
			// add val
			added := currentSet.Add(types.NewValidator(pubkey, power))
			if !added {
				return fmt.Errorf("Failed to add new validator %X with voting power %d", address, power)
			}
		} else if v.Power == 0 {
			// remove val
			_, removed := currentSet.Remove(address)
			if !removed {
				return fmt.Errorf("Failed to remove validator %X", address)
			}
		} else {
			// update val
			val.VotingPower = power
			updated := currentSet.Update(val)
			if !updated {
				return fmt.Errorf("Failed to update validator %X with voting power %d", address, power)
			}
		}
	}
	return nil
}

func changeInVotingPowerMoreOrEqualToOneThird(currentSet *types.ValidatorSet, updates []*abci.Validator) (bool, error) {
	threshold := currentSet.TotalVotingPower() * 1 / 3
	acc := int64(0)

	for _, v := range updates {
		pubkey, err := crypto.PubKeyFromBytes(v.PubKey) // NOTE: expects go-wire encoded pubkey
		if err != nil {
			return false, err
		}

		address := pubkey.Address()
		power := int64(v.Power)
		// mind the overflow from int64
		if power < 0 {
			return false, fmt.Errorf("Power (%d) overflows int64", v.Power)
		}

		_, val := currentSet.GetByAddress(address)
		if val == nil {
			acc += power
		} else {
			np := val.VotingPower - power
			if np < 0 {
				np = -np
			}
			acc += np
		}

		if acc >= threshold {
			return true, nil
		}
	}

	return false, nil
}

// return a bit array of validators that signed the last commit
// NOTE: assumes commits have already been authenticated
/* function is currently unused
func commitBitArrayFromBlock(block *types.Block) *cmn.BitArray {
	signed := cmn.NewBitArray(len(block.LastCommit.Precommits))
	for i, precommit := range block.LastCommit.Precommits {
		if precommit != nil {
			signed.SetIndex(i, true) // val_.LastCommitHeight = block.Height - 1
		}
	}
	return signed
}
*/

//-----------------------------------------------------
// Validate block

// ValidateBlock validates the block against the state.
func (s *State) ValidateBlock(block *types.Block) error {
	return s.validateBlock(block)
}

// MakeBlock builds a block with the given txs and commit from the current state.
func (s *State) MakeBlock(height int64, txs []types.Tx, commit *types.Commit) (*types.Block, *types.PartSet) {
	// build base block
	block := types.MakeBlock(height, txs, commit)

	// fill header with state data
	block.ChainID = s.ChainID
	block.TotalTxs = s.LastBlockTotalTx + block.NumTxs
	block.LastBlockID = s.LastBlockID
	block.ValidatorsHash = s.Validators.Hash()
	block.AppHash = s.AppHash
	block.ConsensusHash = s.LastConsensusParams.Hash()
	block.ResultsHash = s.LastResultHash

	return block, block.MakePartSet(s.ConsensusParams.BlockGossip.BlockPartSizeBytes)
}

func (s *State) validateBlock(b *types.Block) error {
	// Basic block validation.
	if err := b.ValidateBasic(); err != nil {
		return err
	}

	if b.ChainID != s.ChainID {
		return fmt.Errorf("Wrong Block.Header.ChainID. Expected %v, got %v", s.ChainID, b.ChainID)
	}
	if b.Height != s.LastBlockHeight+1 {
		return fmt.Errorf("Wrong Block.Header.Height. Expected %v, got %v", s.LastBlockHeight+1, b.Height)
	}
	/*	TODO: Determine bounds for Time
		See blockchain/reactor "stopSyncingDurationMinutes"

		if !b.Time.After(lastBlockTime) {
			return errors.New("Invalid Block.Header.Time")
		}
	*/

	newTxs := int64(len(b.Data.Txs))
	if b.TotalTxs != s.LastBlockTotalTx+newTxs {
		return fmt.Errorf("Wrong Block.Header.TotalTxs. Expected %v, got %v", s.LastBlockTotalTx+newTxs, b.TotalTxs)
	}
	if !b.LastBlockID.Equals(s.LastBlockID) {
		return fmt.Errorf("Wrong Block.Header.LastBlockID.  Expected %v, got %v", s.LastBlockID, b.LastBlockID)
	}

	if !bytes.Equal(b.AppHash, s.AppHash) {
		return fmt.Errorf("Wrong Block.Header.AppHash.  Expected %X, got %v", s.AppHash, b.AppHash)
	}
	if !bytes.Equal(b.ConsensusHash, s.LastConsensusParams.Hash()) {
		return fmt.Errorf("Wrong Block.Header.ConsensusHash.  Expected %X, got %v", s.LastConsensusParams.Hash(), b.ConsensusHash)
	}
	if !bytes.Equal(b.ResultsHash, s.LastResultHash) {
		return fmt.Errorf("Wrong Block.Header.ResultsHash.  Expected %X, got %v", s.LastResultHash, b.ResultsHash)
	}

	// Validate block LastCommit.
	if b.Height == 1 {
		if len(b.LastCommit.Precommits) != 0 {
			return errors.New("Block at height 1 (first block) should have no LastCommit precommits")
		}
	} else {
		if len(b.LastCommit.Precommits) != s.LastValidators.Size() {
			return fmt.Errorf("Invalid block commit size. Expected %v, got %v",
				s.LastValidators.Size(), len(b.LastCommit.Precommits))
		}
		err := s.LastValidators.VerifyCommit(
			s.ChainID, s.LastBlockID, b.Height-1, b.LastCommit)
		if err != nil {
			return err
		}
	}

	return nil
}

//-----------------------------------------------------------------------------
// ApplyBlock validates & executes the block, updates state w/ ABCI responses,
// then commits and updates the mempool atomically, then saves state.

// ApplyBlock validates the block against the state, executes it against the app,
// commits it, and saves the block and state. It's the only function that needs to be called
// from outside this package to process and commit an entire block.
func (s *State) ApplyBlock(txEventPublisher types.TxEventPublisher, proxyAppConn proxy.AppConnConsensus,
	block *types.Block, partsHeader types.PartSetHeader, mempool types.Mempool) error {

	abciResponses, err := s.ValExecBlock(txEventPublisher, proxyAppConn, block)
	if err != nil {
		return fmt.Errorf("Exec failed for application: %v", err)
	}

	fail.Fail() // XXX

	// save the results before we commit
	s.SaveABCIResponses(abciResponses)

	fail.Fail() // XXX

	// now update the block and validators
	err = s.SetBlockAndValidators(block.Header, partsHeader, abciResponses)
	if err != nil {
		return fmt.Errorf("Commit failed for application: %v", err)
	}

	// lock mempool, commit state, update mempoool
	err = s.CommitStateUpdateMempool(proxyAppConn, block, mempool)
	if err != nil {
		return fmt.Errorf("Commit failed for application: %v", err)
	}

	fail.Fail() // XXX

	// save the state and the validators
	s.Save()

	return nil
}

// CommitStateUpdateMempool locks the mempool, runs the ABCI Commit message, and updates the mempool.
// The Mempool must be locked during commit and update because state is typically reset on Commit and old txs must be replayed
// against committed state before new txs are run in the mempool, lest they be invalid.
func (s *State) CommitStateUpdateMempool(proxyAppConn proxy.AppConnConsensus, block *types.Block, mempool types.Mempool) error {
	mempool.Lock()
	defer mempool.Unlock()

	// Commit block, get hash back
	res, err := proxyAppConn.CommitSync()
	if err != nil {
		s.logger.Error("Client error during proxyAppConn.CommitSync", "err", err)
		return err
	}
	if res.IsErr() {
		s.logger.Error("Error in proxyAppConn.CommitSync", "err", res)
		return res
	}
	if res.Log != "" {
		s.logger.Debug("Commit.Log: " + res.Log)
	}

	s.logger.Info("Committed state", "height", block.Height, "txs", block.NumTxs, "hash", res.Data)

	// Set the state's new AppHash
	s.AppHash = res.Data

	// Update mempool.
	return mempool.Update(block.Height, block.Txs)
}

// ExecCommitBlock executes and commits a block on the proxyApp without validating or mutating the state.
// It returns the application root hash (result of abci.Commit).
func ExecCommitBlock(appConnConsensus proxy.AppConnConsensus, block *types.Block, logger log.Logger, lastValidators *types.ValidatorSet) ([]byte, error) {
	_, err := execBlockOnProxyApp(types.NopEventBus{}, appConnConsensus, block, logger, lastValidators)
	if err != nil {
		logger.Error("Error executing block on proxy app", "height", block.Height, "err", err)
		return nil, err
	}
	// Commit block, get hash back
	res, err := appConnConsensus.CommitSync()
	if err != nil {
		logger.Error("Client error during proxyAppConn.CommitSync", "err", res)
		return nil, err
	}
	if res.IsErr() {
		logger.Error("Error in proxyAppConn.CommitSync", "err", res)
		return nil, res
	}
	if res.Log != "" {
		logger.Info("Commit.Log: " + res.Log)
	}
	return res.Data, nil
}
