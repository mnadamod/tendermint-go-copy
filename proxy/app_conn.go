package proxy

import (
	tmspcli "github.com/tendermint/tmsp/client"
)

type AppConn interface {
	SetResponseCallback(tmspcli.Callback)
	Error() error

	EchoAsync(msg string) *tmspcli.ReqRes
	FlushAsync() *tmspcli.ReqRes
	AppendTxAsync(tx []byte) *tmspcli.ReqRes
	CheckTxAsync(tx []byte) *tmspcli.ReqRes
	CommitAsync() *tmspcli.ReqRes
	SetOptionAsync(key string, value string) *tmspcli.ReqRes

	InfoSync() (info string, err error)
	FlushSync() error
	CommitSync() (hash []byte, log string, err error)
}
