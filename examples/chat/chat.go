package main

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/nevindra/bites"
)

// A type says its length once in a Length method; decoding checks it and the OpenAPI document
// states it.
type (
	roomName    string
	messageText string
)

func (roomName) Length() (min, max int) { return 3, 40 }

// A text is at most 1000 characters so a notification stays under Postgres's limit of 7999 bytes
// (4 bytes a character, 6 when JSON escapes it, is 6000 at most).
func (messageText) Length() (min, max int) { return 1, 1000 }

const (
	defaultPage = 20
	maxPage     = 100
)

// Chat owns the Store and the Hub; its methods are the handlers, registered as method values.
type Chat struct {
	store  Store
	hub    *Hub
	prefix string // the path the clients see in front of the app's own, from CHAT_PUBLIC_URL
}

// RoomPage and MessagePage are a page of a list and, when there is more, the cursor of the next.
type RoomPage struct {
	Items []Room `json:"items"`
	Next  *int64 `json:"next,omitempty"`
}

type MessagePage struct {
	Items []Message `json:"items"`
	Next  *int64    `json:"next,omitempty"`
}

// ---- rooms

type CreateRoom struct {
	User User
	Body struct {
		Name roomName
	}
}

func (c *Chat) CreateRoom(ctx context.Context, in CreateRoom) (bites.Created[Room], error) {
	room, err := c.store.CreateRoom(ctx, string(in.Body.Name))
	if errors.Is(err, ErrRoomExists) {
		return bites.Created[Room]{}, bites.Conflict("a room with that name exists")
	}
	if err != nil {
		return bites.Created[Room]{}, err // a 500: the reason is logged and never sent
	}
	// The Location is a path the client follows, so it carries the prefix a proxy in front strips
	// (ADR 0025): the app is told it by CHAT_PUBLIC_URL rather than by a header.
	return bites.Created[Room]{Body: room, Location: c.prefix + "/rooms/" + itoa(room.ID)}, nil
}

type ListRooms struct {
	Query struct {
		After bites.Opt[bites.PositiveInt64]
		Limit bites.Opt[bites.PositiveInt]
	}
}

func (l ListRooms) Validate() error {
	if l.Query.Limit.Or(defaultPage) > maxPage {
		return bites.Invalid("query.limit", "at most 100")
	}
	return nil
}

func (c *Chat) ListRooms(ctx context.Context, in ListRooms) (RoomPage, error) {
	limit := int(in.Query.Limit.Or(defaultPage))
	rooms, err := c.store.ListRooms(ctx, int64(in.Query.After.Or(0)), limit+1)
	if err != nil {
		return RoomPage{}, err
	}
	page := RoomPage{Items: rooms}
	if len(rooms) > limit { // one more than asked for says there is a next page
		page.Items = rooms[:limit]
		page.Next = &page.Items[limit-1].ID
	}
	if page.Items == nil {
		page.Items = []Room{}
	}
	return page, nil
}

// ---- messages

type PostMessage struct {
	User User
	Path struct{ ID int64 }
	Body struct {
		Text messageText
	}
}

// Validate refuses a text of spaces, which the length alone lets through.
func (p PostMessage) Validate() error {
	if strings.TrimSpace(string(p.Body.Text)) == "" {
		return bites.Invalid("body.text", "must not be blank")
	}
	return nil
}

func (c *Chat) PostMessage(ctx context.Context, in PostMessage) (bites.Created[Message], error) {
	m, err := c.store.PostMessage(ctx, in.Path.ID, in.User.Name, string(in.Body.Text))
	if errors.Is(err, ErrNoRoom) {
		return bites.Created[Message]{}, bites.NotFound("no such room")
	}
	if err != nil {
		return bites.Created[Message]{}, err
	}
	// The sockets get the message through the broker, this instance's too: one path delivers.
	c.hub.Publish(m)
	return bites.Created[Message]{Body: m}, nil
}

type ReadHistory struct {
	Path  struct{ ID int64 }
	Query struct {
		Before bites.Opt[bites.PositiveInt64]
		Limit  bites.Opt[bites.PositiveInt]
	}
}

func (r ReadHistory) Validate() error {
	if r.Query.Limit.Or(defaultPage) > maxPage {
		return bites.Invalid("query.limit", "at most 100")
	}
	return nil
}

func (c *Chat) History(ctx context.Context, in ReadHistory) (MessagePage, error) {
	if ok, err := c.store.RoomExists(ctx, in.Path.ID); err != nil {
		return MessagePage{}, err
	} else if !ok {
		return MessagePage{}, bites.NotFound("no such room")
	}
	limit := int(in.Query.Limit.Or(defaultPage))
	msgs, err := c.store.History(ctx, in.Path.ID, int64(in.Query.Before.Or(0)), limit+1)
	if err != nil {
		return MessagePage{}, err
	}
	page := MessagePage{Items: msgs}
	if len(msgs) > limit {
		page.Items = msgs[:limit]
		page.Next = &page.Items[limit-1].ID
	}
	if page.Items == nil {
		page.Items = []Message{}
	}
	return page, nil
}

// ---- the socket

// Say is what a client sends on a socket.
type Say struct {
	Text string `json:"text"`
}

type JoinRoom struct {
	User User
	Path struct{ ID int64 }
}

// Join runs after the Providers and before the 101: a caller with no token is a 401 and a room
// that does not exist a 404, both as ordinary problem+json, and neither opens a socket.
func (c *Chat) Join(ctx context.Context, in JoinRoom, s *bites.Socket[Frame]) (bites.Inbox[Say], error) {
	ok, err := c.store.RoomExists(ctx, in.Path.ID)
	if err != nil {
		return bites.Inbox[Say]{}, err
	}
	if !ok {
		return bites.Inbox[Say]{}, bites.NotFound("no such room")
	}
	room, author := in.Path.ID, in.User.Name
	if err := c.hub.Join(room, s); err != nil {
		return bites.Inbox[Say]{}, err
	}
	return bites.Inbox[Say]{
		// OnMessage stores the message and announces it. The sender sees it come back through
		// the broker like every other socket, so there is one path from a message to a socket.
		OnMessage: func(ctx context.Context, say Say) error {
			if n := utf8.RuneCountInString(say.Text); n < 1 || n > 1000 || strings.TrimSpace(say.Text) == "" {
				return s.Send(Frame{Kind: "error", Error: "text must be 1 to 1000 characters and not blank"})
			}
			m, err := c.store.PostMessage(ctx, room, author, say.Text)
			if errors.Is(err, ErrNoRoom) {
				return &bites.CloseError{Status: 4404, Reason: "the room is gone"}
			}
			if err == nil {
				c.hub.Publish(m)
			}
			return err // closes the socket with 1011; the reason is not sent
		},
		OnMalformed: func(ctx context.Context, raw []byte, err error) error {
			return s.Send(Frame{Kind: "error", Error: "send {\"text\": \"...\"}"})
		},
	}, nil
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = digits[n%10]
	}
	return string(b[i:])
}
