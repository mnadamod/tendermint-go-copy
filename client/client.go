package tmspcli

import (
	"bufio"
	"container/list"
	"errors"
	"fmt"
	"net"
	"sync"

	. "github.com/tendermint/go-common"
	"github.com/tendermint/tmsp/types"
)

const reqQueueSize = 256        // TODO make configurable
const maxResponseSize = 1048576 // 1MB TODO make configurable
const flushThrottleMS = 20      // Don't wait longer than...

type Callback func(*types.Request, *types.Response)

// This is goroutine-safe, but users should beware that
// the application in general is not meant to be interfaced
// with concurrent callers.
type Client struct {
	QuitService
	sync.Mutex // [EB]: is this even used?

	reqQueue   chan *ReqRes
	flushTimer *ThrottleTimer

	mtx       sync.Mutex
	addr      string
	conn      net.Conn
	bufWriter *bufio.Writer
	err       error
	reqSent   *list.List
	resCb     func(*types.Request, *types.Response) // listens to all callbacks
}

func NewClient(addr string) (*Client, error) {
	conn, err := Connect(addr)
	if err != nil {
		return nil, err
	}
	cli := &Client{
		reqQueue:   make(chan *ReqRes, reqQueueSize),
		flushTimer: NewThrottleTimer("Client", flushThrottleMS),

		conn:      conn,
		bufWriter: bufio.NewWriter(conn),
		reqSent:   list.New(),
		resCb:     nil,
	}
	cli.QuitService = *NewQuitService(nil, "Client", cli)
	cli.Start() // Just start it, it's confusing for callers to remember to start.
	return cli, nil
}

func (cli *Client) OnStart() error {
	cli.QuitService.OnStart()
	go cli.sendRequestsRoutine()
	go cli.recvResponseRoutine()
	return nil
}

func (cli *Client) OnStop() {
	cli.QuitService.OnStop()
	cli.conn.Close()
}

// Set listener for all responses
// NOTE: callback may get internally generated flush responses.
func (cli *Client) SetResponseCallback(resCb Callback) {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	cli.resCb = resCb
}

func (cli *Client) StopForError(err error) {
	cli.mtx.Lock()
	// log.Error("Stopping Client for error.", "error", err)
	if cli.err == nil {
		cli.err = err
	}
	cli.mtx.Unlock()
	cli.Stop()
}

func (cli *Client) Error() error {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	return cli.err
}

//----------------------------------------

func (cli *Client) sendRequestsRoutine() {
	for {
		select {
		case <-cli.flushTimer.Ch:
			select {
			case cli.reqQueue <- NewReqRes(types.RequestFlush()):
			default:
				// Probably will fill the buffer, or retry later.
			}
		case <-cli.QuitService.Quit:
			return
		case reqres := <-cli.reqQueue:
			cli.willSendReq(reqres)
			err := types.WriteMessage(reqres.Request, cli.bufWriter)
			if err != nil {
				cli.StopForError(err)
				return
			}
			// log.Debug("Sent request", "requestType", reflect.TypeOf(reqres.Request), "request", reqres.Request)
			if reqres.Request.Type == types.MessageType_Flush {
				err = cli.bufWriter.Flush()
				if err != nil {
					cli.StopForError(err)
					return
				}
			}
		}
	}
}

func (cli *Client) recvResponseRoutine() {
	r := bufio.NewReader(cli.conn) // Buffer reads
	for {
		var res = &types.Response{}
		err := types.ReadMessage(r, res)
		if err != nil {
			cli.StopForError(err)
			return
		}
		switch res.Type {
		case types.MessageType_Exception:
			// XXX After setting cli.err, release waiters (e.g. reqres.Done())
			cli.StopForError(errors.New(res.Error))
		default:
			// log.Debug("Received response", "responseType", reflect.TypeOf(res), "response", res)
			err := cli.didRecvResponse(res)
			if err != nil {
				cli.StopForError(err)
			}
		}
	}
}

func (cli *Client) willSendReq(reqres *ReqRes) {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()
	cli.reqSent.PushBack(reqres)
}

func (cli *Client) didRecvResponse(res *types.Response) error {
	cli.mtx.Lock()
	defer cli.mtx.Unlock()

	// Get the first ReqRes
	next := cli.reqSent.Front()
	if next == nil {
		return fmt.Errorf("Unexpected result type %v when nothing expected", res.Type)
	}
	reqres := next.Value.(*ReqRes)
	if !resMatchesReq(reqres.Request, res) {
		return fmt.Errorf("Unexpected result type %v when response to %v expected",
			res.Type, reqres.Request.Type)
	}

	reqres.Response = res    // Set response
	reqres.Done()            // Release waiters
	cli.reqSent.Remove(next) // Pop first item from linked list

	// Notify reqRes listener if set
	if cb := reqres.GetCallback(); cb != nil {
		cb(res)
	}

	// Notify client listener if set
	if cli.resCb != nil {
		cli.resCb(reqres.Request, res)
	}

	return nil
}

//----------------------------------------

func (cli *Client) EchoAsync(msg string) *ReqRes {
	return cli.queueRequest(types.RequestEcho(msg))
}

func (cli *Client) FlushAsync() *ReqRes {
	return cli.queueRequest(types.RequestFlush())
}

func (cli *Client) SetOptionAsync(key string, value string) *ReqRes {
	return cli.queueRequest(types.RequestSetOption(key, value))
}

func (cli *Client) AppendTxAsync(tx []byte) *ReqRes {
	return cli.queueRequest(types.RequestAppendTx(tx))
}

func (cli *Client) CheckTxAsync(tx []byte) *ReqRes {
	return cli.queueRequest(types.RequestCheckTx(tx))
}

func (cli *Client) CommitAsync() *ReqRes {
	return cli.queueRequest(types.RequestCommit())
}

func (cli *Client) QueryAsync(query []byte) *ReqRes {
	return cli.queueRequest(types.RequestQuery(query))
}

//----------------------------------------

func (cli *Client) FlushSync() error {
	cli.queueRequest(types.RequestFlush()).Wait()
	return cli.err
}

func (cli *Client) InfoSync() (info string, err error) {
	reqres := cli.queueRequest(types.RequestInfo())
	cli.FlushSync()
	if cli.err != nil {
		return "", cli.err
	}
	return string(reqres.Response.Data), nil
}

func (cli *Client) SetOptionSync(key string, value string) (log string, err error) {
	reqres := cli.queueRequest(types.RequestSetOption(key, value))
	cli.FlushSync()
	if cli.err != nil {
		return "", cli.err
	}
	return reqres.Response.Log, nil
}

func (cli *Client) AppendTxSync(tx []byte) (code types.CodeType, result []byte, log string, err error) {
	reqres := cli.queueRequest(types.RequestAppendTx(tx))
	cli.FlushSync()
	if cli.err != nil {
		return types.CodeType_InternalError, nil, "", cli.err
	}
	res := reqres.Response
	return res.Code, res.Data, res.Log, nil
}

func (cli *Client) CheckTxSync(tx []byte) (code types.CodeType, result []byte, log string, err error) {
	reqres := cli.queueRequest(types.RequestCheckTx(tx))
	cli.FlushSync()
	if cli.err != nil {
		return types.CodeType_InternalError, nil, "", cli.err
	}
	res := reqres.Response
	return res.Code, res.Data, res.Log, nil
}

func (cli *Client) CommitSync() (hash []byte, log string, err error) {
	reqres := cli.queueRequest(types.RequestCommit())
	cli.FlushSync()
	if cli.err != nil {
		return nil, "", cli.err
	}
	res := reqres.Response
	return res.Data, res.Log, nil
}

func (cli *Client) QuerySync(query []byte) (code types.CodeType, result []byte, log string, err error) {
	reqres := cli.queueRequest(types.RequestQuery(query))
	cli.FlushSync()
	if cli.err != nil {
		return types.CodeType_InternalError, nil, "", cli.err
	}
	res := reqres.Response
	return res.Code, res.Data, res.Log, nil
}

//----------------------------------------

func (cli *Client) queueRequest(req *types.Request) *ReqRes {
	reqres := NewReqRes(req)
	// TODO: set cli.err if reqQueue times out
	cli.reqQueue <- reqres

	// Maybe auto-flush, or unset auto-flush
	switch req.Type {
	case types.MessageType_Flush:
		cli.flushTimer.Unset()
	default:
		cli.flushTimer.Set()
	}

	return reqres
}

//----------------------------------------

func resMatchesReq(req *types.Request, res *types.Response) (ok bool) {
	return req.Type == res.Type
}

type ReqRes struct {
	*types.Request
	*sync.WaitGroup
	*types.Response // Not set atomically, so be sure to use WaitGroup.

	mtx  sync.Mutex
	done bool                  // Gets set to true once *after* WaitGroup.Done().
	cb   func(*types.Response) // A single callback that may be set.
}

func NewReqRes(req *types.Request) *ReqRes {
	return &ReqRes{
		Request:   req,
		WaitGroup: waitGroup1(),
		Response:  nil,

		done: false,
		cb:   nil,
	}
}

// Sets the callback for this ReqRes atomically.
// If reqRes is already done, calls cb immediately.
// NOTE: reqRes.cb should not change if reqRes.done.
// NOTE: only one callback is supported.
func (reqRes *ReqRes) SetCallback(cb func(res *types.Response)) {
	reqRes.mtx.Lock()

	if reqRes.done {
		reqRes.mtx.Unlock()
		cb(reqRes.Response)
		return
	}

	defer reqRes.mtx.Unlock()
	reqRes.cb = cb
}

func (reqRes *ReqRes) GetCallback() func(*types.Response) {
	reqRes.mtx.Lock()
	defer reqRes.mtx.Unlock()
	return reqRes.cb
}

// NOTE: it should be safe to read reqRes.cb without locks after this.
func (reqRes *ReqRes) SetDone() {
	reqRes.mtx.Lock()
	reqRes.done = true
	reqRes.mtx.Unlock()
}

func waitGroup1() (wg *sync.WaitGroup) {
	wg = &sync.WaitGroup{}
	wg.Add(1)
	return
}
