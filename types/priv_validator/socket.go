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

// NewPrivValidatorSocketClient returns an instance of
// PrivValidatorSocketClient.
func NewPrivValidatorSocketClient(logger log.Logger, socketAddr string) *PrivValidatorSocketClient {
	pvsc := &PrivValidatorSocketClient{
		SocketAddress: socketAddr,
	}
	pvsc.BaseService = *cmn.NewBaseService(logger, "privValidatorSocketClient", pvsc)
	return pvsc
}

// OnStart implements cmn.Service.
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

// OnStop implements cmn.Service.
func (pvsc *PrivValidatorSocketClient) OnStop() {
	pvsc.BaseService.OnStop()

	if pvsc.conn != nil {
		pvsc.conn.Close()
	}
}

// Address is an alias for PubKey().Address().
func (pvsc *PrivValidatorSocketClient) Address() data.Bytes {
	pubKey := pvsc.PubKey()
	return pubKey.Address()
}

// PubKey implements PrivValidator.
func (pvsc *PrivValidatorSocketClient) PubKey() crypto.PubKey {
	res, err := readWrite(pvsc.conn, PubKeyMsg{})
	if err != nil {
		panic(err)
	}
	return res.(PubKeyMsg).PubKey
}

// SignVote implements PrivValidator.
func (pvsc *PrivValidatorSocketClient) SignVote(chainID string, vote *types.Vote) error {
	res, err := readWrite(pvsc.conn, SignVoteMsg{Vote: vote})
	if err != nil {
		return err
	}
	*vote = *res.(SignVoteMsg).Vote
	return nil
}

// SignProposal implements PrivValidator.
func (pvsc *PrivValidatorSocketClient) SignProposal(chainID string, proposal *types.Proposal) error {
	res, err := readWrite(pvsc.conn, SignProposalMsg{Proposal: proposal})
	if err != nil {
		return err
	}
	*proposal = *res.(SignProposalMsg).Proposal
	return nil
}

// SignHeartbeat implements PrivValidator.
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

	proto, addr string
	listener    net.Listener

	privVal PrivValidator
	chainID string
}

// NewPrivValidatorSocketServer returns an instance of
// PrivValidatorSocketServer.
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

// OnStart implements cmn.Service.
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

// OnStop implements cmn.Service.
func (pvss *PrivValidatorSocketServer) OnStop() {
	pvss.BaseService.OnStop()

	if pvss.listener == nil {
		return
	}

	if err := pvss.listener.Close(); err != nil {
		pvss.Logger.Error("Error closing listener", "err", err)
	}
}

func (pvss *PrivValidatorSocketServer) acceptConnectionsRoutine() {
	for {
		// Accept a connection
		pvss.Logger.Info("Waiting for new connection...")

		conn, err := pvss.listener.Accept()
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
			b := wire.ReadByteSlice(conn, 0, &n, &err) //XXX: no max
			req, err := decodeMsg(b)
			if err != nil {
				panic(err)
			}
			var res PrivValidatorSocketMsg
			switch r := req.(type) {
			case PubKeyMsg:
				res = PubKeyMsg{pvss.privVal.PubKey()}
			case SignVoteMsg:
				pvss.privVal.SignVote(pvss.chainID, r.Vote)
				res = SignVoteMsg{r.Vote}
			case SignProposalMsg:
				pvss.privVal.SignProposal(pvss.chainID, r.Proposal)
				res = SignProposalMsg{r.Proposal}
			case SignHeartbeatMsg:
				pvss.privVal.SignHeartbeat(pvss.chainID, r.Heartbeat)
				res = SignHeartbeatMsg{r.Heartbeat}
			default:
				panic(fmt.Sprintf("unknown msg: %v", r))
			}

			b = wire.BinaryBytes(res)
			_, err = conn.Write(b)
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

// PrivValidatorSocketMsg is a message sent between PrivValidatorSocket client
// and server.
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

// PubKeyMsg is a PrivValidatorSocket message containing the public key.
type PubKeyMsg struct {
	PubKey crypto.PubKey
}

// SignVoteMsg is a PrivValidatorSocket message containing a vote.
type SignVoteMsg struct {
	Vote *types.Vote
}

// SignProposalMsg is a PrivValidatorSocket message containing a Proposal.
type SignProposalMsg struct {
	Proposal *types.Proposal
}

// SignHeartbeatMsg is a PrivValidatorSocket message containing a Heartbeat.
type SignHeartbeatMsg struct {
	Heartbeat *types.Heartbeat
}
