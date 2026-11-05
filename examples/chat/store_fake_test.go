package main

import (
	"context"
	"sync"
	"time"
)

// fakeStore is the Store in memory.
type fakeStore struct {
	mu    sync.Mutex
	rooms []Room
	msgs  []Message
	fail  error // returned by every call when set
}

func (f *fakeStore) CreateRoom(ctx context.Context, name string) (Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return Room{}, f.fail
	}
	for _, r := range f.rooms {
		if r.Name == name {
			return Room{}, ErrRoomExists
		}
	}
	r := Room{ID: int64(len(f.rooms) + 1), Name: name, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	f.rooms = append(f.rooms, r)
	return r, nil
}

func (f *fakeStore) ListRooms(ctx context.Context, after int64, limit int) ([]Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	var out []Room
	for _, r := range f.rooms {
		if r.ID > after && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) RoomExists(ctx context.Context, id int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return id >= 1 && id <= int64(len(f.rooms)), f.fail
}

func (f *fakeStore) PostMessage(ctx context.Context, room int64, author, text string) (Message, error) {
	f.mu.Lock()
	if f.fail != nil {
		f.mu.Unlock()
		return Message{}, f.fail
	}
	if room < 1 || room > int64(len(f.rooms)) {
		f.mu.Unlock()
		return Message{}, ErrNoRoom
	}
	m := Message{ID: int64(len(f.msgs) + 1), RoomID: room, Author: author, Text: text,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	f.msgs = append(f.msgs, m)
	f.mu.Unlock()
	return m, nil
}

func (f *fakeStore) History(ctx context.Context, room int64, before int64, limit int) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	var out []Message
	for i := len(f.msgs) - 1; i >= 0 && len(out) < limit; i-- {
		m := f.msgs[i]
		if m.RoomID == room && (before == 0 || m.ID < before) {
			out = append(out, m)
		}
	}
	return out, nil
}
