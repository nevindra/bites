// The bites WebSocket server of the harness: GET /ws echoes each message as bytes (an Inbox of
// []byte, answered as a binary frame), GET /wsjson decodes each text message into a struct and
// sends the struct back as JSON, the typed path a bites application takes. Any origin is allowed
// (the client sends Origin: http://x).
package main

import (
	"context"
	"flag"
	"io"
	"log/slog"

	"github.com/nevindra/bites"

	"github.com/nevindra/bites/bench/common"
)

func main() {
	flag.Parse()
	common.StartStats()
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), bites.WithAllowAnyOrigin())
	app.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[[]byte]) (bites.Inbox[[]byte], error) {
		return bites.Inbox[[]byte]{OnMessage: func(_ context.Context, m []byte) error { return s.Send(m) }}, nil
	})
	app.WebSocket("/wsjson", func(_ context.Context, _ struct{}, s *bites.Socket[common.Said]) (bites.Inbox[common.Said], error) {
		return bites.Inbox[common.Said]{OnMessage: func(_ context.Context, m common.Said) error { return s.Send(m) }}, nil
	})
	if err := app.Listen(*common.Addr); err != nil {
		panic(err)
	}
}
