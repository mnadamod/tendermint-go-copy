package example

import (
	"reflect"
	"testing"
	"time"

	. "github.com/tendermint/go-common"
	"github.com/tendermint/go-wire"
	"github.com/tendermint/tmsp/server"
	"github.com/tendermint/tmsp/types"
)

func TestStream(t *testing.T) {

	numAppendTxs := 200000

	// Start the listener
	_, err := server.StartListener("tcp://127.0.0.1:46658", NewDummyApplication())
	if err != nil {
		Exit(err.Error())
	}

	// Connect to the socket
	conn, err := Connect("tcp://127.0.0.1:46658")
	if err != nil {
		Exit(err.Error())
	}

	// Read response data
	done := make(chan struct{})
	go func() {
		counter := 0
		for {
			var n int
			var err error
			var res types.Response
			wire.ReadVarint(conn, &n, &err) // ignore
			wire.ReadBinaryPtr(&res, conn, 0, &n, &err)
			if err != nil {
				Exit(err.Error())
			}

			// Process response
			switch res := res.(type) {
			case types.ResponseAppendTx:
				counter += 1
				if res.RetCode != types.RetCodeOK {
					t.Error("AppendTx failed with ret_code", res.RetCode)
				}
				if counter > numAppendTxs {
					t.Fatal("Too many AppendTx responses")
				}
				t.Log("response", counter)
				if counter == numAppendTxs {
					go func() {
						time.Sleep(time.Second * 2) // Wait for a bit to allow counter overflow
						close(done)
					}()
				}
			case types.ResponseFlush:
				// ignore
			default:
				t.Error("Unexpected response type", reflect.TypeOf(res))
			}
		}
	}()

	// Write requests
	for counter := 0; counter < numAppendTxs; counter++ {
		// Send request
		var n int
		var err error
		var req types.Request = types.RequestAppendTx{TxBytes: []byte("test")}
		wire.WriteBinaryLengthPrefixed(struct{ types.Request }{req}, conn, &n, &err)
		if err != nil {
			t.Fatal(err.Error())
		}

		// Sometimes send flush messages
		if counter%123 == 0 {
			t.Log("flush")
			wire.WriteBinaryLengthPrefixed(struct{ types.Request }{types.RequestFlush{}}, conn, &n, &err)
			if err != nil {
				t.Fatal(err.Error())
			}
		}
	}

	// Send final flush message
	var n int
	wire.WriteBinaryLengthPrefixed(struct{ types.Request }{types.RequestFlush{}}, conn, &n, &err)
	if err != nil {
		t.Fatal(err.Error())
	}

	<-done
}
