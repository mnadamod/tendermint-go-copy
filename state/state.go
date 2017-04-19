package state

import (
	"bytes"
	"io/ioutil"
	"sync"
	"time"

	abci "github.com/tendermint/abci/types"
	. "github.com/tendermint/go-common"
	cfg "github.com/tendermint/go-config"
	dbm "github.com/tendermint/go-db"
	"github.com/tendermint/go-wire"
	"github.com/tendermint/tendermint/state/txindex"
	"github.com/tendermint/tendermint/state/txindex/null"
	"github.com/tendermint/tendermint/types"
)

var (
	stateKey         = []byte("stateKey")
	abciResponsesKey = []byte("abciResponsesKey")
)

//-----------------------------------------------------------------------------

// NOTE: not goroutine-safe.
type State struct {
	// mtx for writing to db
	mtx sync.Mutex
	db  dbm.DB

	// should not change
	GenesisDoc *types.GenesisDoc
	ChainID    string

	// updated at end of SetBlockAndValidators
	LastBlockHeight int // Genesis state has this set to 0.  So, Block(H=0) does not exist.
	LastBlockID     types.BlockID
	LastBlockTime   time.Time
	Validators      *types.ValidatorSet
	LastValidators  *types.ValidatorSet // block.LastCommit validated against this

	// AppHash is updated after Commit
	AppHash []byte

	TxIndexer txindex.TxIndexer `json:"-"` // Transaction indexer.

	// Intermediate results from processing
	// Persisted separately from the state
	abciResponses *ABCIResponses
}

func LoadState(db dbm.DB) *State {
	return loadState(db, stateKey)
}

func loadState(db dbm.DB, key []byte) *State {
	s := &State{db: db, TxIndexer: &null.TxIndex{}}
	buf := db.Get(key)
	if len(buf) == 0 {
		return nil
	} else {
		r, n, err := bytes.NewReader(buf), new(int), new(error)
		wire.ReadBinaryPtr(&s, r, 0, n, err)
		if *err != nil {
			// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
			Exit(Fmt("Data has been corrupted or its spec has changed: %v\n", *err))
		}
		// TODO: ensure that buf is completely read.
	}

	s.LoadABCIResponses()
	return s
}

func (s *State) Copy() *State {
	return &State{
		db:              s.db,
		GenesisDoc:      s.GenesisDoc,
		ChainID:         s.ChainID,
		LastBlockHeight: s.LastBlockHeight,
		LastBlockID:     s.LastBlockID,
		LastBlockTime:   s.LastBlockTime,
		Validators:      s.Validators.Copy(),
		LastValidators:  s.LastValidators.Copy(),
		AppHash:         s.AppHash,
		abciResponses:   s.abciResponses, // pointer here, not value
		TxIndexer:       s.TxIndexer,     // pointer here, not value
	}
}

func (s *State) Save() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.db.SetSync(stateKey, s.Bytes())
}

// Sets the ABCIResponses in the state and writes them to disk
func (s *State) SaveABCIResponses(abciResponses *ABCIResponses) {
	s.abciResponses = abciResponses

	// save the validators to the db
	s.db.SetSync(abciResponsesKey, s.abciResponses.Bytes())

	// save the tx results using the TxIndexer
	batch := txindexer.NewBatch()
	for i, r := range s.abciResponses.TxResults {
		tx := s.abciResponses.Txs[i]
		batch.Index(tx.Hash(), *r)
	}
	s.TxIndexer.Batch(batch)
}

func (s *State) LoadABCIResponses() {
	s.abciResponses = new(ABCIResponses)

	buf := s.db.Get(abciResponsesKey)
	if len(buf) != 0 {
		r, n, err := bytes.NewReader(buf), new(int), new(error)
		wire.ReadBinaryPtr(&s.abciResponses.Validators, r, 0, n, err)
		if *err != nil {
			// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
			Exit(Fmt("Data has been corrupted or its spec has changed: %v\n", *err))
		}
		// TODO: ensure that buf is completely read.
	}
}

func (s *State) Equals(s2 *State) bool {
	return bytes.Equal(s.Bytes(), s2.Bytes())
}

func (s *State) Bytes() []byte {
	buf, n, err := new(bytes.Buffer), new(int), new(error)
	wire.WriteBinary(s, buf, n, err)
	if *err != nil {
		PanicCrisis(*err)
	}
	return buf.Bytes()
}

// Mutate state variables to match block and validators
// after running EndBlock
func (s *State) SetBlockAndValidators(header *types.Header, blockPartsHeader types.PartSetHeader) {

	// copy the valset
	prevValSet := s.Validators.Copy()
	nextValSet := prevValSet.Copy()

	// update the validator set with the latest abciResponses
	err := updateValidators(nextValSet, s.abciResponses.Validators)
	if err != nil {
		log.Warn("Error changing validator set", "error", err)
		// TODO: err or carry on?
	}
	// Update validator accums and set state variables
	nextValSet.IncrementAccum(1)

	s.setBlockAndValidators(header.Height,
		types.BlockID{header.Hash(), blockPartsHeader}, header.Time,
		prevValSet, nextValSet)
}

func (s *State) setBlockAndValidators(
	height int, blockID types.BlockID, blockTime time.Time,
	prevValSet, nextValSet *types.ValidatorSet) {

	s.LastBlockHeight = height
	s.LastBlockID = blockID
	s.LastBlockTime = blockTime
	s.Validators = nextValSet
	s.LastValidators = prevValSet
}

func (s *State) GetValidators() (*types.ValidatorSet, *types.ValidatorSet) {
	return s.LastValidators, s.Validators
}

// Load the most recent state from "state" db,
// or create a new one (and save) from genesis.
func GetState(config cfg.Config, stateDB dbm.DB) *State {
	state := LoadState(stateDB)
	if state == nil {
		state = MakeGenesisStateFromFile(stateDB, config.GetString("genesis_file"))
		state.Save()
	}

	return state
}

//--------------------------------------------------
// ABCIResponses holds intermediate state during block processing

type ABCIResponses struct {
	Validators []*abci.Validator // changes to the validator set

	Txs       types.Txs         // for reference later
	TxResults []*types.TxResult // results of the txs, populated in the proxyCb
}

func NewABCIResponses(block *types.Block) *ABCIResponses {
	return &ABCIResponses{
		Txs:       block.Data.Txs,
		TxResults: make([]*types.TxResult, block.NumTxs),
	}
}

// Serialize the list of validators
func (a *ABCIResponses) Bytes() []byte {
	buf, n, err := new(bytes.Buffer), new(int), new(error)
	wire.WriteBinary(a.Validators, buf, n, err)
	if *err != nil {
		PanicCrisis(*err)
	}
	return buf.Bytes()
}

//-----------------------------------------------------------------------------
// Genesis

// MakeGenesisStateFromFile reads and unmarshals state from the given file.
//
// Used during replay and in tests.
func MakeGenesisStateFromFile(db dbm.DB, genDocFile string) *State {
	genDocJSON, err := ioutil.ReadFile(genDocFile)
	if err != nil {
		Exit(Fmt("Couldn't read GenesisDoc file: %v", err))
	}
	genDoc, err := types.GenesisDocFromJSON(genDocJSON)
	if err != nil {
		Exit(Fmt("Error reading GenesisDoc: %v", err))
	}
	return MakeGenesisState(db, genDoc)
}

// MakeGenesisState creates state from types.GenesisDoc.
//
// Used in tests.
func MakeGenesisState(db dbm.DB, genDoc *types.GenesisDoc) *State {
	if len(genDoc.Validators) == 0 {
		Exit(Fmt("The genesis file has no validators"))
	}

	if genDoc.GenesisTime.IsZero() {
		genDoc.GenesisTime = time.Now()
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
			VotingPower: val.Amount,
		}
	}

	return &State{
		db:              db,
		GenesisDoc:      genDoc,
		ChainID:         genDoc.ChainID,
		LastBlockHeight: 0,
		LastBlockID:     types.BlockID{},
		LastBlockTime:   genDoc.GenesisTime,
		Validators:      types.NewValidatorSet(validators),
		LastValidators:  types.NewValidatorSet(nil),
		AppHash:         genDoc.AppHash,
		TxIndexer:       &null.TxIndex{}, // we do not need indexer during replay and in tests
	}
}
