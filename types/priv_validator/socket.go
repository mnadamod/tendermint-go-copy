package types

import (
	"bytes"
	"fmt"
	"net"
	"time"

	crypto "github.com/tendermint/go-crypto"
	wire "github.com/tendermint/go-wire"
	"github.com/tendermint/go-wire/data"
	cmn "github.com/tendermint/tmlibs/common"
	"github.com/tendermint/tmlibs/log"

	"github.com/tendermint/tendermint/types"
)

//-----------------------------------------------------------------

var _ types.PrivValidator = (*PrivValidatorSocketClient)(nil)

// PrivValidatorSocketClient implements PrivValidator.
// It uses a socket to request signatures.
type PrivValidatorSocketClient struct {
	cmn.BaseService

	conn net.Conn

	ID            types.ValidatorID
	SocketAddress string
}

const (
	dialRetryIntervalSeconds = 1
)

// NewPrivValidatorSocket returns an instance of PrivValidatorSocket.
func NewPrivValidatorSocketClient(logger log.Logger, socketAddr string) *PrivValidatorSocketClient {
	pvsc := &PrivValidatorSocketClient{
		SocketAddress: socketAddr,
	}
	pvsc.BaseService = *cmn.NewBaseService(logger, "privValidatorSocketClient", pvsc)
	return pvsc
}

func (pvsc *PrivValidatorSocketClient) OnStart() error {
	if err := pvsc.BaseService.OnStart(); err != nil {
		return err
	}

	var err error
	var conn net.Conn
RETRY_LOOP:
	for {
		conn, err = cmn.Connect(pvsc.SocketAddress)
		if err != nil {
			pvsc.Logger.Error(fmt.Sprintf("PrivValidatorSocket failed to connect to %v.  Retrying...", pvsc.SocketAddress))
			time.Sleep(time.Second * dialRetryIntervalSeconds)
			continue RETRY_LOOP
		}
		pvsc.conn = conn
		return nil
	}
}

func (pvsc *PrivValidatorSocketClient) OnStop() {
	pvsc.BaseService.OnStop()

	if pvsc.conn != nil {
		pvsc.conn.Close()
	}
}

func (pvsc *PrivValidatorSocketClient) Address() data.Bytes {
	pubKey := pvsc.PubKey()
	return pubKey.Address()
}

func (pvsc *PrivValidatorSocketClient) PubKey() crypto.PubKey {
	res, err := readWrite(pvsc.conn, PubKeyMsg{})
	if err != nil {
		panic(err)
	}
	return res.(PubKeyMsg).PubKey
}

func (pvsc *PrivValidatorSocketClient) SignVote(chainID string, vote *types.Vote) error {
	res, err := readWrite(pvsc.conn, SignVoteMsg{Vote: vote})
	if err != nil {
		return err
	}
	*vote = *res.(SignVoteMsg).Vote
	return nil
}

func (pvsc *PrivValidatorSocketClient) SignProposal(chainID string, proposal *types.Proposal) error {
	res, err := readWrite(pvsc.conn, SignProposalMsg{Proposal: proposal})
	if err != nil {
		return err
	}
	*proposal = *res.(SignProposalMsg).Proposal
	return nil
}

func (pvsc *PrivValidatorSocketClient) SignHeartbeat(chainID string, heartbeat *types.Heartbeat) error {
	res, err := readWrite(pvsc.conn, SignHeartbeatMsg{Heartbeat: heartbeat})
	if err != nil {
		return err
	}
	*heartbeat = *res.(SignHeartbeatMsg).Heartbeat
	return nil
}

//---------------------------------------------------------

// PrivValidatorSocketServer implements PrivValidator.
// It responds to requests over a socket
type PrivValidatorSocketServer struct {
	cmn.BaseService

	conn        net.Conn
	proto, addr string
	listener    net.Listener

	privVal PrivValidator
	chainID string
}

func NewPrivValidatorSocketServer(logger log.Logger, socketAddr, chainID string, privVal PrivValidator) *PrivValidatorSocketServer {
	proto, addr := cmn.ProtocolAndAddress(socketAddr)
	pvss := &PrivValidatorSocketServer{
		proto:   proto,
		addr:    addr,
		privVal: privVal,
		chainID: chainID,
	}
	pvss.BaseService = *cmn.NewBaseService(logger, "privValidatorSocketServer", pvss)
	return pvss
}

func (pvss *PrivValidatorSocketServer) OnStart() error {
	if err := pvss.BaseService.OnStart(); err != nil {
		return err
	}
	ln, err := net.Listen(pvss.proto, pvss.addr)
	if err != nil {
		return err
	}
	pvss.listener = ln
	go pvss.acceptConnectionsRoutine()
	return nil
}

func (pvss *PrivValidatorSocketServer) OnStop() {
	pvss.BaseService.OnStop()
	if err := pvss.listener.Close(); err != nil {
		pvss.Logger.Error("Error closing listener", "err", err)
	}

	if err := pvss.conn.Close(); err != nil {
		pvss.Logger.Error("Error closing connection", "conn", pvss.conn, "err", err)
	}
}

func (pvss *PrivValidatorSocketServer) acceptConnectionsRoutine() {
	for {
		// Accept a connection
		pvss.Logger.Info("Waiting for new connection...")
		var err error
		pvss.conn, err = pvss.listener.Accept()
		if err != nil {
			if !pvss.IsRunning() {
				return // Ignore error from listener closing.
			}
			pvss.Logger.Error("Failed to accept connection: " + err.Error())
			continue
		}

		pvss.Logger.Info("Accepted a new connection")

		// read/write
		for {
			if !pvss.IsRunning() {
				return // Ignore error from listener closing.
			}

			var n int
			var err error
			b := wire.ReadByteSlice(pvss.conn, 0, &n, &err) //XXX: no max
			req_, err := decodeMsg(b)
			if err != nil {
				panic(err)
			}
			var res PrivValidatorSocketMsg
			switch req := req_.(type) {
			case PubKeyMsg:
				res = PubKeyMsg{pvss.privVal.PubKey()}
			case SignVoteMsg:
				pvss.privVal.SignVote(pvss.chainID, req.Vote)
				res = SignVoteMsg{req.Vote}
			case SignProposalMsg:
				pvss.privVal.SignProposal(pvss.chainID, req.Proposal)
				res = SignProposalMsg{req.Proposal}
			case SignHeartbeatMsg:
				pvss.privVal.SignHeartbeat(pvss.chainID, req.Heartbeat)
				res = SignHeartbeatMsg{req.Heartbeat}
			default:
				panic(fmt.Sprintf("unknown msg: %v", req_))
			}

			b = wire.BinaryBytes(res)
			_, err = pvss.conn.Write(b)
			if err != nil {
				panic(err)
			}
		}
	}
}

//---------------------------------------------------------

const (
	msgTypePubKey        = byte(0x01)
	msgTypeSignVote      = byte(0x10)
	msgTypeSignProposal  = byte(0x11)
	msgTypeSignHeartbeat = byte(0x12)
)

type PrivValidatorSocketMsg interface{}

var _ = wire.RegisterInterface(
	struct{ PrivValidatorSocketMsg }{},
	wire.ConcreteType{&PubKeyMsg{}, msgTypePubKey},
	wire.ConcreteType{&SignVoteMsg{}, msgTypeSignVote},
	wire.ConcreteType{&SignProposalMsg{}, msgTypeSignProposal},
	wire.ConcreteType{&SignHeartbeatMsg{}, msgTypeSignHeartbeat},
)

func readWrite(conn net.Conn, req PrivValidatorSocketMsg) (res PrivValidatorSocketMsg, err error) {
	b := wire.BinaryBytes(req)
	_, err = conn.Write(b)
	if err != nil {
		return nil, err
	}

	var n int
	b = wire.ReadByteSlice(conn, 0, &n, &err) //XXX: no max
	return decodeMsg(b)
}

func decodeMsg(bz []byte) (msg PrivValidatorSocketMsg, err error) {
	n := new(int)
	r := bytes.NewReader(bz)
	msgI := wire.ReadBinary(struct{ PrivValidatorSocketMsg }{}, r, 0, n, &err)
	msg = msgI.(struct{ PrivValidatorSocketMsg }).PrivValidatorSocketMsg
	return msg, err
}

type PubKeyMsg struct {
	PubKey crypto.PubKey
}

type SignVoteMsg struct {
	Vote *types.Vote
}

type SignProposalMsg struct {
	Proposal *types.Proposal
}

type SignHeartbeatMsg struct {
	Heartbeat *types.Heartbeat
}
