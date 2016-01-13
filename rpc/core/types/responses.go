package core_types

import (
	"github.com/tendermint/go-crypto"
	"github.com/tendermint/go-events"
	"github.com/tendermint/go-p2p"
	"github.com/tendermint/go-rpc/types"
	"github.com/tendermint/go-wire"
	"github.com/tendermint/tendermint/types"
)

type ResultBlockchainInfo struct {
	LastHeight int                `json:"last_height"`
	BlockMetas []*types.BlockMeta `json:"block_metas"`
}

type ResultGenesis struct {
	Genesis *types.GenesisDoc `json:"genesis"`
}

type ResultGetBlock struct {
	BlockMeta *types.BlockMeta `json:"block_meta"`
	Block     *types.Block     `json:"block"`
}

type ResultStatus struct {
	NodeInfo          *p2p.NodeInfo `json:"node_info"`
	PubKey            crypto.PubKey `json:"pub_key"`
	LatestBlockHash   []byte        `json:"latest_block_hash"`
	LatestAppHash     []byte        `json:"latest_app_hash"`
	LatestBlockHeight int           `json:"latest_block_height"`
	LatestBlockTime   int64         `json:"latest_block_time"` // nano
}

type ResultNetInfo struct {
	Listening bool     `json:"listening"`
	Listeners []string `json:"listeners"`
	Peers     []Peer   `json:"peers"`
}

type Peer struct {
	p2p.NodeInfo     `json:"node_info"`
	IsOutbound       bool                 `json:"is_outbound"`
	ConnectionStatus p2p.ConnectionStatus `json:"connection_status"`
}

type ResultListValidators struct {
	BlockHeight int                `json:"block_height"`
	Validators  []*types.Validator `json:"validators"`
}

type ResultDumpConsensusState struct {
	RoundState      string   `json:"round_state"`
	PeerRoundStates []string `json:"peer_round_states"`
}

type ResultBroadcastTx struct {
}

type ResultListUnconfirmedTxs struct {
	N   int        `json:"n_txs"`
	Txs []types.Tx `json:"txs"`
}

type ResultSubscribe struct {
}

type ResultUnsubscribe struct {
}

// TODO: something about this
type ResultEvent struct {
	Event string           `json:"event"`
	Data  events.EventData `json:"data"`
}

//----------------------------------------
// response & result types

const (
	ResultTypeGenesis            = byte(0x01)
	ResultTypeBlockchainInfo     = byte(0x02)
	ResultTypeGetBlock           = byte(0x03)
	ResultTypeStatus             = byte(0x04)
	ResultTypeNetInfo            = byte(0x05)
	ResultTypeListValidators     = byte(0x06)
	ResultTypeDumpConsensusState = byte(0x07)
	ResultTypeBroadcastTx        = byte(0x08)
	ResultTypeListUnconfirmedTxs = byte(0x09)
	ResultTypeSubscribe          = byte(0x0A)
	ResultTypeUnsubscribe        = byte(0x0B)
	ResultTypeEvent              = byte(0x0C)
)

// for wire.readReflect
var _ = wire.RegisterInterface(
	struct{ rpctypes.Result }{},
	wire.ConcreteType{&ResultGenesis{}, ResultTypeGenesis},
	wire.ConcreteType{&ResultBlockchainInfo{}, ResultTypeBlockchainInfo},
	wire.ConcreteType{&ResultGetBlock{}, ResultTypeGetBlock},
	wire.ConcreteType{&ResultStatus{}, ResultTypeStatus},
	wire.ConcreteType{&ResultNetInfo{}, ResultTypeNetInfo},
	wire.ConcreteType{&ResultListValidators{}, ResultTypeListValidators},
	wire.ConcreteType{&ResultDumpConsensusState{}, ResultTypeDumpConsensusState},
	wire.ConcreteType{&ResultBroadcastTx{}, ResultTypeBroadcastTx},
	wire.ConcreteType{&ResultListUnconfirmedTxs{}, ResultTypeListUnconfirmedTxs},
	wire.ConcreteType{&ResultSubscribe{}, ResultTypeSubscribe},
	wire.ConcreteType{&ResultUnsubscribe{}, ResultTypeUnsubscribe},
	wire.ConcreteType{&ResultEvent{}, ResultTypeEvent},
)
