package consensus

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	. "github.com/tendermint/go-common"
	"github.com/tendermint/go-wire"

	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/tailseek"
	"github.com/tendermint/tendermint/types"
)

//--------------------------------------------------------
// types and functions for savings consensus messages

type ConsensusLogMessage struct {
	Time time.Time                    `json:"time"`
	Msg  ConsensusLogMessageInterface `json:"msg"`
}

type ConsensusLogMessageInterface interface{}

var _ = wire.RegisterInterface(
	struct{ ConsensusLogMessageInterface }{},
	wire.ConcreteType{&types.EventDataRoundState{}, 0x01},
	wire.ConcreteType{msgInfo{}, 0x02},
	wire.ConcreteType{timeoutInfo{}, 0x03},
)

// called in newStep and for each pass in receiveRoutine
func (cs *ConsensusState) saveMsg(msg ConsensusLogMessageInterface) {
	if cs.msgLogFP != nil {
		var n int
		var err error
		wire.WriteJSON(ConsensusLogMessage{time.Now(), msg}, cs.msgLogFP, &n, &err)
		wire.WriteTo([]byte("\n"), cs.msgLogFP, &n, &err) // one message per line
		if err != nil {
			log.Error("Error writing to consensus message log file", "err", err, "msg", msg)
		}
	}
}

//--------------------------------------------------------
// replay the messages

// Interactive playback
func (cs ConsensusState) ReplayConsole(file string) error {
	return cs.replay(file, true)
}

// Full playback, with tests
func (cs ConsensusState) ReplayMessages(file string) error {
	return cs.replay(file, false)
}

func (cs *ConsensusState) catchupReplay(height int) error {
	if !cs.msgLogExists {
		return nil
	}

	if cs.msgLogFP == nil {
		log.Warn("consensus msg log is nil")
		return nil
	}

	log.Notice("Catchup by replaying consensus messages")
	f := cs.msgLogFP

	n, err := seek.SeekFromEndOfFile(f, func(lineBytes []byte) bool {
		var err error
		var msg ConsensusLogMessage
		wire.ReadJSON(&msg, lineBytes, &err)
		if err != nil {
			panic(Fmt("Failed to read cs_msg_log json: %v", err))
		}
		m, ok := msg.Msg.(*types.EventDataRoundState)
		if ok && m.Step == RoundStepNewHeight.String() {
			f.Seek(0, 1)
			// TODO: ensure the height matches
			return true
		}
		return false
	})

	if err != nil {
		return err
	}

	// we found it, now we can replay everything
	pb := newPlayback("", cs.msgLogFP, cs, nil, cs.state.Copy())

	reader := bufio.NewReader(cs.msgLogFP)
	i := 0
	for {
		i += 1
		msgBytes, err := reader.ReadBytes('\n')
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		} else if len(msgBytes) == 0 {
			continue
		}
		// the first msg is the NewHeight event, so we can ignore it
		if i == 1 {
			continue
		}

		// NOTE: since the priv key is set when the msgs are received
		// it will attempt to eg double sign but we can just ignore it
		// since the votes will be replayed and we'll get to the next step
		if err := pb.readReplayMessage(msgBytes); err != nil {
			return err

		}
		if i >= n {
			break
		}
	}
	return nil

}

// replay all msgs or start the console
func (cs *ConsensusState) replay(file string, console bool) error {
	if cs.IsRunning() {
		return errors.New("cs is already running, cannot replay")
	}

	cs.startForReplay()

	// set the FP to nil so we don't overwrite
	if cs.msgLogFP != nil {
		cs.msgLogFP.Close()
		cs.msgLogFP = nil
	}

	// ensure all new step events are regenerated as expected
	newStepCh := cs.evsw.SubscribeToEvent("replay-test", types.EventStringNewRoundStep(), 1)

	fp, err := os.OpenFile(file, os.O_RDONLY, 0666)
	if err != nil {
		return err
	}

	pb := newPlayback(file, fp, cs, newStepCh, cs.state.Copy())
	defer pb.fp.Close()

	var nextN int // apply N msgs in a row
	for pb.scanner.Scan() {
		if nextN == 0 && console {
			nextN = pb.replayConsoleLoop()
		}

		if err := pb.readReplayMessage(pb.scanner.Bytes()); err != nil {
			return err
		}

		if nextN > 0 {
			nextN -= 1
		}
		pb.count += 1
	}
	return nil
}

//------------------------------------------------
// playback manager

type playback struct {
	cs           *ConsensusState
	file         string
	fp           *os.File
	scanner      *bufio.Scanner
	newStepCh    chan interface{}
	genesisState *sm.State
	count        int
}

func newPlayback(file string, fp *os.File, cs *ConsensusState, ch chan interface{}, genState *sm.State) *playback {
	return &playback{
		cs:           cs,
		file:         file,
		newStepCh:    ch,
		genesisState: genState,
		fp:           fp,
		scanner:      bufio.NewScanner(fp),
	}
}

// go back count steps by resetting the state and running (pb.count - count) steps
func (pb *playback) replayReset(count int) error {

	pb.cs.Stop()

	newCs := NewConsensusState(pb.genesisState.Copy(), pb.cs.proxyAppConn, pb.cs.blockStore, pb.cs.mempool)
	newCs.SetEventSwitch(pb.cs.evsw)

	// ensure all new step events are regenerated as expected
	pb.newStepCh = newCs.evsw.SubscribeToEvent("replay-test", types.EventStringNewRoundStep(), 1)

	newCs.startForReplay()

	pb.fp.Close()
	fp, err := os.OpenFile(pb.file, os.O_RDONLY, 0666)
	if err != nil {
		return err
	}
	pb.fp = fp
	pb.scanner = bufio.NewScanner(fp)
	count = pb.count - count
	log.Notice(Fmt("Reseting from %d to %d", pb.count, count))
	pb.count = 0
	pb.cs = newCs
	for i := 0; pb.scanner.Scan() && i < count; i++ {
		if err := pb.readReplayMessage(pb.scanner.Bytes()); err != nil {
			return err
		}
		pb.count += 1
	}
	return nil
}

func (cs *ConsensusState) startForReplay() {
	cs.BaseService.OnStart()
	go cs.receiveRoutine(0)
	// since we replay tocks we just ignore ticks
	go func() {
		for {
			select {
			case <-cs.tickChan:
			case <-cs.Quit:
				return
			}
		}
	}()
}

// console function for parsing input and running commands
func (pb *playback) replayConsoleLoop() int {
	for {
		fmt.Printf("> ")
		bufReader := bufio.NewReader(os.Stdin)
		line, more, err := bufReader.ReadLine()
		if more {
			Exit("input is too long")
		} else if err != nil {
			Exit(err.Error())
		}

		tokens := strings.Split(string(line), " ")
		if len(tokens) == 0 {
			continue
		}

		switch tokens[0] {
		case "next":
			// "next" -> replay next message
			// "next N" -> replay next N messages

			if len(tokens) == 1 {
				return 0
			} else {
				i, err := strconv.Atoi(tokens[1])
				if err != nil {
					fmt.Println("next takes an integer argument")
				} else {
					return i
				}
			}

		case "back":
			// "back" -> go back one message
			// "back N" -> go back N messages

			// NOTE: "back" is not supported in the state machine design,
			// so we restart and replay up to

			if len(tokens) == 1 {
				pb.replayReset(1)
			} else {
				i, err := strconv.Atoi(tokens[1])
				if err != nil {
					fmt.Println("back takes an integer argument")
				} else if i > pb.count {
					fmt.Printf("argument to back must not be larger than the current count (%d)\n", pb.count)
				} else {
					pb.replayReset(i)
				}
			}

		case "rs":
			// "rs" -> print entire round state
			// "rs short" -> print height/round/step
			// "rs <field>" -> print another field of the round state

			rs := pb.cs.RoundState
			if len(tokens) == 1 {
				fmt.Println(rs)
			} else {
				switch tokens[1] {
				case "short":
					fmt.Printf("%v/%v/%v\n", rs.Height, rs.Round, rs.Step)
				case "validators":
					fmt.Println(rs.Validators)
				case "proposal":
					fmt.Println(rs.Proposal)
				case "proposal_block":
					fmt.Printf("%v %v\n", rs.ProposalBlockParts.StringShort(), rs.ProposalBlock.StringShort())
				case "locked_round":
					fmt.Println(rs.LockedRound)
				case "locked_block":
					fmt.Printf("%v %v\n", rs.LockedBlockParts.StringShort(), rs.LockedBlock.StringShort())
				case "votes":
					fmt.Println(rs.Votes.StringIndented("    "))

				default:
					fmt.Println("Unknown option", tokens[1])
				}
			}
		case "n":
			fmt.Println(pb.count)
		}
	}
	return 0
}

func (pb *playback) readReplayMessage(msgBytes []byte) error {
	var err error
	var msg ConsensusLogMessage
	wire.ReadJSON(&msg, msgBytes, &err)
	if err != nil {
		return fmt.Errorf("Error reading json data: %v", err)
	}

	// for logging
	switch m := msg.Msg.(type) {
	case *types.EventDataRoundState:
		log.Notice("New Step", "height", m.Height, "round", m.Round, "step", m.Step)
		// these are playback checks
		ticker := time.After(time.Second * 2)
		if pb.newStepCh != nil {
			select {
			case mi := <-pb.newStepCh:
				m2 := mi.(*types.EventDataRoundState)
				if m.Height != m2.Height || m.Round != m2.Round || m.Step != m2.Step {
					return fmt.Errorf("RoundState mismatch. Got %v; Expected %v", m2, m)
				}
			case <-ticker:
				return fmt.Errorf("Failed to read off newStepCh")
			}
		}
	case msgInfo:
		peerKey := m.PeerKey
		if peerKey == "" {
			peerKey = "local"
		}
		switch msg := m.Msg.(type) {
		case *ProposalMessage:
			p := msg.Proposal
			log.Notice("Proposal", "height", p.Height, "round", p.Round, "header",
				p.BlockPartsHeader, "pol", p.POLRound, "peer", peerKey)
		case *BlockPartMessage:
			log.Notice("BlockPart", "height", msg.Height, "round", msg.Round, "peer", peerKey)
		case *VoteMessage:
			v := msg.Vote
			log.Notice("Vote", "height", v.Height, "round", v.Round, "type", v.Type,
				"hash", v.BlockHash, "header", v.BlockPartsHeader, "peer", peerKey)
		}
		// internal or from peer
		if m.PeerKey == "" {
			pb.cs.internalMsgQueue <- m
		} else {
			pb.cs.peerMsgQueue <- m
		}
	case timeoutInfo:
		log.Notice("Timeout", "height", m.Height, "round", m.Round, "step", m.Step, "dur", m.Duration)
		pb.cs.tockChan <- m
	default:
		return fmt.Errorf("Unknown ConsensusLogMessage type: %v", reflect.TypeOf(msg.Msg))
	}
	return nil
}

// Read lines starting from the end of the file until we read a line that causes found to return true
func SeekFromEndOfFile(f *os.File, found func([]byte) bool) (nLines int, err error) {
	var current int64
	// start at the end
	current, err = f.Seek(0, 2)
	if err != nil {
		fmt.Println("1")
		return
	}

	// backup until we find the the right line
	for {
		current -= 1
		if current < 0 {
			return
		}
		// backup one and read a new byte
		if _, err = f.Seek(current, 0); err != nil {
			fmt.Println("2", current)
			return
		}
		b := make([]byte, 1)
		if _, err = f.Read(b); err != nil {
			return
		}
		if b[0] == '\n' || len(b) == 0 {
			nLines += 1

			// read a full line
			reader := bufio.NewReader(f)
			lineBytes, _ := reader.ReadBytes('\n')
			if len(lineBytes) == 0 {
				continue
			}

			if found(lineBytes) {
				f.Seek(current, 0)
				return
			}
		}
	}
}
