package consensus

import (
	"time"

	auto "github.com/tendermint/go-autofile"
	. "github.com/tendermint/go-common"
	"github.com/tendermint/go-wire"
	"github.com/tendermint/tendermint/types"
)

//--------------------------------------------------------
// types and functions for savings consensus messages

type TimedWALMessage struct {
	Time time.Time  `json:"time"`
	Msg  WALMessage `json:"msg"`
}

type WALMessage interface{}

var _ = wire.RegisterInterface(
	struct{ WALMessage }{},
	wire.ConcreteType{types.EventDataRoundState{}, 0x01},
	wire.ConcreteType{msgInfo{}, 0x02},
	wire.ConcreteType{timeoutInfo{}, 0x03},
)

//--------------------------------------------------------
// Simple write-ahead logger

// Write ahead logger writes msgs to disk before they are processed.
// Can be used for crash-recovery and deterministic replay
// TODO: currently the wal is overwritten during replay catchup
//   give it a mode so it's either reading or appending - must read to end to start appending again
// TODO: #HEIGHT 1 is never printed ...
type WAL struct {
	BaseService

	group *auto.Group
	light bool // ignore block parts
}

func NewWAL(walDir string, light bool) (*WAL, error) {
	head, err := auto.OpenAutoFile(walDir + "/wal")
	if err != nil {
		return nil, err
	}
	group, err := auto.OpenGroup(head)
	if err != nil {
		return nil, err
	}
	wal := &WAL{
		group: group,
		light: light,
	}
	wal.BaseService = *NewBaseService(log, "WAL", wal)
	return wal, nil
}

func (wal *WAL) OnStop() {
	wal.BaseService.OnStop()
	wal.group.Head.Close()
	wal.group.Close()
}

// called in newStep and for each pass in receiveRoutine
func (wal *WAL) Save(wmsg WALMessage) {
	if wal == nil {
		return
	}
	if wal.light {
		// in light mode we only write new steps, timeouts, and our own votes (no proposals, block parts)
		if mi, ok := wmsg.(msgInfo); ok {
			_ = mi
			if mi.PeerKey != "" {
				return
			}
		}
	}
	// Write #HEIGHT: XYZ if new height
	if edrs, ok := wmsg.(types.EventDataRoundState); ok {
		if edrs.Step == RoundStepNewHeight.String() {
			wal.group.WriteLine(Fmt("#HEIGHT: %v", edrs.Height))
		}
	}
	// Write the wal message
	var wmsgBytes = wire.JSONBytes(TimedWALMessage{time.Now(), wmsg})
	err := wal.group.WriteLine(string(wmsgBytes))
	if err != nil {
		PanicQ(Fmt("Error writing msg to consensus wal. Error: %v \n\nMessage: %v", err, wmsg))
	}
}
