package counter

import (
	"encoding/binary"
	"fmt"

	"github.com/tendermint/abci/types"
)

type CounterApplication struct {
	hashCount int
	txCount   int
	serial    bool
}

func NewCounterApplication(serial bool) *CounterApplication {
	return &CounterApplication{serial: serial}
}

func (app *CounterApplication) Info() types.ResponseInfo {
	return types.ResponseInfo{Data: fmt.Sprintf("{\"hashes\":%v,\"txs\":%v}", app.hashCount, app.txCount)}
}

func (app *CounterApplication) SetOption(key string, value string) (log string) {
	if key == "serial" && value == "on" {
		app.serial = true
	}
	return ""
}

func (app *CounterApplication) DeliverTx(tx []byte) types.Result {
	if app.serial {
		if len(tx) > 8 {
			return types.ErrEncodingError.SetLog(fmt.Sprintf("Max tx size is 8 bytes, got %d", len(tx)))
		}
		tx8 := make([]byte, 8)
		copy(tx8[len(tx8)-len(tx):], tx)
		txValue := binary.BigEndian.Uint64(tx8)
		if txValue != uint64(app.txCount) {
			return types.ErrBadNonce.SetLog(fmt.Sprintf("Invalid nonce. Expected %v, got %v", app.txCount, txValue))
		}
	}
	app.txCount++
	return types.OK
}

func (app *CounterApplication) CheckTx(tx []byte) types.Result {
	if app.serial {
		if len(tx) > 8 {
			return types.ErrEncodingError.SetLog(fmt.Sprintf("Max tx size is 8 bytes, got %d", len(tx)))
		}
		tx8 := make([]byte, 8)
		copy(tx8[len(tx8)-len(tx):], tx)
		txValue := binary.BigEndian.Uint64(tx8)
		if txValue < uint64(app.txCount) {
			return types.ErrBadNonce.SetLog(fmt.Sprintf("Invalid nonce. Expected >= %v, got %v", app.txCount, txValue))
		}
	}
	return types.OK
}

func (app *CounterApplication) Commit() types.Result {
	app.hashCount++
	if app.txCount == 0 {
		return types.OK
	}
	hash := make([]byte, 8)
	binary.BigEndian.PutUint64(hash, uint64(app.txCount))
	return types.NewResultOK(hash, "")
}

func (app *CounterApplication) Query(query []byte) types.Result {
	queryStr := string(query)

	switch queryStr {
	case "hash":
		return types.NewResultOK(nil, fmt.Sprintf("%v", app.hashCount))
	case "tx":
		return types.NewResultOK(nil, fmt.Sprintf("%v", app.txCount))
	}

	return types.ErrUnknownRequest.SetLog(fmt.Sprintf("Invalid nonce. Expected hash or tx, got %v", queryStr))
}
