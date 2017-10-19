package dummy

import (
	"bytes"
	"encoding/hex"
	"path"
	"strconv"
	"strings"

	"github.com/tendermint/abci/types"
	"github.com/tendermint/iavl"
	cmn "github.com/tendermint/tmlibs/common"
	dbm "github.com/tendermint/tmlibs/db"
	"github.com/tendermint/tmlibs/log"
)

const (
	ValidatorSetChangePrefix string = "val:"
)

//-----------------------------------------

type PersistentDummyApplication struct {
	app *DummyApplication

	// latest received
	// TODO: move to merkle tree?
	blockHeader *types.Header
	height      uint64

	// validator set
	changes []*types.Validator

	logger log.Logger
}

func NewPersistentDummyApplication(dbDir string) *PersistentDummyApplication {
	name := "dummy"
	dbPath := path.Join(dbDir, name+".db")
	empty, _ := cmn.IsDirEmpty(dbPath)

	db, err := dbm.NewGoLevelDB(name, dbDir)
	if err != nil {
		panic(err)
	}

	stateTree := iavl.NewVersionedTree(500, db)
	if !empty {
		stateTree.Load()
	}

	return &PersistentDummyApplication{
		app:    &DummyApplication{state: stateTree},
		logger: log.NewNopLogger(),
	}
}

func (app *PersistentDummyApplication) SetLogger(l log.Logger) {
	app.logger = l
}

func (app *PersistentDummyApplication) Info(req types.RequestInfo) (resInfo types.ResponseInfo) {
	resInfo = app.app.Info(req)
	resInfo.LastBlockHeight = app.height
	resInfo.LastBlockAppHash = app.app.state.Hash()
	return resInfo
}

func (app *PersistentDummyApplication) SetOption(key string, value string) (log string) {
	return app.app.SetOption(key, value)
}

// tx is either "val:pubkey/power" or "key=value" or just arbitrary bytes
func (app *PersistentDummyApplication) DeliverTx(tx []byte) types.Result {
	// if it starts with "val:", update the validator set
	// format is "val:pubkey/power"
	if isValidatorTx(tx) {
		// update validators in the merkle tree
		// and in app.changes
		return app.execValidatorTx(tx)
	}

	// otherwise, update the key-value store
	return app.app.DeliverTx(tx)
}

func (app *PersistentDummyApplication) CheckTx(tx []byte) types.Result {
	return app.app.CheckTx(tx)
}

func (app *PersistentDummyApplication) Commit() types.Result {
	h := app.blockHeader.Height

	// Save a new version
	var appHash []byte
	var err error

	if app.app.state.Size() > 0 {
		appHash, err = app.app.state.SaveVersion(h)
		if err != nil {
			// if this wasn't a dummy app, we'd do something smarter
			panic(err)
		}
		app.logger.Info("Saved state", "root", appHash)
	}

	app.height = h
	app.logger.Info("Commit block", "height", h, "root", appHash)
	return types.NewResultOK(appHash, "")
}

func (app *PersistentDummyApplication) Query(reqQuery types.RequestQuery) types.ResponseQuery {
	return app.app.Query(reqQuery)
}

// Save the validators in the merkle tree
func (app *PersistentDummyApplication) InitChain(params types.RequestInitChain) {
	for _, v := range params.Validators {
		r := app.updateValidator(v)
		if r.IsErr() {
			app.logger.Error("Error updating validators", "r", r)
		}
	}
}

// Track the block hash and header information
func (app *PersistentDummyApplication) BeginBlock(params types.RequestBeginBlock) {
	// update latest block info
	app.blockHeader = params.Header

	// reset valset changes
	app.changes = make([]*types.Validator, 0)
}

// Update the validator set
func (app *PersistentDummyApplication) EndBlock(height uint64) (resEndBlock types.ResponseEndBlock) {
	return types.ResponseEndBlock{Diffs: app.changes}
}

//-----------------------------------------
// persist the last block info

var lastBlockKey = []byte("lastblock")

type LastBlockInfo struct {
	Height  uint64
	AppHash []byte
}

//---------------------------------------------
// update validators

func (app *PersistentDummyApplication) Validators() (validators []*types.Validator) {
	app.app.state.Iterate(func(key, value []byte) bool {
		if isValidatorTx(key) {
			validator := new(types.Validator)
			err := types.ReadMessage(bytes.NewBuffer(value), validator)
			if err != nil {
				panic(err)
			}
			validators = append(validators, validator)
		}
		return false
	})
	return
}

func MakeValSetChangeTx(pubkey []byte, power uint64) []byte {
	return []byte(cmn.Fmt("val:%X/%d", pubkey, power))
}

func isValidatorTx(tx []byte) bool {
	return strings.HasPrefix(string(tx), ValidatorSetChangePrefix)
}

// format is "val:pubkey1/power1,addr2/power2,addr3/power3"tx
func (app *PersistentDummyApplication) execValidatorTx(tx []byte) types.Result {
	tx = tx[len(ValidatorSetChangePrefix):]
	pubKeyAndPower := strings.Split(string(tx), "/")
	if len(pubKeyAndPower) != 2 {
		return types.ErrEncodingError.SetLog(cmn.Fmt("Expected 'pubkey/power'. Got %v", pubKeyAndPower))
	}
	pubkeyS, powerS := pubKeyAndPower[0], pubKeyAndPower[1]
	pubkey, err := hex.DecodeString(pubkeyS)
	if err != nil {
		return types.ErrEncodingError.SetLog(cmn.Fmt("Pubkey (%s) is invalid hex", pubkeyS))
	}
	power, err := strconv.Atoi(powerS)
	if err != nil {
		return types.ErrEncodingError.SetLog(cmn.Fmt("Power (%s) is not an int", powerS))
	}

	// update
	return app.updateValidator(&types.Validator{pubkey, uint64(power)})
}

// add, update, or remove a validator
func (app *PersistentDummyApplication) updateValidator(v *types.Validator) types.Result {
	key := []byte("val:" + string(v.PubKey))
	if v.Power == 0 {
		// remove validator
		if !app.app.state.Has(key) {
			return types.ErrUnauthorized.SetLog(cmn.Fmt("Cannot remove non-existent validator %X", key))
		}
		app.app.state.Remove(key)
	} else {
		// add or update validator
		value := bytes.NewBuffer(make([]byte, 0))
		if err := types.WriteMessage(v, value); err != nil {
			return types.ErrInternalError.SetLog(cmn.Fmt("Error encoding validator: %v", err))
		}
		app.app.state.Set(key, value.Bytes())
	}

	// we only update the changes array if we successfully updated the tree
	app.changes = append(app.changes, v)

	return types.OK
}
