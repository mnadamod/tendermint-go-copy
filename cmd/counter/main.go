package main

import (
	"flag"
	stdlog "log"
	"os"

	"github.com/tendermint/abci/example/counter"
	"github.com/tendermint/abci/server"
	cmn "github.com/tendermint/tmlibs/common"
	"github.com/tendermint/tmlibs/log"
)

func main() {

	addrPtr := flag.String("addr", "tcp://0.0.0.0:46658", "Listen address")
	abciPtr := flag.String("abci", "socket", "ABCI server: socket | grpc")
	serialPtr := flag.Bool("serial", false, "Enforce incrementing (serial) txs")
	flag.Parse()
	app := counter.NewCounterApplication(*serialPtr)

	// Start the listener
	srv, err := server.NewServer(*addrPtr, *abciPtr, app)
	if err != nil {
		stdlog.Fatal(err.Error())
	}
	logger := log.NewTmLogger(os.Stdout)
	srv.SetLogger(log.With(logger, "module", "abci-server"))

	// Wait forever
	cmn.TrapSignal(func() {
		// Cleanup
		srv.Stop()
	})

}
