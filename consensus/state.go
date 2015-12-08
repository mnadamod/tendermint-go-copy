package consensus

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"time"

	. "github.com/tendermint/go-common"
	"github.com/tendermint/go-wire"
	bc "github.com/tendermint/tendermint/blockchain"
	"github.com/tendermint/tendermint/events"
	mempl "github.com/tendermint/tendermint/mempool"
	"github.com/tendermint/tendermint/proxy"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

var (
	timeoutPropose        = 3000 * time.Millisecond // Maximum duration of RoundStepPropose
	timeoutPrevote0       = 1000 * time.Millisecond // After any +2/3 prevotes received, wait this long for stragglers.
	timeoutPrevoteDelta   = 0500 * time.Millisecond // timeoutPrevoteN is timeoutPrevote0 + timeoutPrevoteDelta*N
	timeoutPrecommit0     = 1000 * time.Millisecond // After any +2/3 precommits received, wait this long for stragglers.
	timeoutPrecommitDelta = 0500 * time.Millisecond // timeoutPrecommitN is timeoutPrecommit0 + timeoutPrecommitDelta*N
	timeoutCommit         = 2000 * time.Millisecond // After +2/3 commits received for committed block, wait this long for stragglers in the next height's RoundStepNewHeight.
)

var (
	ErrInvalidProposalSignature = errors.New("Error invalid proposal signature")
	ErrInvalidProposalPOLRound  = errors.New("Error invalid proposal POL round")
	ErrAddingVote               = errors.New("Error adding vote")
	ErrVoteHeightMismatch       = errors.New("Error vote height mismatch")
)

//-----------------------------------------------------------------------------
// RoundStepType enum type

type RoundStepType uint8 // These must be numeric, ordered.

const (
	RoundStepNewHeight     = RoundStepType(0x01) // Wait til CommitTime + timeoutCommit
	RoundStepNewRound      = RoundStepType(0x02) // Setup new round and go to RoundStepPropose
	RoundStepPropose       = RoundStepType(0x03) // Did propose, gossip proposal
	RoundStepPrevote       = RoundStepType(0x04) // Did prevote, gossip prevotes
	RoundStepPrevoteWait   = RoundStepType(0x05) // Did receive any +2/3 prevotes, start timeout
	RoundStepPrecommit     = RoundStepType(0x06) // Did precommit, gossip precommits
	RoundStepPrecommitWait = RoundStepType(0x07) // Did receive any +2/3 precommits, start timeout
	RoundStepCommit        = RoundStepType(0x08) // Entered commit state machine
	// NOTE: RoundStepNewHeight acts as RoundStepCommitWait.
)

func (rs RoundStepType) String() string {
	switch rs {
	case RoundStepNewHeight:
		return "RoundStepNewHeight"
	case RoundStepNewRound:
		return "RoundStepNewRound"
	case RoundStepPropose:
		return "RoundStepPropose"
	case RoundStepPrevote:
		return "RoundStepPrevote"
	case RoundStepPrevoteWait:
		return "RoundStepPrevoteWait"
	case RoundStepPrecommit:
		return "RoundStepPrecommit"
	case RoundStepPrecommitWait:
		return "RoundStepPrecommitWait"
	case RoundStepCommit:
		return "RoundStepCommit"
	default:
		return "RoundStepUnknown" // Cannot panic.
	}
}

//-----------------------------------------------------------------------------

// Immutable when returned from ConsensusState.GetRoundState()
type RoundState struct {
	Height             int // Height we are working on
	Round              int
	Step               RoundStepType
	StartTime          time.Time
	CommitTime         time.Time // Subjective time when +2/3 precommits for Block at Round were found
	Validators         *types.ValidatorSet
	Proposal           *types.Proposal
	ProposalBlock      *types.Block
	ProposalBlockParts *types.PartSet
	LockedRound        int
	LockedBlock        *types.Block
	LockedBlockParts   *types.PartSet
	Votes              *HeightVoteSet
	CommitRound        int            //
	LastCommit         *types.VoteSet // Last precommits at Height-1
	LastValidators     *types.ValidatorSet
}

func (rs *RoundState) RoundStateEvent() *types.EventDataRoundState {
	return &types.EventDataRoundState{
		CurrentTime:   time.Now(),
		Height:        rs.Height,
		Round:         rs.Round,
		Step:          rs.Step.String(),
		StartTime:     rs.StartTime,
		CommitTime:    rs.CommitTime,
		Proposal:      rs.Proposal,
		ProposalBlock: rs.ProposalBlock,
		LockedRound:   rs.LockedRound,
		LockedBlock:   rs.LockedBlock,
		POLRound:      rs.Votes.POLRound(),
	}
}

func (rs *RoundState) String() string {
	return rs.StringIndented("")
}

func (rs *RoundState) StringIndented(indent string) string {
	return fmt.Sprintf(`RoundState{
%s  H:%v R:%v S:%v
%s  StartTime:     %v
%s  CommitTime:    %v
%s  Validators:    %v
%s  Proposal:      %v
%s  ProposalBlock: %v %v
%s  LockedRound:   %v
%s  LockedBlock:   %v %v
%s  Votes:         %v
%s  LastCommit: %v
%s  LastValidators:    %v
%s}`,
		indent, rs.Height, rs.Round, rs.Step,
		indent, rs.StartTime,
		indent, rs.CommitTime,
		indent, rs.Validators.StringIndented(indent+"    "),
		indent, rs.Proposal,
		indent, rs.ProposalBlockParts.StringShort(), rs.ProposalBlock.StringShort(),
		indent, rs.LockedRound,
		indent, rs.LockedBlockParts.StringShort(), rs.LockedBlock.StringShort(),
		indent, rs.Votes.StringIndented(indent+"    "),
		indent, rs.LastCommit.StringShort(),
		indent, rs.LastValidators.StringIndented(indent+"    "),
		indent)
}

func (rs *RoundState) StringShort() string {
	return fmt.Sprintf(`RoundState{H:%v R:%v S:%v ST:%v}`,
		rs.Height, rs.Round, rs.Step, rs.StartTime)
}

//-----------------------------------------------------------------------------

var (
	msgQueueSize   = 1000
	tickBufferSize = 0 // I think this will deadlock ...
	tockBufferSize = 0
)

// msgs from the reactor which update the state
type msgInfo struct {
	msg     ConsensusMessage
	peerKey string
}

// internally generated messages which update the state
type timeoutInfo struct {
	duration time.Duration
	height   int
	round    int
	step     RoundStepType
}

func (ti *timeoutInfo) String() string {
	return fmt.Sprintf("%v ; %d/%d %v", ti.duration, ti.height, ti.round, ti.step)
}

// Tracks consensus state across block heights and rounds.
type ConsensusState struct {
	BaseService

	proxyAppCtx   proxy.AppContext
	blockStore    *bc.BlockStore
	mempool       *mempl.Mempool
	privValidator *types.PrivValidator
	newStepCh     chan *RoundState

	mtx sync.Mutex
	RoundState
	state       *sm.State    // State until height-1.
	stagedBlock *types.Block // Cache last staged block.
	stagedState *sm.State    // Cache result of staged block.

	roundState RoundState       // roundState for the receiveRoutine
	msgQueue   chan msgInfo     // serializes msgs affecting state (proposals, block parts, votes)
	tickChan   chan timeoutInfo // start a timer in the timeoutRoutine
	tockChan   chan timeoutInfo // receive a timeout

	evsw events.Fireable
	evc  *events.EventCache // set in stageBlock and passed into state
}

func NewConsensusState(state *sm.State, proxyAppCtx proxy.AppContext, blockStore *bc.BlockStore, mempool *mempl.Mempool) *ConsensusState {
	cs := &ConsensusState{
		proxyAppCtx: proxyAppCtx,
		blockStore:  blockStore,
		mempool:     mempool,
		newStepCh:   make(chan *RoundState, 10),
		msgQueue:    make(chan msgInfo, msgQueueSize),
		tickChan:    make(chan timeoutInfo, tickBufferSize),
		tockChan:    make(chan timeoutInfo, tockBufferSize),
	}
	cs.updateToState(state)
	// Don't call scheduleRound0 yet.
	// We do that upon Start().
	cs.reconstructLastCommit(state)
	cs.BaseService = *NewBaseService(log, "ConsensusState", cs)
	return cs
}

// Reconstruct LastCommit from SeenValidation, which we saved along with the block,
// (which happens even before saving the state)
func (cs *ConsensusState) reconstructLastCommit(state *sm.State) {
	if state.LastBlockHeight == 0 {
		return
	}
	lastPrecommits := types.NewVoteSet(state.LastBlockHeight, 0, types.VoteTypePrecommit, state.LastValidators)
	seenValidation := cs.blockStore.LoadSeenValidation(state.LastBlockHeight)
	for idx, precommit := range seenValidation.Precommits {
		if precommit == nil {
			continue
		}
		added, _, err := lastPrecommits.AddByIndex(idx, precommit)
		if !added || err != nil {
			PanicCrisis(Fmt("Failed to reconstruct LastCommit: %v", err))
		}
	}
	if !lastPrecommits.HasTwoThirdsMajority() {
		PanicSanity("Failed to reconstruct LastCommit: Does not have +2/3 maj")
	}
	cs.LastCommit = lastPrecommits
}

func (cs *ConsensusState) GetState() *sm.State {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	return cs.state.Copy()
}

func (cs *ConsensusState) GetRoundState() *RoundState {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	return cs.getRoundState()
}

func (cs *ConsensusState) getRoundState() *RoundState {
	rs := cs.RoundState // copy
	return &rs
}

func (cs *ConsensusState) NewStepCh() chan *RoundState {
	return cs.newStepCh
}

func (cs *ConsensusState) OnStart() error {
	cs.BaseService.OnStart()
	go cs.scheduleRound0(cs.Height)
	return nil
}

func (cs *ConsensusState) OnStop() {
	// It's asynchronous so, there's not much to stop.
	cs.BaseService.OnStop()
}

func (cs *ConsensusState) updateHeight(height int) {
	cs.Height = height

	cs.roundState.Height = height
}

func (cs *ConsensusState) updateRoundStep(round int, step RoundStepType) {
	cs.Round = round
	cs.Step = step

	cs.roundState.Round = round
	cs.roundState.Step = step
}

// EnterNewRound(height, 0) at cs.StartTime.
func (cs *ConsensusState) scheduleRound0(height int) {
	//log.Info("scheduleRound0", "now", time.Now(), "startTime", cs.StartTime)
	sleepDuration := cs.StartTime.Sub(time.Now())

	log.Debug("scheduleRound0 by firing on tick", "height", height)
	// if sleepDuration is 0 it will just relay it to tockChan right away
	cs.scheduleHeightRoundStep(sleepDuration, height, 0, RoundStepNewHeight)
}

func (cs *ConsensusState) scheduleHeightRoundStep(duration time.Duration, height, round int, step RoundStepType) {
	cs.tickChan <- timeoutInfo{duration, height, round, step}
}

// Updates ConsensusState and increments height to match that of state.
// The round becomes 0 and cs.Step becomes RoundStepNewHeight.
func (cs *ConsensusState) updateToState(state *sm.State) {
	if cs.CommitRound > -1 && 0 < cs.Height && cs.Height != state.LastBlockHeight {
		PanicSanity(Fmt("updateToState() expected state height of %v but found %v",
			cs.Height, state.LastBlockHeight))
	}
	if cs.state != nil && cs.state.LastBlockHeight+1 != cs.Height {
		// This might happen when someone else is mutating cs.state.
		// Someone forgot to pass in state.Copy() somewhere?!
		PanicSanity(Fmt("Inconsistent cs.state.LastBlockHeight+1 %v vs cs.Height %v",
			cs.state.LastBlockHeight+1, cs.Height))
	}

	// If state isn't further out than cs.state, just ignore.
	// This happens when SwitchToConsensus() is called in the reactor.
	// We don't want to reset e.g. the Votes.
	if cs.state != nil && (state.LastBlockHeight <= cs.state.LastBlockHeight) {
		log.Notice("Ignoring updateToState()", "newHeight", state.LastBlockHeight+1, "oldHeight", cs.state.LastBlockHeight+1)
		return
	}

	// Reset fields based on state.
	validators := state.Validators
	height := state.LastBlockHeight + 1 // next desired block height
	lastPrecommits := (*types.VoteSet)(nil)
	if cs.CommitRound > -1 && cs.Votes != nil {
		if !cs.Votes.Precommits(cs.CommitRound).HasTwoThirdsMajority() {
			PanicSanity("updateToState(state) called but last Precommit round didn't have +2/3")
		}
		lastPrecommits = cs.Votes.Precommits(cs.CommitRound)
	}

	// RoundState fields
	cs.updateHeight(height)
	cs.updateRoundStep(0, RoundStepNewHeight)
	if cs.CommitTime.IsZero() {
		// "Now" makes it easier to sync up dev nodes.
		// We add timeoutCommit to allow transactions
		// to be gathered for the first block.
		// And alternative solution that relies on clocks:
		//  cs.StartTime = state.LastBlockTime.Add(timeoutCommit)
		cs.StartTime = time.Now().Add(timeoutCommit)
	} else {
		cs.StartTime = cs.CommitTime.Add(timeoutCommit)
	}
	cs.CommitTime = time.Time{}
	cs.Validators = validators
	cs.Proposal = nil
	cs.ProposalBlock = nil
	cs.ProposalBlockParts = nil
	cs.LockedRound = 0
	cs.LockedBlock = nil
	cs.LockedBlockParts = nil
	cs.Votes = NewHeightVoteSet(height, validators)
	cs.CommitRound = -1
	cs.LastCommit = lastPrecommits
	cs.LastValidators = state.LastValidators

	cs.state = state
	cs.stagedBlock = nil
	cs.stagedState = nil

	// Finally, broadcast RoundState
	cs.newStepCh <- cs.getRoundState()
}

func (cs *ConsensusState) SetPrivValidator(priv *types.PrivValidator) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	cs.privValidator = priv
}

//-----------------------------------------------------------------------------

// Enter: +2/3 precommits for nil at (height,round-1)
// Enter: `timeoutPrecommits` after any +2/3 precommits from (height,round-1)
// Enter: `startTime = commitTime+timeoutCommit` from NewHeight(height)
// NOTE: cs.StartTime was already set for height.
func (cs *ConsensusState) EnterNewRound(height int, round int, timedOut bool) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	if cs.Height != height || round < cs.Round || (cs.Round == round && cs.Step != RoundStepNewHeight) {
		log.Debug(Fmt("EnterNewRound(%v/%v): Invalid args. Current step: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))
		return
	}

	if timedOut {
		cs.evsw.FireEvent(types.EventStringTimeoutWait(), cs.RoundStateEvent())
	}

	if now := time.Now(); cs.StartTime.After(now) {
		log.Warn("Need to set a buffer and log.Warn() here for sanity.", "startTime", cs.StartTime, "now", now)
	}
	log.Notice(Fmt("EnterNewRound(%v/%v). Current: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))

	// Increment validators if necessary
	validators := cs.Validators
	if cs.Round < round {
		validators = validators.Copy()
		validators.IncrementAccum(round - cs.Round)
	}

	// Setup new round
	cs.updateRoundStep(round, RoundStepNewRound)
	cs.Validators = validators
	if round == 0 {
		// We've already reset these upon new height,
		// and meanwhile we might have received a proposal
		// for round 0.
	} else {
		cs.Proposal = nil
		cs.ProposalBlock = nil
		cs.ProposalBlockParts = nil
	}
	cs.Votes.SetRound(round + 1) // also track next round (round+1) to allow round-skipping

	cs.evsw.FireEvent(types.EventStringNewRound(), cs.RoundStateEvent())

	// Immediately go to EnterPropose.
	go cs.EnterPropose(height, round)
}

// Enter: from NewRound(height,round).
func (cs *ConsensusState) EnterPropose(height int, round int) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	if cs.Height != height || round < cs.Round || (cs.Round == round && RoundStepPropose <= cs.Step) {
		log.Debug(Fmt("EnterPropose(%v/%v): Invalid args. Current step: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))
		return
	}
	log.Info(Fmt("EnterPropose(%v/%v). Current: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))

	defer func() {
		// Done EnterPropose:
		cs.updateRoundStep(round, RoundStepPropose)
		cs.newStepCh <- cs.getRoundState()

		// If we have the whole proposal + POL, then goto Prevote now.
		// else, we'll EnterPrevote when the rest of the proposal is received (in AddProposalBlockPart),
		// or else after timeoutPropose
		if cs.isProposalComplete() {
			go cs.EnterPrevote(height, cs.Round, false)
		}
	}()

	// This step times out after `timeoutPropose`
	cs.tickChan <- timeoutInfo{timeoutPropose, height, round, RoundStepPropose}
	log.Debug("started timer")

	// Nothing more to do if we're not a validator
	if cs.privValidator == nil {
		return
	}

	if !bytes.Equal(cs.Validators.Proposer().Address, cs.privValidator.Address) {
		log.Info("EnterPropose: Not our turn to propose", "proposer", cs.Validators.Proposer().Address, "privValidator", cs.privValidator)
	} else {
		log.Info("EnterPropose: Our turn to propose", "proposer", cs.Validators.Proposer().Address, "privValidator", cs.privValidator)
		cs.decideProposal(height, round)
	}
}

func (cs *ConsensusState) decideProposal(height, round int) {
	var block *types.Block
	var blockParts *types.PartSet

	// Decide on block
	if cs.LockedBlock != nil {
		// If we're locked onto a block, just choose that.
		block, blockParts = cs.LockedBlock, cs.LockedBlockParts
	} else {
		// Create a new proposal block from state/txs from the mempool.
		block, blockParts = cs.createProposalBlock()
		if block == nil { // on error
			return
		}
	}

	// Make proposal
	proposal := types.NewProposal(height, round, blockParts.Header(), cs.Votes.POLRound())
	err := cs.privValidator.SignProposal(cs.state.ChainID, proposal)
	if err == nil {
		log.Notice("Signed and set proposal", "height", height, "round", round, "proposal", proposal)
		log.Debug(Fmt("Signed and set proposal block: %v", block))
		// Set fields
		cs.Proposal = proposal
		cs.ProposalBlock = block
		cs.ProposalBlockParts = blockParts

		// TODO: can we do better than just launching a go routine?
		go func() {
			cs.msgQueue <- msgInfo{&ProposalMessage{proposal}, ""}
			for i := 0; i < blockParts.Total(); i++ {
				part := blockParts.GetPart(i)
				cs.msgQueue <- msgInfo{&BlockPartMessage{cs.Height, cs.Round, part}, ""}
			}
		}()
	} else {
		log.Warn("EnterPropose: Error signing proposal", "height", height, "round", round, "error", err)
	}

}

// Returns true if the proposal block is complete &&
// (if POLRound was proposed, we have +2/3 prevotes from there).
func (cs *ConsensusState) isProposalComplete() bool {
	if cs.Proposal == nil || cs.ProposalBlock == nil {
		return false
	}
	// we have the proposal. if there's a POLRound,
	// make sure we have the prevotes from it too
	if cs.Proposal.POLRound < 0 {
		return true
	} else {
		// if this is false the proposer is lying or we haven't received the POL yet
		return cs.Votes.Prevotes(cs.Proposal.POLRound).HasTwoThirdsMajority()
	}
}

// Create the next block to propose and return it.
// Returns nil block upon error.
// NOTE: keep it side-effect free for clarity.
func (cs *ConsensusState) createProposalBlock() (block *types.Block, blockParts *types.PartSet) {
	var validation *types.Validation
	if cs.Height == 1 {
		// We're creating a proposal for the first block.
		// The validation is empty, but not nil.
		validation = &types.Validation{}
	} else if cs.LastCommit.HasTwoThirdsMajority() {
		// Make the validation from LastCommit
		validation = cs.LastCommit.MakeValidation()
	} else {
		// This shouldn't happen.
		log.Error("EnterPropose: Cannot propose anything: No validation for the previous block.")
		return
	}

	// Mempool run transactions and the resulting hash
	txs, hash, err := cs.mempool.Reap()
	if err != nil {
		log.Warn("createProposalBlock: Error getting proposal txs", "error", err)
		return nil, nil
	}

	block = &types.Block{
		Header: &types.Header{
			ChainID:        cs.state.ChainID,
			Height:         cs.Height,
			Time:           time.Now(),
			Fees:           0, // TODO fees
			NumTxs:         len(txs),
			LastBlockHash:  cs.state.LastBlockHash,
			LastBlockParts: cs.state.LastBlockParts,
			ValidatorsHash: cs.state.Validators.Hash(),
			AppHash:        hash,
		},
		LastValidation: validation,
		Data: &types.Data{
			Txs: txs,
		},
	}
	block.FillHeader()
	blockParts = block.MakePartSet()

	return block, blockParts
}

// Enter: `timeoutPropose` after entering Propose.
// Enter: proposal block and POL is ready.
// Enter: any +2/3 prevotes for future round.
// Prevote for LockedBlock if we're locked, or ProposalBlock if valid.
// Otherwise vote nil.
func (cs *ConsensusState) EnterPrevote(height int, round int, timedOut bool) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	if cs.Height != height || round < cs.Round || (cs.Round == round && RoundStepPrevote <= cs.Step) {
		log.Debug(Fmt("EnterPrevote(%v/%v): Invalid args. Current step: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))
		return
	}

	// fire event for how we got here
	if timedOut {
		cs.evsw.FireEvent(types.EventStringTimeoutPropose(), cs.RoundStateEvent())
	} else if cs.isProposalComplete() {
		cs.evsw.FireEvent(types.EventStringCompleteProposal(), cs.RoundStateEvent())
	} else {
		// we received +2/3 prevotes for a future round
		// TODO: catchup event?
	}

	log.Info(Fmt("EnterPrevote(%v/%v). Current: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))

	// Sign and broadcast vote as necessary
	cs.doPrevote(height, round)

	// Done EnterPrevote:
	cs.updateRoundStep(round, RoundStepPrevote)
	cs.newStepCh <- cs.getRoundState()

	// Once `addVote` hits any +2/3 prevotes, we will go to PrevoteWait
	// (so we have more time to try and collect +2/3 prevotes for a single block)
}

func (cs *ConsensusState) doPrevote(height int, round int) {
	// If a block is locked, prevote that.
	if cs.LockedBlock != nil {
		log.Info("EnterPrevote: Block was locked")
		cs.signAddVote(types.VoteTypePrevote, cs.LockedBlock.Hash(), cs.LockedBlockParts.Header())
		return
	}

	// If ProposalBlock is nil, prevote nil.
	if cs.ProposalBlock == nil {
		log.Warn("EnterPrevote: ProposalBlock is nil")
		cs.signAddVote(types.VoteTypePrevote, nil, types.PartSetHeader{})
		return
	}

	// Try staging cs.ProposalBlock
	err := cs.stageBlock(cs.ProposalBlock, cs.ProposalBlockParts)
	if err != nil {
		// ProposalBlock is invalid, prevote nil.
		log.Warn("EnterPrevote: ProposalBlock is invalid", "error", err)
		cs.signAddVote(types.VoteTypePrevote, nil, types.PartSetHeader{})
		return
	}

	// Prevote cs.ProposalBlock
	// NOTE: the proposal signature is validated when it is received,
	// and the proposal block parts are validated as they are received (against the merkle hash in the proposal)
	cs.signAddVote(types.VoteTypePrevote, cs.ProposalBlock.Hash(), cs.ProposalBlockParts.Header())
	return
}

// Enter: any +2/3 prevotes at next round.
func (cs *ConsensusState) EnterPrevoteWait(height int, round int) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	if cs.Height != height || round < cs.Round || (cs.Round == round && RoundStepPrevoteWait <= cs.Step) {
		log.Debug(Fmt("EnterPrevoteWait(%v/%v): Invalid args. Current step: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))
		return
	}
	if !cs.Votes.Prevotes(round).HasTwoThirdsAny() {
		PanicSanity(Fmt("EnterPrevoteWait(%v/%v), but Prevotes does not have any +2/3 votes", height, round))
	}
	log.Info(Fmt("EnterPrevoteWait(%v/%v). Current: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))

	// Done EnterPrevoteWait:
	cs.updateRoundStep(round, RoundStepPrevoteWait)
	cs.newStepCh <- cs.getRoundState()

	// After `timeoutPrevote0+timeoutPrevoteDelta*round`, EnterPrecommit()
	cs.tickChan <- timeoutInfo{timeoutPrevote0 + timeoutPrevoteDelta*time.Duration(round), height, round, RoundStepPrevote}
}

// Enter: +2/3 precomits for block or nil.
// Enter: `timeoutPrevote` after any +2/3 prevotes.
// Enter: any +2/3 precommits for next round.
// Lock & precommit the ProposalBlock if we have enough prevotes for it (a POL in this round)
// else, unlock an existing lock and precommit nil if +2/3 of prevotes were nil,
// else, precommit nil otherwise.
func (cs *ConsensusState) EnterPrecommit(height int, round int, timedOut bool) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	if cs.Height != height || round < cs.Round || (cs.Round == round && RoundStepPrecommit <= cs.Step) {
		log.Debug(Fmt("EnterPrecommit(%v/%v): Invalid args. Current step: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))
		return
	}

	if timedOut {
		cs.evsw.FireEvent(types.EventStringTimeoutWait(), cs.RoundStateEvent())
	}

	log.Info(Fmt("EnterPrecommit(%v/%v). Current: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))

	defer func() {
		// Done EnterPrecommit:
		cs.updateRoundStep(round, RoundStepPrecommit)
		cs.newStepCh <- cs.getRoundState()
	}()

	hash, partsHeader, ok := cs.Votes.Prevotes(round).TwoThirdsMajority()

	// If we don't have a polka, we must precommit nil
	if !ok {
		if cs.LockedBlock != nil {
			log.Info("EnterPrecommit: No +2/3 prevotes during EnterPrecommit while we're locked. Precommitting nil")
		} else {
			log.Info("EnterPrecommit: No +2/3 prevotes during EnterPrecommit. Precommitting nil.")
		}
		cs.signAddVote(types.VoteTypePrecommit, nil, types.PartSetHeader{})
		return
	}

	// At this point +2/3 prevoted for a particular block or nil
	cs.evsw.FireEvent(types.EventStringPolka(), cs.RoundStateEvent())

	// the latest POLRound should be this round
	if cs.Votes.POLRound() < round {
		PanicSanity(Fmt("This POLRound should be %v but got %", round, cs.Votes.POLRound()))
	}

	// +2/3 prevoted nil. Unlock and precommit nil.
	if len(hash) == 0 {
		if cs.LockedBlock == nil {
			log.Info("EnterPrecommit: +2/3 prevoted for nil.")
		} else {
			log.Info("EnterPrecommit: +2/3 prevoted for nil. Unlocking")
			cs.LockedRound = 0
			cs.LockedBlock = nil
			cs.LockedBlockParts = nil
			cs.evsw.FireEvent(types.EventStringUnlock(), cs.RoundStateEvent())
		}
		cs.signAddVote(types.VoteTypePrecommit, nil, types.PartSetHeader{})
		return
	}

	// At this point, +2/3 prevoted for a particular block.

	// If we're already locked on that block, precommit it, and update the LockedRound
	if cs.LockedBlock.HashesTo(hash) {
		log.Info("EnterPrecommit: +2/3 prevoted locked block. Relocking")
		cs.LockedRound = round
		cs.evsw.FireEvent(types.EventStringRelock(), cs.RoundStateEvent())
		cs.signAddVote(types.VoteTypePrecommit, hash, partsHeader)
		return
	}

	// If +2/3 prevoted for proposal block, stage and precommit it
	if cs.ProposalBlock.HashesTo(hash) {
		log.Info("EnterPrecommit: +2/3 prevoted proposal block. Locking", "hash", hash)
		// Validate the block.
		if err := cs.stageBlock(cs.ProposalBlock, cs.ProposalBlockParts); err != nil {
			PanicConsensus(Fmt("EnterPrecommit: +2/3 prevoted for an invalid block: %v", err))
		}
		cs.LockedRound = round
		cs.LockedBlock = cs.ProposalBlock
		cs.LockedBlockParts = cs.ProposalBlockParts
		cs.evsw.FireEvent(types.EventStringLock(), cs.RoundStateEvent())
		cs.signAddVote(types.VoteTypePrecommit, hash, partsHeader)
		return
	}

	// There was a polka in this round for a block we don't have.
	// Fetch that block, unlock, and precommit nil.
	// The +2/3 prevotes for this round is the POL for our unlock.
	// TODO: In the future save the POL prevotes for justification.
	cs.LockedRound = 0
	cs.LockedBlock = nil
	cs.LockedBlockParts = nil
	if !cs.ProposalBlockParts.HasHeader(partsHeader) {
		cs.ProposalBlock = nil
		cs.ProposalBlockParts = types.NewPartSetFromHeader(partsHeader)
	}
	cs.evsw.FireEvent(types.EventStringUnlock(), cs.RoundStateEvent())
	cs.signAddVote(types.VoteTypePrecommit, nil, types.PartSetHeader{})
	return
}

// Enter: any +2/3 precommits for next round.
func (cs *ConsensusState) EnterPrecommitWait(height int, round int) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	if cs.Height != height || round < cs.Round || (cs.Round == round && RoundStepPrecommitWait <= cs.Step) {
		log.Debug(Fmt("EnterPrecommitWait(%v/%v): Invalid args. Current step: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))
		return
	}
	if !cs.Votes.Precommits(round).HasTwoThirdsAny() {
		PanicSanity(Fmt("EnterPrecommitWait(%v/%v), but Precommits does not have any +2/3 votes", height, round))
	}
	log.Info(Fmt("EnterPrecommitWait(%v/%v). Current: %v/%v/%v", height, round, cs.Height, cs.Round, cs.Step))

	// Done EnterPrecommitWait:
	cs.updateRoundStep(round, RoundStepPrecommitWait)
	cs.newStepCh <- cs.getRoundState()

	// After `timeoutPrecommit0+timeoutPrecommitDelta*round`, EnterNewRound()
	cs.tickChan <- timeoutInfo{timeoutPrecommit0 + timeoutPrecommitDelta*time.Duration(round), height, round, RoundStepPrecommit}

}

// Enter: +2/3 precommits for block
func (cs *ConsensusState) EnterCommit(height int, commitRound int) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()
	if cs.Height != height || RoundStepCommit <= cs.Step {
		log.Debug(Fmt("EnterCommit(%v/%v): Invalid args. Current step: %v/%v/%v", height, commitRound, cs.Height, cs.Round, cs.Step))
		return
	}
	log.Info(Fmt("EnterCommit(%v/%v). Current: %v/%v/%v", height, commitRound, cs.Height, cs.Round, cs.Step))

	defer func() {
		// Done Entercommit:
		// keep ca.Round the same, it points to the right Precommits set.
		cs.Step = RoundStepCommit
		cs.CommitRound = commitRound
		cs.newStepCh <- cs.getRoundState()

		// Maybe finalize immediately.
		cs.tryFinalizeCommit(height)
	}()

	hash, partsHeader, ok := cs.Votes.Precommits(commitRound).TwoThirdsMajority()
	if !ok {
		PanicSanity("RunActionCommit() expects +2/3 precommits")
	}

	// The Locked* fields no longer matter.
	// Move them over to ProposalBlock if they match the commit hash,
	// otherwise they'll be cleared in updateToState.
	if cs.LockedBlock.HashesTo(hash) {
		cs.ProposalBlock = cs.LockedBlock
		cs.ProposalBlockParts = cs.LockedBlockParts
	}

	// If we don't have the block being committed, set up to get it.
	if !cs.ProposalBlock.HashesTo(hash) {
		if !cs.ProposalBlockParts.HasHeader(partsHeader) {
			// We're getting the wrong block.
			// Set up ProposalBlockParts and keep waiting.
			cs.ProposalBlock = nil
			cs.ProposalBlockParts = types.NewPartSetFromHeader(partsHeader)
		} else {
			// We just need to keep waiting.
		}
	}
}

// If we have the block AND +2/3 commits for it, finalize.
func (cs *ConsensusState) tryFinalizeCommit(height int) {
	if cs.Height != height {
		PanicSanity(Fmt("tryFinalizeCommit() cs.Height: %v vs height: %v", cs.Height, height))
	}

	hash, _, ok := cs.Votes.Precommits(cs.CommitRound).TwoThirdsMajority()
	if !ok || len(hash) == 0 {
		log.Warn("Attempt to finalize failed. There was no +2/3 majority, or +2/3 was for <nil>.")
		return
	}
	if !cs.ProposalBlock.HashesTo(hash) {
		log.Warn("Attempt to finalize failed. We don't have the commit block.")
		return
	}
	go cs.FinalizeCommit(height)
}

// Increment height and goto RoundStepNewHeight
func (cs *ConsensusState) FinalizeCommit(height int) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()

	if cs.Height != height || cs.Step != RoundStepCommit {
		log.Debug(Fmt("FinalizeCommit(%v): Invalid args. Current step: %v/%v/%v", height, cs.Height, cs.Round, cs.Step))
		return
	}

	hash, header, ok := cs.Votes.Precommits(cs.CommitRound).TwoThirdsMajority()

	if !ok {
		PanicSanity(Fmt("Cannot FinalizeCommit, commit does not have two thirds majority"))
	}
	if !cs.ProposalBlockParts.HasHeader(header) {
		PanicSanity(Fmt("Expected ProposalBlockParts header to be commit header"))
	}
	if !cs.ProposalBlock.HashesTo(hash) {
		PanicSanity(Fmt("Cannot FinalizeCommit, ProposalBlock does not hash to commit hash"))
	}
	if err := cs.stageBlock(cs.ProposalBlock, cs.ProposalBlockParts); err != nil {
		PanicConsensus(Fmt("+2/3 committed an invalid block: %v", err))
	}

	log.Info(Fmt("Finalizing commit of block: %v", cs.ProposalBlock))
	// We have the block, so stage/save/commit-vote.
	cs.saveBlock(cs.ProposalBlock, cs.ProposalBlockParts, cs.Votes.Precommits(cs.CommitRound))
	// Increment height.
	cs.updateToState(cs.stagedState)
	// cs.StartTime is already set.
	// Schedule Round0 to start soon.
	go cs.scheduleRound0(height + 1)

	// By here,
	// * cs.Height has been increment to height+1
	// * cs.Step is now RoundStepNewHeight
	// * cs.StartTime is set to when we will start round0.
	return
}

//-----------------------------------------------------------------------------

func (cs *ConsensusState) SetProposal(proposal *types.Proposal) error {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()

	// Already have one
	if cs.Proposal != nil {
		return nil
	}

	// Does not apply
	if proposal.Height != cs.Height || proposal.Round != cs.Round {
		return nil
	}

	// We don't care about the proposal if we're already in RoundStepCommit.
	if RoundStepCommit <= cs.Step {
		return nil
	}

	// Verify POLRound, which must be -1 or between 0 and proposal.Round exclusive.
	if proposal.POLRound != -1 &&
		(proposal.POLRound < 0 || proposal.Round <= proposal.POLRound) {
		return ErrInvalidProposalPOLRound
	}

	// Verify signature
	if !cs.Validators.Proposer().PubKey.VerifyBytes(types.SignBytes(cs.state.ChainID, proposal), proposal.Signature) {
		return ErrInvalidProposalSignature
	}

	cs.Proposal = proposal
	cs.ProposalBlockParts = types.NewPartSetFromHeader(proposal.BlockPartsHeader)
	return nil
}

// NOTE: block is not necessarily valid.
// This can trigger us to go into EnterPrevote asynchronously (before we timeout of propose) or to attempt to commit
func (cs *ConsensusState) AddProposalBlockPart(height int, part *types.Part) (added bool, err error) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()

	// Blocks might be reused, so round mismatch is OK
	if cs.Height != height {
		return false, nil
	}

	// We're not expecting a block part.
	if cs.ProposalBlockParts == nil {
		return false, nil // TODO: bad peer? Return error?
	}

	added, err = cs.ProposalBlockParts.AddPart(part)
	if err != nil {
		return added, err
	}
	if added && cs.ProposalBlockParts.IsComplete() {
		// Added and completed!
		var n int
		var err error
		cs.ProposalBlock = wire.ReadBinary(&types.Block{}, cs.ProposalBlockParts.GetReader(), types.MaxBlockSize, &n, &err).(*types.Block)
		log.Info("Received complete proposal", "hash", cs.ProposalBlock.Hash())
		if cs.Step == RoundStepPropose && cs.isProposalComplete() {
			// Move onto the next step
			go cs.EnterPrevote(height, cs.Round, false)
		} else if cs.Step == RoundStepCommit {
			// If we're waiting on the proposal block...
			cs.tryFinalizeCommit(height)
		}
		return true, err
	}
	return added, nil
}

// Attempt to add the vote. if its a duplicate signature, dupeout the validator
func (cs *ConsensusState) TryAddVote(valIndex int, vote *types.Vote, peerKey string) (bool, error) {
	added, _, err := cs.AddVote(valIndex, vote, peerKey)
	if err != nil {
		// If the vote height is off, we'll just ignore it,
		// But if it's a conflicting sig, broadcast evidence tx for slashing.
		// If it's otherwise invalid, punish peer.
		if err == ErrVoteHeightMismatch {
			return added, err
		} else if _, ok := err.(*types.ErrVoteConflictingSignature); ok {
			log.Warn("Found conflicting vote. Publish evidence")
			/* TODO
			evidenceTx := &types.DupeoutTx{
				Address: address,
				VoteA:   *errDupe.VoteA,
				VoteB:   *errDupe.VoteB,
			}
			cs.mempool.BroadcastTx(evidenceTx) // shouldn't need to check returned err
			*/
			return added, err
		} else {
			// Probably an invalid signature. Bad peer.
			log.Warn("Error attempting to add vote", "error", err)
			return added, ErrAddingVote
		}
	}
	return added, nil
}

func (cs *ConsensusState) AddVote(valIndex int, vote *types.Vote, peerKey string) (added bool, address []byte, err error) {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()

	return cs.addVote(valIndex, vote, peerKey)
}

//-----------------------------------------------------------------------------

func (cs *ConsensusState) addVote(valIndex int, vote *types.Vote, peerKey string) (added bool, address []byte, err error) {
	log.Debug("addVote", "voteHeight", vote.Height, "voteType", vote.Type, "csHeight", cs.Height)

	defer func() {
		if added {
			cs.evsw.FireEvent(types.EventStringVote(), &types.EventDataVote{valIndex, address, vote})
		}
	}()

	// A precommit for the previous height?
	if vote.Height+1 == cs.Height {
		if !(cs.Step == RoundStepNewHeight && vote.Type == types.VoteTypePrecommit) {
			// TODO: give the reason ..
			// fmt.Errorf("TryAddVote: Wrong height, not a LastCommit straggler commit.")
			return added, nil, ErrVoteHeightMismatch
		}
		added, address, err = cs.LastCommit.AddByIndex(valIndex, vote)
		if added {
			log.Info(Fmt("Added to lastPrecommits: %v", cs.LastCommit.StringShort()))
		}
		return
	}

	// A prevote/precommit for this height?
	if vote.Height == cs.Height {
		height := cs.Height
		added, address, err = cs.Votes.AddByIndex(valIndex, vote, peerKey)
		if added {
			switch vote.Type {
			case types.VoteTypePrevote:
				prevotes := cs.Votes.Prevotes(vote.Round)
				log.Info("Added to prevote", "vote", vote, "prevotes", prevotes.StringShort())
				// First, unlock if prevotes is a valid POL.
				// >> lockRound < POLRound <= unlockOrChangeLockRound (see spec)
				// NOTE: If (lockRound < POLRound) but !(POLRound <= unlockOrChangeLockRound),
				// we'll still EnterNewRound(H,vote.R) and EnterPrecommit(H,vote.R) to process it
				// there.
				if (cs.LockedBlock != nil) && (cs.LockedRound < vote.Round) && (vote.Round <= cs.Round) {
					hash, _, ok := prevotes.TwoThirdsMajority()
					if ok && !cs.LockedBlock.HashesTo(hash) {
						log.Notice("Unlocking because of POL.", "lockedRound", cs.LockedRound, "POLRound", vote.Round)
						cs.LockedRound = 0
						cs.LockedBlock = nil
						cs.LockedBlockParts = nil
						cs.evsw.FireEvent(types.EventStringUnlock(), cs.RoundStateEvent())
					}
				}
				if cs.Round <= vote.Round && prevotes.HasTwoThirdsAny() {
					// Round-skip over to PrevoteWait or goto Precommit.
					go func() {
						cs.EnterNewRound(height, vote.Round, false)
						if prevotes.HasTwoThirdsMajority() {
							cs.EnterPrecommit(height, vote.Round, false)
						} else {
							cs.EnterPrevote(height, vote.Round, false)
							cs.EnterPrevoteWait(height, vote.Round)
						}
					}()
				} else if cs.Proposal != nil && 0 <= cs.Proposal.POLRound && cs.Proposal.POLRound == vote.Round {
					// If the proposal is now complete, enter prevote of cs.Round.
					if cs.isProposalComplete() {
						go cs.EnterPrevote(height, cs.Round, false)
					}
				}
			case types.VoteTypePrecommit:
				precommits := cs.Votes.Precommits(vote.Round)
				log.Info("Added to precommit", "vote", vote, "precommits", precommits.StringShort())
				hash, _, ok := precommits.TwoThirdsMajority()
				if ok {
					go func() {
						if len(hash) == 0 {
							cs.EnterNewRound(height, vote.Round+1, false)
						} else {
							cs.EnterNewRound(height, vote.Round, false)
							cs.EnterPrecommit(height, vote.Round, false)
							cs.EnterCommit(height, vote.Round)
						}
					}()
				} else if cs.Round <= vote.Round && precommits.HasTwoThirdsAny() {
					go func() {
						cs.EnterNewRound(height, vote.Round, false)
						cs.EnterPrecommit(height, vote.Round, false)
						cs.EnterPrecommitWait(height, vote.Round)
					}()
				}
			default:
				PanicSanity(Fmt("Unexpected vote type %X", vote.Type)) // Should not happen.
			}
		}
		// Either duplicate, or error upon cs.Votes.AddByIndex()
		return
	} else {
		err = ErrVoteHeightMismatch
	}

	// Height mismatch, bad peer?
	log.Info("Vote ignored and not added", "voteHeight", vote.Height, "csHeight", cs.Height)
	return
}

func (cs *ConsensusState) stageBlock(block *types.Block, blockParts *types.PartSet) error {
	if block == nil {
		PanicSanity("Cannot stage nil block")
	}

	// Already staged?
	blockHash := block.Hash()
	if cs.stagedBlock != nil && len(blockHash) != 0 && bytes.Equal(cs.stagedBlock.Hash(), blockHash) {
		return nil
	}

	// Create a new event cache to cache all events.
	cs.evc = events.NewEventCache(cs.evsw)

	// Create a copy of the state for staging
	stateCopy := cs.state.Copy()
	stateCopy.SetFireable(cs.evc)

	// Run the block on the State:
	// + update validator sets
	// + first rolls back proxyAppCtx
	// + run txs on the proxyAppCtx or rollback
	err := stateCopy.ExecBlock(cs.proxyAppCtx, block, blockParts.Header())
	if err != nil {
		return err
	}

	// Everything looks good!
	cs.stagedBlock = block
	cs.stagedState = stateCopy
	return nil
}

func (cs *ConsensusState) signVote(type_ byte, hash []byte, header types.PartSetHeader) (*types.Vote, error) {
	vote := &types.Vote{
		Height:           cs.Height,
		Round:            cs.Round,
		Type:             type_,
		BlockHash:        hash,
		BlockPartsHeader: header,
	}
	err := cs.privValidator.SignVote(cs.state.ChainID, vote)
	return vote, err
}

func (cs *ConsensusState) signAddVote(type_ byte, hash []byte, header types.PartSetHeader) *types.Vote {
	if cs.privValidator == nil || !cs.Validators.HasAddress(cs.privValidator.Address) {
		return nil
	}
	vote, err := cs.signVote(type_, hash, header)
	if err == nil {
		// NOTE: store our index in the cs so we don't have to do this every time
		valIndex, _ := cs.Validators.GetByAddress(cs.privValidator.Address)
		_, _, err := cs.addVote(valIndex, vote, "")
		log.Notice("Signed and added vote", "height", cs.Height, "round", cs.Round, "vote", vote, "error", err)
		// so we fire events for ourself and can run replays
		// TODO: can we do better than just launching a go-routine
		// XXX: maybe we can use a "personal" channel in the receiveRoutine select
		//  and fire these there so we don't possibly block on a full msgQueue.
		//  though if things are really backed up, we could block on the personal channel too
		go func() {
			cs.msgQueue <- msgInfo{&VoteMessage{valIndex, vote}, ""}
		}()
		return vote
	} else {
		log.Warn("Error signing vote", "height", cs.Height, "round", cs.Round, "vote", vote, "error", err)
		return nil
	}
}

// Save Block, save the +2/3 Commits we've seen
func (cs *ConsensusState) saveBlock(block *types.Block, blockParts *types.PartSet, commits *types.VoteSet) {

	// The proposal must be valid.
	if err := cs.stageBlock(block, blockParts); err != nil {
		PanicSanity(Fmt("saveBlock() an invalid block: %v", err))
	}

	// Save to blockStore.
	if cs.blockStore.Height() < block.Height {
		seenValidation := commits.MakeValidation()
		cs.blockStore.SaveBlock(block, blockParts, seenValidation)
	}

	// Commit to proxyAppCtx
	err := cs.stagedState.Commit(cs.proxyAppCtx)
	if err != nil {
		// TODO: handle this gracefully.
		PanicQ(Fmt("Commit failed for applicaiton"))
	}

	// Save the state.
	cs.stagedState.Save()

	// Update mempool.
	cs.mempool.Update(block)

	// Fire off event
	if cs.evsw != nil && cs.evc != nil {
		cs.evsw.FireEvent(types.EventStringNewBlock(), types.EventDataNewBlock{block})
		go cs.evc.Flush()
	}

}

// implements events.Eventable
func (cs *ConsensusState) SetFireable(evsw events.Fireable) {
	cs.evsw = evsw
}

func (cs *ConsensusState) String() string {
	return Fmt("ConsensusState(H:%v R:%v S:%v", cs.Height, cs.Round, cs.Step)
}

func CompareHRS(h1, r1 int, s1 RoundStepType, h2, r2 int, s2 RoundStepType) int {
	if h1 < h2 {
		return -1
	} else if h1 > h2 {
		return 1
	}
	if r1 < r2 {
		return -1
	} else if r1 > r2 {
		return 1
	}
	if s1 < s2 {
		return -1
	} else if s1 > s2 {
		return 1
	}
	return 0
}
