package state

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/abci/example/dummy"
	abci "github.com/tendermint/abci/types"
	crypto "github.com/tendermint/go-crypto"
	"github.com/tendermint/tendermint/proxy"
	"github.com/tendermint/tendermint/types"
	dbm "github.com/tendermint/tmlibs/db"
	"github.com/tendermint/tmlibs/log"
)

var (
	privKey      = crypto.GenPrivKeyEd25519FromSecret([]byte("execution_test"))
	chainID      = "execution_chain"
	testPartSize = 65536
	nTxsPerBlock = 10
)

func TestValidateBlock(t *testing.T) {
	state := state()
	state.SetLogger(log.TestingLogger())

	// proper block must pass
	block := makeBlock(state, 1)
	err := state.ValidateBlock(block)
	require.NoError(t, err)

	// wrong chain fails
	block = makeBlock(state, 1)
	block.ChainID = "not-the-real-one"
	err = state.ValidateBlock(block)
	require.Error(t, err)

	// wrong height fails
	block = makeBlock(state, 1)
	block.Height += 10
	err = state.ValidateBlock(block)
	require.Error(t, err)

	// wrong total tx fails
	block = makeBlock(state, 1)
	block.TotalTxs += 10
	err = state.ValidateBlock(block)
	require.Error(t, err)

	// wrong blockid fails
	block = makeBlock(state, 1)
	block.LastBlockID.PartsHeader.Total += 10
	err = state.ValidateBlock(block)
	require.Error(t, err)

	// wrong app hash fails
	block = makeBlock(state, 1)
	block.AppHash = []byte("wrong app hash")
	err = state.ValidateBlock(block)
	require.Error(t, err)

	// wrong consensus hash fails
	block = makeBlock(state, 1)
	block.ConsensusHash = []byte("wrong consensus hash")
	err = state.ValidateBlock(block)
	require.Error(t, err)

	// wrong results hash fails
	block = makeBlock(state, 1)
	block.LastResultsHash = []byte("wrong results hash")
	err = state.ValidateBlock(block)
	require.Error(t, err)

	// wrong validators hash fails
	block = makeBlock(state, 1)
	block.ValidatorsHash = []byte("wrong validators hash")
	err = state.ValidateBlock(block)
	require.Error(t, err)
}

func TestApplyBlock(t *testing.T) {
	cc := proxy.NewLocalClientCreator(dummy.NewDummyApplication())
	proxyApp := proxy.NewAppConns(cc, nil)
	err := proxyApp.Start()
	require.Nil(t, err)
	defer proxyApp.Stop()

	state := state()
	state.SetLogger(log.TestingLogger())

	block := makeBlock(state, 1)

	err = state.ApplyBlock(types.NopEventBus{}, proxyApp.Consensus(),
		block, block.MakePartSet(testPartSize).Header(),
		types.MockMempool{}, types.MockEvidencePool{})

	require.Nil(t, err)

	// TODO check state and mempool
}

// TestBeginBlockAbsentValidators ensures we send absent validators list.
func TestBeginBlockAbsentValidators(t *testing.T) {
	app := &testApp{}
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc, nil)
	err := proxyApp.Start()
	require.Nil(t, err)
	defer proxyApp.Stop()

	state := state()
	state.SetLogger(log.TestingLogger())

	// there were 2 validators
	val1PrivKey := crypto.GenPrivKeyEd25519()
	val2PrivKey := crypto.GenPrivKeyEd25519()
	lastValidators := types.NewValidatorSet([]*types.Validator{
		types.NewValidator(val1PrivKey.PubKey(), 10),
		types.NewValidator(val2PrivKey.PubKey(), 5),
	})

	prevHash := state.LastBlockID.Hash
	prevParts := types.PartSetHeader{}
	prevBlockID := types.BlockID{prevHash, prevParts}

	now := time.Now().UTC()
	testCases := []struct {
		desc                     string
		lastCommitPrecommits     []*types.Vote
		expectedAbsentValidators []int32
	}{
		{"none absent", []*types.Vote{{ValidatorIndex: 0, Timestamp: now}, {ValidatorIndex: 1, Timestamp: now}}, []int32{}},
		{"one absent", []*types.Vote{{ValidatorIndex: 0, Timestamp: now}, nil}, []int32{1}},
		{"multiple absent", []*types.Vote{nil, nil}, []int32{0, 1}},
	}

	for _, tc := range testCases {
		lastCommit := &types.Commit{BlockID: prevBlockID, Precommits: tc.lastCommitPrecommits}

		block, _ := state.MakeBlock(2, makeTxs(2), lastCommit)
		_, err = ExecCommitBlock(proxyApp.Consensus(), block, log.TestingLogger(), lastValidators)
		require.Nil(t, err, tc.desc)

		// -> app must receive an index of the absent validator
		assert.Equal(t, tc.expectedAbsentValidators, app.AbsentValidators, tc.desc)
	}
}

//----------------------------------------------------------------------------

// make some bogus txs
func makeTxs(height int64) (txs []types.Tx) {
	for i := 0; i < nTxsPerBlock; i++ {
		txs = append(txs, types.Tx([]byte{byte(height), byte(i)}))
	}
	return txs
}

func state() *State {
	s, _ := MakeGenesisState(dbm.NewMemDB(), &types.GenesisDoc{
		ChainID: chainID,
		Validators: []types.GenesisValidator{
			{privKey.PubKey(), 10000, "test"},
		},
		AppHash: nil,
	})
	return s
}

func makeBlock(state *State, height int64) *types.Block {
	block, _ := state.MakeBlock(height, makeTxs(state.LastBlockHeight), new(types.Commit))
	return block
}

//----------------------------------------------------------------------------

var _ abci.Application = (*testApp)(nil)

type testApp struct {
	abci.BaseApplication

	AbsentValidators []int32
}

func NewDummyApplication() *testApp {
	return &testApp{}
}

func (app *testApp) Info(req abci.RequestInfo) (resInfo abci.ResponseInfo) {
	return abci.ResponseInfo{}
}

func (app *testApp) BeginBlock(req abci.RequestBeginBlock) abci.ResponseBeginBlock {
	app.AbsentValidators = req.AbsentValidators
	return abci.ResponseBeginBlock{}
}

func (app *testApp) DeliverTx(tx []byte) abci.ResponseDeliverTx {
	return abci.ResponseDeliverTx{Tags: []*abci.KVPair{}}
}

func (app *testApp) CheckTx(tx []byte) abci.ResponseCheckTx {
	return abci.ResponseCheckTx{}
}

func (app *testApp) Commit() abci.ResponseCommit {
	return abci.ResponseCommit{}
}

func (app *testApp) Query(reqQuery abci.RequestQuery) (resQuery abci.ResponseQuery) {
	return
}
