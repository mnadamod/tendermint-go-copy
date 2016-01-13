package core

import (
	"github.com/tendermint/go-events"
	"github.com/tendermint/go-rpc/types"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
)

func Subscribe(wsCtx rpctypes.WSRPCContext, event string) (*ctypes.ResultSubscribe, error) {
	log.Notice("Subscribe to event", "remote", wsCtx.GetRemoteAddr(), "event", event)
	wsCtx.GetEventSwitch().AddListenerForEvent(wsCtx.GetRemoteAddr(), event, func(msg events.EventData) {
		// NOTE: EventSwitch callbacks must be nonblocking
		// NOTE: RPCResponses of subscribed events have id suffix "#event"
		wsCtx.TryWriteRPCResponse(rpctypes.NewRPCResponse(wsCtx.Request.ID+"#event", &events.EventResult{event, msg}, ""))
	})
	return &ctypes.ResultSubscribe{}, nil
}

func Unsubscribe(wsCtx rpctypes.WSRPCContext, event string) (*ctypes.ResultUnsubscribe, error) {
	log.Notice("Unsubscribe to event", "remote", wsCtx.GetRemoteAddr(), "event", event)
	wsCtx.GetEventSwitch().AddListenerForEvent(wsCtx.GetRemoteAddr(), event, func(msg events.EventData) {
		// NOTE: EventSwitch callbacks must be nonblocking
		// NOTE: RPCResponses of subscribed events have id suffix "#event"
		wsCtx.TryWriteRPCResponse(rpctypes.NewRPCResponse(wsCtx.Request.ID+"#event", &events.EventResult{event, msg}, ""))
	})
	return &ctypes.ResultUnsubscribe{}, nil
}
