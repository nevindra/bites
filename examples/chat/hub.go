package main

import (
	"log/slog"

	"github.com/nevindra/bites"
)

// Frame is what the server sends on a socket: a message, a request to reload, or an error about
// what the client sent.
type Frame struct {
	Kind    string   `json:"kind"` // "message", "resync" or "error"
	Message *Message `json:"message,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Hub is the process's rooms: a Topic a room, a key of one Topics, made when its first socket
// joins and dropped when it is empty (ADR 0084). The family publishes through the Broker
// (ADR 0081), so a message posted on any instance reaches the sockets of every instance, by one
// path, the poster's own included, whether or not this instance has a socket in the room.
type Hub struct {
	log   *slog.Logger
	rooms *bites.Topics[Frame]
}

// NewHub returns an empty Hub whose rooms publish through broker. When the broker reports that
// notifications may have been missed (the LISTEN connection dropped and came back), the sockets of
// each room on this instance are told to read the history again: a message committed meanwhile is
// there, and the live path cannot replay it.
func NewHub(log *slog.Logger, broker bites.Broker) *Hub {
	h := &Hub{log: log}
	h.rooms = bites.NewTopics[Frame]("room.", broker, bites.TopicsOptions{
		Log:   log,
		OnGap: func(room string) { h.rooms.PublishLocal(room, Frame{Kind: "resync"}) },
	})
	return h
}

// Join subscribes the socket to the room. The socket leaves the room by itself when it closes.
func (h *Hub) Join(room int64, s *bites.Socket[Frame]) error {
	return h.rooms.Subscribe(itoa(room), s)
}

// Publish announces a stored message to its room, on every instance.
func (h *Hub) Publish(m Message) {
	// The message is stored, so a failed publish is not the poster's failure: the history has it.
	if err := h.rooms.Publish(itoa(m.RoomID), Frame{Kind: "message", Message: &m}); err != nil {
		h.log.Warn("chat: the message is stored but was not announced to the sockets", "room", m.RoomID, "err", err)
	}
}

// Rooms is the number of rooms with a socket on this instance, for tests and for a gauge one day.
func (h *Hub) Rooms() int { return h.rooms.Keys() }
