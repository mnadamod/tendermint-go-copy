package state

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"sync"
	"time"

	abci "github.com/tendermint/abci/types"
	"github.com/tendermint/go-wire/data"
	"golang.org/x/crypto/ripemd160"

	cmn "github.com/tendermint/tmlibs/common"
	dbm "github.com/tendermint/tmlibs/db"
	"github.com/tendermint/tmlibs/log"
	"github.com/tendermint/tmlibs/merkle"

	wire "github.com/tendermint/go-wire"

	"github.com/tendermint/tendermint/types"
)

// database keys
var (
	stateKey         = []byte("stateKey")
	abciResponsesKey = []byte("abciResponsesKey")
)

func calcValidatorsKey(height int64) []byte {
	return []byte(cmn.Fmt("validatorsKey:%v", height))
}

func calcConsensusParamsKey(height int64) []byte {
	return []byte(cmn.Fmt("consensusParamsKey:%v", height))
}

func calcResultsKey(height int64) []byte {
	return []byte(cmn.Fmt("resultsKey:%v", height))
}

//-----------------------------------------------------------------------------

// State is a short description of the latest committed block of the Tendermint consensus.
// It keeps all information necessary to validate new blocks,
// including the last validator set and the consensus params.
// All fields are exposed so the struct can be easily serialized,
// but the fields should only be changed by calling state.SetBlockAndValidators.
// NOTE: not goroutine-safe.
type State struct {
	// mtx for writing to db
	mtx sync.Mutex
	db  dbm.DB

	// Immutable
	ChainID string

	// Exposed fields are updated by SetBlockAndValidators.

	// LastBlockHeight=0 at genesis (ie. block(H=0) does not exist)
	LastBlockHeight  int64
	LastBlockTotalTx int64
	LastBlockID      types.BlockID
	LastBlockTime    time.Time

	// LastValidators is used to validate block.LastCommit.
	// Validators are persisted to the database separately every time they change,
	// so we can query for historical validator sets.
	// Note that if s.LastBlockHeight causes a valset change,
	// we set s.LastHeightValidatorsChanged = s.LastBlockHeight + 1
	Validators                  *types.ValidatorSet
	LastValidators              *types.ValidatorSet
	LastHeightValidatorsChanged int64

	// Consensus parameters used for validating blocks.
	// Changes returned by EndBlock and updated after Commit.
	ConsensusParams                  types.ConsensusParams
	LastConsensusParams              types.ConsensusParams
	LastHeightConsensusParamsChanged int64

	// Store LastABCIResults along with hash
	LastResults        ABCIResults // TODO: remove??
	LastResultHash     []byte      // this is the one for the next block to propose
	LastLastResultHash []byte      // this verifies the last block?

	// The latest AppHash we've received from calling abci.Commit()
	AppHash []byte

	logger log.Logger
}

// GetState loads the most recent state from the database,
// or creates a new one from the given genesisFile and persists the result
// to the database.
func GetState(stateDB dbm.DB, genesisFile string) (*State, error) {
	state := LoadState(stateDB)
	if state == nil {
		var err error
		state, err = MakeGenesisStateFromFile(stateDB, genesisFile)
		if err != nil {
			return nil, err
		}
		state.Save()
	}

	return state, nil
}

// LoadState loads the State from the database.
func LoadState(db dbm.DB) *State {
	return loadState(db, stateKey)
}

func loadState(db dbm.DB, key []byte) *State {
	buf := db.Get(key)
	if len(buf) == 0 {
		return nil
	}

	s := &State{db: db}
	r, n, err := bytes.NewReader(buf), new(int), new(error)
	wire.ReadBinaryPtr(&s, r, 0, n, err)
	if *err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmn.Exit(cmn.Fmt(`LoadState: Data has been corrupted or its spec has changed:
                %v\n`, *err))
	}
	// TODO: ensure that buf is completely read.

	return s
}

// SetLogger sets the logger on the State.
func (s *State) SetLogger(l log.Logger) {
	s.logger = l
}

// Copy makes a copy of the State for mutating.
func (s *State) Copy() *State {
	return &State{
		db: s.db,

		ChainID: s.ChainID,

		LastBlockHeight:  s.LastBlockHeight,
		LastBlockTotalTx: s.LastBlockTotalTx,
		LastBlockID:      s.LastBlockID,
		LastBlockTime:    s.LastBlockTime,

		Validators:                  s.Validators.Copy(),
		LastValidators:              s.LastValidators.Copy(),
		LastHeightValidatorsChanged: s.LastHeightValidatorsChanged,

		ConsensusParams:                  s.ConsensusParams,
		LastConsensusParams:              s.LastConsensusParams,
		LastHeightConsensusParamsChanged: s.LastHeightConsensusParamsChanged,

		AppHash: s.AppHash,

		LastResults:    s.LastResults,
		LastResultHash: s.LastResultHash,

		logger: s.logger,
	}
}

// Save persists the State to the database.
func (s *State) Save() {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	s.saveValidatorsInfo()
	s.saveConsensusParamsInfo()
	s.saveResults()
	s.db.SetSync(stateKey, s.Bytes())
}

// SaveABCIResponses persists the ABCIResponses to the database.
// This is useful in case we crash after app.Commit and before s.Save().
func (s *State) SaveABCIResponses(abciResponses *ABCIResponses) {
	s.db.SetSync(abciResponsesKey, abciResponses.Bytes())
}

// LoadABCIResponses loads the ABCIResponses from the database.
// This is useful for recovering from crashes where we called app.Commit and before we called
// s.Save()
func (s *State) LoadABCIResponses() *ABCIResponses {
	buf := s.db.Get(abciResponsesKey)
	if len(buf) == 0 {
		return nil
	}

	abciResponses := new(ABCIResponses)
	r, n, err := bytes.NewReader(buf), new(int), new(error)
	wire.ReadBinaryPtr(abciResponses, r, 0, n, err)
	if *err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmn.Exit(cmn.Fmt(`LoadABCIResponses: Data has been corrupted or its spec has
                changed: %v\n`, *err))
	}
	// TODO: ensure that buf is completely read.

	return abciResponses
}

// LoadValidators loads the ValidatorSet for a given height.
func (s *State) LoadValidators(height int64) (*types.ValidatorSet, error) {
	valInfo := s.loadValidatorsInfo(height)
	if valInfo == nil {
		return nil, ErrNoValSetForHeight{height}
	}

	if valInfo.ValidatorSet == nil {
		valInfo = s.loadValidatorsInfo(valInfo.LastHeightChanged)
		if valInfo == nil {
			cmn.PanicSanity(fmt.Sprintf(`Couldn't find validators at height %d as
                        last changed from height %d`, valInfo.LastHeightChanged, height))
		}
	}

	return valInfo.ValidatorSet, nil
}

func (s *State) loadValidatorsInfo(height int64) *ValidatorsInfo {
	buf := s.db.Get(calcValidatorsKey(height))
	if len(buf) == 0 {
		return nil
	}

	v := new(ValidatorsInfo)
	r, n, err := bytes.NewReader(buf), new(int), new(error)
	wire.ReadBinaryPtr(v, r, 0, n, err)
	if *err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmn.Exit(cmn.Fmt(`LoadValidators: Data has been corrupted or its spec has changed:
                %v\n`, *err))
	}
	// TODO: ensure that buf is completely read.

	return v
}

// saveValidatorsInfo persists the validator set for the next block to disk.
// It should be called from s.Save(), right before the state itself is persisted.
// If the validator set did not change after processing the latest block,
// only the last height for which the validators changed is persisted.
func (s *State) saveValidatorsInfo() {
	changeHeight := s.LastHeightValidatorsChanged
	nextHeight := s.LastBlockHeight + 1
	valInfo := &ValidatorsInfo{
		LastHeightChanged: changeHeight,
	}
	if changeHeight == nextHeight {
		valInfo.ValidatorSet = s.Validators
	}
	s.db.SetSync(calcValidatorsKey(nextHeight), valInfo.Bytes())
}

// LoadConsensusParams loads the ConsensusParams for a given height.
func (s *State) LoadConsensusParams(height int64) (types.ConsensusParams, error) {
	empty := types.ConsensusParams{}

	paramsInfo := s.loadConsensusParamsInfo(height)
	if paramsInfo == nil {
		return empty, ErrNoConsensusParamsForHeight{height}
	}

	if paramsInfo.ConsensusParams == empty {
		paramsInfo = s.loadConsensusParamsInfo(paramsInfo.LastHeightChanged)
		if paramsInfo == nil {
			cmn.PanicSanity(fmt.Sprintf(`Couldn't find consensus params at height %d as
                        last changed from height %d`, paramsInfo.LastHeightChanged, height))
		}
	}

	return paramsInfo.ConsensusParams, nil
}

func (s *State) loadConsensusParamsInfo(height int64) *ConsensusParamsInfo {
	buf := s.db.Get(calcConsensusParamsKey(height))
	if len(buf) == 0 {
		return nil
	}

	paramsInfo := new(ConsensusParamsInfo)
	r, n, err := bytes.NewReader(buf), new(int), new(error)
	wire.ReadBinaryPtr(paramsInfo, r, 0, n, err)
	if *err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmn.Exit(cmn.Fmt(`LoadConsensusParams: Data has been corrupted or its spec has changed:
                %v\n`, *err))
	}
	// TODO: ensure that buf is completely read.

	return paramsInfo
}

// saveConsensusParamsInfo persists the consensus params for the next block to disk.
// It should be called from s.Save(), right before the state itself is persisted.
// If the consensus params did not change after processing the latest block,
// only the last height for which they changed is persisted.
func (s *State) saveConsensusParamsInfo() {
	changeHeight := s.LastHeightConsensusParamsChanged
	nextHeight := s.LastBlockHeight + 1
	paramsInfo := &ConsensusParamsInfo{
		LastHeightChanged: changeHeight,
	}
	if changeHeight == nextHeight {
		paramsInfo.ConsensusParams = s.ConsensusParams
	}
	s.db.SetSync(calcConsensusParamsKey(nextHeight), paramsInfo.Bytes())
}

// LoadResults loads the ABCIResults for a given height.
func (s *State) LoadResults(height int64) (ABCIResults, error) {
	resInfo := s.loadResults(height)
	if resInfo == nil {
		return nil, ErrNoResultsForHeight{height}
	}
	return resInfo, nil
}

func (s *State) loadResults(height int64) ABCIResults {
	buf := s.db.Get(calcResultsKey(height))
	if len(buf) == 0 {
		return nil
	}

	v := new(ABCIResults)
	err := wire.ReadBinaryBytes(buf, v)
	if err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmn.Exit(cmn.Fmt(`LoadResults: Data has been corrupted or its spec has changed:
                %v\n`, err))
	}
	return *v
}

// saveResults persists the results for the last block to disk.
// It should be called from s.Save(), right before the state itself is persisted.
func (s *State) saveResults() {
	nextHeight := s.LastBlockHeight + 1
	results := s.LastResults
	s.db.SetSync(calcResultsKey(nextHeight), results.Bytes())
}

// Equals returns true if the States are identical.
func (s *State) Equals(s2 *State) bool {
	return bytes.Equal(s.Bytes(), s2.Bytes())
}

// Bytes serializes the State using go-wire.
func (s *State) Bytes() []byte {
	return wire.BinaryBytes(s)
}

// SetBlockAndValidators mutates State variables
// to update block and validators after running EndBlock.
func (s *State) SetBlockAndValidators(header *types.Header, blockPartsHeader types.PartSetHeader,
	abciResponses *ABCIResponses) error {

	// copy the valset so we can apply changes from EndBlock
	// and update s.LastValidators and s.Validators
	prevValSet := s.Validators.Copy()
	nextValSet := prevValSet.Copy()

	// update the validator set with the latest abciResponses
	if len(abciResponses.EndBlock.ValidatorUpdates) > 0 {
		err := updateValidators(nextValSet, abciResponses.EndBlock.ValidatorUpdates)
		if err != nil {
			return fmt.Errorf("Error changing validator set: %v", err)
		}
		// change results from this height but only applies to the next height
		s.LastHeightValidatorsChanged = header.Height + 1
	}

	// Update validator accums and set state variables
	nextValSet.IncrementAccum(1)

	// update the params with the latest abciResponses
	nextParams := s.ConsensusParams
	if abciResponses.EndBlock.ConsensusParamUpdates != nil {
		// NOTE: must not mutate s.ConsensusParams
		nextParams = s.ConsensusParams.Update(abciResponses.EndBlock.ConsensusParamUpdates)
		err := nextParams.Validate()
		if err != nil {
			return fmt.Errorf("Error updating consensus params: %v", err)
		}
		// change results from this height but only applies to the next height
		s.LastHeightConsensusParamsChanged = header.Height + 1
	}

	s.setBlockAndValidators(header.Height,
		header.NumTxs,
		types.BlockID{header.Hash(), blockPartsHeader},
		header.Time,
		nextValSet,
		nextParams,
		NewResults(abciResponses.DeliverTx))
	return nil
}

func (s *State) setBlockAndValidators(height int64,
	newTxs int64, blockID types.BlockID, blockTime time.Time,
	valSet *types.ValidatorSet,
	params types.ConsensusParams,
	results ABCIResults) {

	s.LastBlockHeight = height
	s.LastBlockTotalTx += newTxs
	s.LastBlockID = blockID
	s.LastBlockTime = blockTime

	s.LastValidators = s.Validators.Copy()
	s.Validators = valSet

	s.LastConsensusParams = s.ConsensusParams
	s.ConsensusParams = params

	s.LastResults = results
	s.LastResultHash = results.Hash()
}

// GetValidators returns the last and current validator sets.
func (s *State) GetValidators() (last *types.ValidatorSet, current *types.ValidatorSet) {
	return s.LastValidators, s.Validators
}

//------------------------------------------------------------------------

// ABCIResponses retains the responses of the various ABCI calls during block processing.
// It is persisted to disk before calling Commit.
type ABCIResponses struct {
	Height int64

	DeliverTx []*abci.ResponseDeliverTx
	EndBlock  *abci.ResponseEndBlock

	txs types.Txs // reference for indexing results by hash
}

// NewABCIResponses returns a new ABCIResponses
func NewABCIResponses(block *types.Block) *ABCIResponses {
	return &ABCIResponses{
		Height:    block.Height,
		DeliverTx: make([]*abci.ResponseDeliverTx, block.NumTxs),
		txs:       block.Data.Txs,
	}
}

// Bytes serializes the ABCIResponse using go-wire
func (a *ABCIResponses) Bytes() []byte {
	return wire.BinaryBytes(*a)
}

//-----------------------------------------------------------------------------

// ABCIResult is just the essential info to prove
// success/failure of a DeliverTx
type ABCIResult struct {
	Code uint32     `json:"code"`
	Data data.Bytes `json:"data"`
}

// Hash creates a canonical json hash of the ABCIResult
func (a ABCIResult) Hash() []byte {
	// stupid canonical json output, easy to check in any language
	bs := fmt.Sprintf(`{"code":%d,"data":"%s"}`, a.Code, a.Data)
	var hasher = ripemd160.New()
	hasher.Write([]byte(bs))
	return hasher.Sum(nil)
}

// ABCIResults wraps the deliver tx results to return a proof
type ABCIResults []ABCIResult

// NewResults creates ABCIResults from ResponseDeliverTx
func NewResults(del []*abci.ResponseDeliverTx) ABCIResults {
	res := make(ABCIResults, len(del))
	for i, d := range del {
		res[i] = ABCIResult{
			Code: d.Code,
			Data: d.Data,
		}
	}
	return res
}

// Bytes serializes the ABCIResponse using go-wire
func (a ABCIResults) Bytes() []byte {
	return wire.BinaryBytes(a)
}

// Hash returns a merkle hash of all results
func (a ABCIResults) Hash() []byte {
	return merkle.SimpleHashFromHashables(a.toHashables())
}

// ProveResult returns a merkle proof of one result from the set
func (a ABCIResults) ProveResult(i int) merkle.SimpleProof {
	_, proofs := merkle.SimpleProofsFromHashables(a.toHashables())
	return *proofs[i]
}

func (a ABCIResults) toHashables() []merkle.Hashable {
	l := len(a)
	hashables := make([]merkle.Hashable, l)
	for i := 0; i < l; i++ {
		hashables[i] = a[i]
	}
	return hashables
}

//-----------------------------------------------------------------------------

// ValidatorsInfo represents the latest validator set, or the last height it changed
type ValidatorsInfo struct {
	ValidatorSet      *types.ValidatorSet
	LastHeightChanged int64
}

// Bytes serializes the ValidatorsInfo using go-wire
func (valInfo *ValidatorsInfo) Bytes() []byte {
	return wire.BinaryBytes(*valInfo)
}

//-----------------------------------------------------------------------------

// ConsensusParamsInfo represents the latest consensus params, or the last height it changed
type ConsensusParamsInfo struct {
	ConsensusParams   types.ConsensusParams
	LastHeightChanged int64
}

// Bytes serializes the ConsensusParamsInfo using go-wire
func (params ConsensusParamsInfo) Bytes() []byte {
	return wire.BinaryBytes(params)
}

//------------------------------------------------------------------------
// Genesis

// MakeGenesisStateFromFile reads and unmarshals state from the given
// file.
//
// Used during replay and in tests.
func MakeGenesisStateFromFile(db dbm.DB, genDocFile string) (*State, error) {
	genDoc, err := MakeGenesisDocFromFile(genDocFile)
	if err != nil {
		return nil, err
	}
	return MakeGenesisState(db, genDoc)
}

// MakeGenesisDocFromFile reads and unmarshals genesis doc from the given file.
func MakeGenesisDocFromFile(genDocFile string) (*types.GenesisDoc, error) {
	genDocJSON, err := ioutil.ReadFile(genDocFile)
	if err != nil {
		return nil, fmt.Errorf("Couldn't read GenesisDoc file: %v", err)
	}
	genDoc, err := types.GenesisDocFromJSON(genDocJSON)
	if err != nil {
		return nil, fmt.Errorf("Error reading GenesisDoc: %v", err)
	}
	return genDoc, nil
}

// MakeGenesisState creates state from types.GenesisDoc.
func MakeGenesisState(db dbm.DB, genDoc *types.GenesisDoc) (*State, error) {
	err := genDoc.ValidateAndComplete()
	if err != nil {
		return nil, fmt.Errorf("Error in genesis file: %v", err)
	}

	// Make validators slice
	validators := make([]*types.Validator, len(genDoc.Validators))
	for i, val := range genDoc.Validators {
		pubKey := val.PubKey
		address := pubKey.Address()

		// Make validator
		validators[i] = &types.Validator{
			Address:     address,
			PubKey:      pubKey,
			VotingPower: val.Power,
		}
	}

	return &State{
		db: db,

		ChainID: genDoc.ChainID,

		LastBlockHeight: 0,
		LastBlockID:     types.BlockID{},
		LastBlockTime:   genDoc.GenesisTime,

		Validators:                  types.NewValidatorSet(validators),
		LastValidators:              types.NewValidatorSet(nil),
		LastHeightValidatorsChanged: 1,

		ConsensusParams:                  *genDoc.ConsensusParams,
		LastConsensusParams:              types.ConsensusParams{},
		LastHeightConsensusParamsChanged: 1,

		AppHash: genDoc.AppHash,
	}, nil
}
