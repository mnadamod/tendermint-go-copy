package consensus

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/tendermint/tendermint/config/tendermint_test"

	. "github.com/tendermint/go-common"
	cfg "github.com/tendermint/go-config"
	"github.com/tendermint/go-crypto"
	dbm "github.com/tendermint/go-db"
	"github.com/tendermint/go-events"
	"github.com/tendermint/go-logger"
	"github.com/tendermint/go-p2p"
	bc "github.com/tendermint/tendermint/blockchain"
	"github.com/tendermint/tendermint/types"
)

func init() {
	config = tendermint_test.ResetConfig("consensus_reactor_test")
}

func resetConfigTimeouts() {
	logger.SetLogLevel("notice")
	//config.Set("log_level", "notice")
	config.Set("timeout_propose", 2000)
	//	config.Set("timeout_propose_delta", 500)
	//	config.Set("timeout_prevote", 1000)
	//	config.Set("timeout_prevote_delta", 500)
	//	config.Set("timeout_precommit", 1000)
	//	config.Set("timeout_precommit_delta", 500)
	//	config.Set("timeout_commit", 1000)
}

func TestReactor(t *testing.T) {
	resetConfigTimeouts()
	N := 4
	css := randConsensusNet(N)
	reactors := make([]*ConsensusReactor, N)
	eventChans := make([]chan interface{}, N)
	for i := 0; i < N; i++ {
		blockStoreDB := dbm.NewDB(Fmt("blockstore%d", i), config.GetString("db_backend"), config.GetString("db_dir"))
		blockStore := bc.NewBlockStore(blockStoreDB)
		reactors[i] = NewConsensusReactor(css[i], blockStore, false)
		reactors[i].SetPrivValidator(css[i].privValidator)

		eventSwitch := events.NewEventSwitch()
		_, err := eventSwitch.Start()
		if err != nil {
			t.Fatalf("Failed to start switch: %v", err)
		}

		reactors[i].SetEventSwitch(eventSwitch)
		eventChans[i] = subscribeToEvent(eventSwitch, "tester", types.EventStringNewBlock(), 1)
	}
	p2p.MakeConnectedSwitches(N, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("CONSENSUS", reactors[i])
		return s
	}, p2p.Connect2Switches)

	// wait till everyone makes the first new block
	wg := new(sync.WaitGroup)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(j int) {
			<-eventChans[j]
			wg.Done()
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	tick := time.NewTicker(time.Second * 3)
	select {
	case <-done:
	case <-tick.C:
		t.Fatalf("Timed out waiting for all validators to commit first block")
	}
}

// 4 validators. 1 is byzantine. The other three are partitioned into A (1 val) and B (2 vals).
// byzantine validator sends conflicting proposals into A and B,
// and prevotes/precommits on both of them.
// B sees a commit, A doesn't.
// Byzantine validator refuses to prevote.
// Heal partition and ensure A sees the commit
func TestByzantine(t *testing.T) {
	resetConfigTimeouts()
	N := 4
	css := randConsensusNet(N)

	switches := make([]*p2p.Switch, N)
	for i := 0; i < N; i++ {
		switches[i] = p2p.NewSwitch(cfg.NewMapConfig(nil))
	}

	reactors := make([]p2p.Reactor, N)
	eventChans := make([]chan interface{}, N)
	for i := 0; i < N; i++ {
		blockStoreDB := dbm.NewDB(Fmt("blockstore%d", i), config.GetString("db_backend"), config.GetString("db_dir"))
		blockStore := bc.NewBlockStore(blockStoreDB)

		var privVal PrivValidator
		privVal = css[i].privValidator
		if i == 0 {
			privVal = NewByzantinePrivValidator(privVal.(*types.PrivValidator))
			// make byzantine
			css[i].decideProposal = func(j int) func(int, int) {
				return func(height, round int) {
					byzantineDecideProposalFunc(height, round, css[j], switches[j])
				}
			}(i)
			css[i].doPrevote = func(height, round int) {}
		}

		eventSwitch := events.NewEventSwitch()
		_, err := eventSwitch.Start()
		if err != nil {
			t.Fatalf("Failed to start switch: %v", err)
		}
		eventChans[i] = subscribeToEvent(eventSwitch, "tester", types.EventStringNewBlock(), 1)

		conR := NewConsensusReactor(css[i], blockStore, false)
		conR.SetPrivValidator(privVal)
		conR.SetEventSwitch(eventSwitch)

		var conRI p2p.Reactor
		conRI = conR
		if i == 0 {
			conRI = NewByzantineReactor(conR)
		}
		reactors[i] = conRI
	}

	p2p.MakeConnectedSwitches(N, func(i int, s *p2p.Switch) *p2p.Switch {
		// ignore new switch s, we already made ours
		switches[i].AddReactor("CONSENSUS", reactors[i])
		return switches[i]
	}, func(sws []*p2p.Switch, i, j int) {
		// the network starts partitioned with globally active adversary
		if i != 0 {
			return
		}
		p2p.Connect2Switches(sws, i, j)
	})

	// byz proposer sends one block to peers[0]
	// and the other block to peers[1] and peers[2].
	// note peers and switches order don't match.
	peers := switches[0].Peers().List()
	ind0 := getSwitchIndex(switches, peers[0])
	ind1 := getSwitchIndex(switches, peers[1])
	ind2 := getSwitchIndex(switches, peers[2])

	// connect the 2 peers in the larger partition
	p2p.Connect2Switches(switches, ind1, ind2)

	// wait for someone in the big partition to make a block

	select {
	case <-eventChans[ind2]:
	}

	log.Notice("A block has been committed. Healing partition")

	// connect the partitions
	p2p.Connect2Switches(switches, ind0, ind1)
	p2p.Connect2Switches(switches, ind0, ind2)

	// wait till everyone makes the first new block
	// (one of them already has)
	wg := new(sync.WaitGroup)
	wg.Add(2)
	for i := 1; i < N-1; i++ {
		go func(j int) {
			<-eventChans[j]
			wg.Done()
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	tick := time.NewTicker(time.Second * 10)
	select {
	case <-done:
	case <-tick.C:
		t.Fatalf("Timed out waiting for all validators to commit first block")
	}
}

func getSwitchIndex(switches []*p2p.Switch, peer *p2p.Peer) int {
	for i, s := range switches {
		if bytes.Equal(peer.NodeInfo.PubKey.Address(), s.NodeInfo().PubKey.Address()) {
			return i
		}
	}
	panic("didnt find peer in switches")
	return -1
}

//-------------------------------
// byzantine consensus functions

func byzantineDecideProposalFunc(height, round int, cs *ConsensusState, sw *p2p.Switch) {
	// byzantine user should create two proposals and try to split the vote.
	// Avoid sending on internalMsgQueue and running consensus state.

	// Create a new proposal block from state/txs from the mempool.
	block1, blockParts1 := cs.createProposalBlock()
	polRound, polBlockID := cs.Votes.POLInfo()
	proposal1 := types.NewProposal(height, round, blockParts1.Header(), polRound, polBlockID)
	cs.privValidator.SignProposal(cs.state.ChainID, proposal1) // byzantine doesnt err

	// Create a new proposal block from state/txs from the mempool.
	block2, blockParts2 := cs.createProposalBlock()
	polRound, polBlockID = cs.Votes.POLInfo()
	proposal2 := types.NewProposal(height, round, blockParts2.Header(), polRound, polBlockID)
	cs.privValidator.SignProposal(cs.state.ChainID, proposal2) // byzantine doesnt err

	block1Hash := block1.Hash()
	block2Hash := block2.Hash()

	// broadcast conflicting proposals/block parts to peers
	peers := sw.Peers().List()
	log.Notice("Byzantine: broadcasting conflicting proposals", "peers", len(peers))
	for i, peer := range peers {
		if i < len(peers)/2 {
			go sendProposalAndParts(height, round, cs, peer, proposal1, block1Hash, blockParts1)
		} else {
			go sendProposalAndParts(height, round, cs, peer, proposal2, block2Hash, blockParts2)
		}
	}
}

func sendProposalAndParts(height, round int, cs *ConsensusState, peer *p2p.Peer, proposal *types.Proposal, blockHash []byte, parts *types.PartSet) {
	// proposal
	msg := &ProposalMessage{Proposal: proposal}
	peer.Send(DataChannel, struct{ ConsensusMessage }{msg})

	// parts
	for i := 0; i < parts.Total(); i++ {
		part := parts.GetPart(i)
		msg := &BlockPartMessage{
			Height: height, // This tells peer that this part applies to us.
			Round:  round,  // This tells peer that this part applies to us.
			Part:   part,
		}
		peer.Send(DataChannel, struct{ ConsensusMessage }{msg})
	}

	// votes
	cs.mtx.Lock()
	prevote, _ := cs.signVote(types.VoteTypePrevote, blockHash, parts.Header())
	precommit, _ := cs.signVote(types.VoteTypePrecommit, blockHash, parts.Header())
	cs.mtx.Unlock()

	peer.Send(VoteChannel, struct{ ConsensusMessage }{&VoteMessage{prevote}})
	peer.Send(VoteChannel, struct{ ConsensusMessage }{&VoteMessage{precommit}})
}

//----------------------------------------
// byzantine consensus reactor

type ByzantineReactor struct {
	Service
	reactor *ConsensusReactor
}

func NewByzantineReactor(conR *ConsensusReactor) *ByzantineReactor {
	return &ByzantineReactor{
		Service: conR,
		reactor: conR,
	}
}

func (br *ByzantineReactor) SetSwitch(s *p2p.Switch)               { br.reactor.SetSwitch(s) }
func (br *ByzantineReactor) GetChannels() []*p2p.ChannelDescriptor { return br.reactor.GetChannels() }
func (br *ByzantineReactor) AddPeer(peer *p2p.Peer) {
	if !br.reactor.IsRunning() {
		return
	}

	// Create peerState for peer
	peerState := NewPeerState(peer)
	peer.Data.Set(types.PeerStateKey, peerState)

	// Send our state to peer.
	// If we're fast_syncing, broadcast a RoundStepMessage later upon SwitchToConsensus().
	if !br.reactor.fastSync {
		br.reactor.sendNewRoundStepMessage(peer)
	}
}
func (br *ByzantineReactor) RemovePeer(peer *p2p.Peer, reason interface{}) {
	br.reactor.RemovePeer(peer, reason)
}
func (br *ByzantineReactor) Receive(chID byte, peer *p2p.Peer, msgBytes []byte) {
	br.reactor.Receive(chID, peer, msgBytes)
}

//----------------------------------------
// byzantine privValidator

type ByzantinePrivValidator struct {
	Address      []byte `json:"address"`
	types.Signer `json:"-"`

	mtx sync.Mutex
}

// Return a priv validator that will sign anything
func NewByzantinePrivValidator(pv *types.PrivValidator) *ByzantinePrivValidator {
	return &ByzantinePrivValidator{
		Address: pv.Address,
		Signer:  pv.Signer,
	}
}

func (privVal *ByzantinePrivValidator) GetAddress() []byte {
	return privVal.Address
}

func (privVal *ByzantinePrivValidator) SignVote(chainID string, vote *types.Vote) error {
	privVal.mtx.Lock()
	defer privVal.mtx.Unlock()

	// Sign
	vote.Signature = privVal.Sign(types.SignBytes(chainID, vote)).(crypto.SignatureEd25519)
	return nil
}

func (privVal *ByzantinePrivValidator) SignProposal(chainID string, proposal *types.Proposal) error {
	privVal.mtx.Lock()
	defer privVal.mtx.Unlock()

	// Sign
	proposal.Signature = privVal.Sign(types.SignBytes(chainID, proposal)).(crypto.SignatureEd25519)
	return nil
}

func (privVal *ByzantinePrivValidator) String() string {
	return Fmt("PrivValidator{%X}", privVal.Address)
}
