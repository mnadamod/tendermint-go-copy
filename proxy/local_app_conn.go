package proxy

import (
	tmspcli "github.com/tendermint/tmsp/client"
	tmsp "github.com/tendermint/tmsp/types"
	"sync"
)

type localAppConn struct {
	mtx *sync.Mutex
	tmsp.Application
	tmspcli.Callback
}

func NewLocalAppConn(mtx *sync.Mutex, app tmsp.Application) *localAppConn {
	return &localAppConn{
		mtx:         mtx,
		Application: app,
	}
}

func (app *localAppConn) SetResponseCallback(cb tmspcli.Callback) {
	app.mtx.Lock()
	defer app.mtx.Unlock()
	app.Callback = cb
}

// TODO: change tmsp.Application to include Error()?
func (app *localAppConn) Error() error {
	return nil
}

func (app *localAppConn) EchoAsync(msg string) *tmspcli.ReqRes {
	return app.callback(
		tmsp.RequestEcho(msg),
		tmsp.ResponseEcho(msg),
	)
}

func (app *localAppConn) FlushAsync() *tmspcli.ReqRes {
	// Do nothing
	return NewReqRes(tmsp.RequestFlush(), nil)
}

func (app *localAppConn) SetOptionAsync(key string, value string) *tmspcli.ReqRes {
	app.mtx.Lock()
	log := app.Application.SetOption(key, value)
	app.mtx.Unlock()
	return app.callback(
		tmsp.RequestSetOption(key, value),
		tmsp.ResponseSetOption(log),
	)
}

func (app *localAppConn) AppendTxAsync(tx []byte) *tmspcli.ReqRes {
	app.mtx.Lock()
	code, result, log := app.Application.AppendTx(tx)
	app.mtx.Unlock()
	return app.callback(
		tmsp.RequestAppendTx(tx),
		tmsp.ResponseAppendTx(code, result, log),
	)
}

func (app *localAppConn) CheckTxAsync(tx []byte) *tmspcli.ReqRes {
	app.mtx.Lock()
	code, result, log := app.Application.CheckTx(tx)
	app.mtx.Unlock()
	return app.callback(
		tmsp.RequestCheckTx(tx),
		tmsp.ResponseCheckTx(code, result, log),
	)
}

func (app *localAppConn) CommitAsync() *tmspcli.ReqRes {
	app.mtx.Lock()
	hash, log := app.Application.Commit()
	app.mtx.Unlock()
	return app.callback(
		tmsp.RequestCommit(),
		tmsp.ResponseCommit(hash, log),
	)
}

func (app *localAppConn) InfoSync() (info string, err error) {
	app.mtx.Lock()
	info = app.Application.Info()
	app.mtx.Unlock()
	return info, nil
}

func (app *localAppConn) FlushSync() error {
	return nil
}

func (app *localAppConn) CommitSync() (hash []byte, log string, err error) {
	app.mtx.Lock()
	hash, log = app.Application.Commit()
	app.mtx.Unlock()
	return hash, log, nil
}

func (app *localAppConn) InitChainSync(validators []*tmsp.Validator) (err error) {
	app.mtx.Lock()
	if bcApp, ok := app.Application.(tmsp.BlockchainAware); ok {
		bcApp.InitChain(validators)
	}
	app.mtx.Unlock()
	return nil
}

func (app *localAppConn) BeginBlockSync(height uint64) (err error) {
	app.mtx.Lock()
	if bcApp, ok := app.Application.(tmsp.BlockchainAware); ok {
		bcApp.BeginBlock(height)
	}
	app.mtx.Unlock()
	return nil
}

func (app *localAppConn) EndBlockSync() (changedValidators []*tmsp.Validator, err error) {
	app.mtx.Lock()
	if bcApp, ok := app.Application.(tmsp.BlockchainAware); ok {
		changedValidators = bcApp.EndBlock()
	}
	app.mtx.Unlock()
	return changedValidators, nil
}

//-------------------------------------------------------

func (app *localAppConn) callback(req *tmsp.Request, res *tmsp.Response) *tmspcli.ReqRes {
	app.Callback(req, res)
	return NewReqRes(req, res)
}

func NewReqRes(req *tmsp.Request, res *tmsp.Response) *tmspcli.ReqRes {
	reqRes := tmspcli.NewReqRes(req)
	reqRes.Response = res
	reqRes.SetDone()
	return reqRes
}
