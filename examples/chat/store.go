package main

import (
	"context"
	"errors"
	"time"
)

// Room is a place messages are posted in. The json tags are its wire names, in the answers and in
// the OpenAPI document.
type Room struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Message is one line said in a room. The same shape is the answer of the HTTP routes, the payload
// of the notification and what a WebSocket client receives.
type Message struct {
	ID        int64     `json:"id"`
	RoomID    int64     `json:"room_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	// ErrRoomExists is CreateRoom's answer for a name that is taken.
	ErrRoomExists = errors.New("chat: a room with that name exists")
	// ErrNoRoom is PostMessage's answer for a room that does not exist.
	ErrNoRoom = errors.New("chat: no such room")
)

// Store is what the handlers need from the database. The handlers know this interface and not
// pgx, so the unit tests run against a fake and need no database (see store_fake_test.go).
type Store interface {
	CreateRoom(ctx context.Context, name string) (Room, error)
	// ListRooms returns up to limit rooms with an id above after, in id order.
	ListRooms(ctx context.Context, after int64, limit int) ([]Room, error)
	RoomExists(ctx context.Context, id int64) (bool, error)
	// PostMessage stores a message. Its delivery to the room's sockets is not the store's call:
	// the handler publishes it to the Hub once it is stored.
	PostMessage(ctx context.Context, room int64, author, text string) (Message, error)
	// History returns up to limit messages of a room, newest first. A before above zero keeps only
	// messages with a lower id.
	History(ctx context.Context, room int64, before int64, limit int) ([]Message, error)
}
