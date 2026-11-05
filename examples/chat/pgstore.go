package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// PGStore is the Store over a pgx pool. It is the struct that owns the database handle (ADR 0014):
// the handlers hold it behind the Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects, applies the schema and returns the store.
func OpenPostgres(ctx context.Context, url string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("chat: database url: %w", err)
	}
	s := &PGStore{pool: pool}
	if err := s.applySchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the pool.
func (s *PGStore) Close() { s.pool.Close() }

// Ping reports whether the database answers.
func (s *PGStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *PGStore) applySchema(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("chat: connect to the database: %w", err)
	}
	defer tx.Rollback(ctx)
	// The lock is released with the transaction. 7243 is an arbitrary number of this example.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7243)"); err != nil {
		return fmt.Errorf("chat: lock for the schema: %w", err)
	}
	if _, err := tx.Exec(ctx, schema); err != nil {
		return fmt.Errorf("chat: apply the schema: %w", err)
	}
	return tx.Commit(ctx)
}

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func (s *PGStore) CreateRoom(ctx context.Context, name string) (Room, error) {
	var r Room
	err := s.pool.QueryRow(ctx,
		`INSERT INTO rooms (name) VALUES ($1) RETURNING id, name, created_at`, name).
		Scan(&r.ID, &r.Name, &r.CreatedAt)
	if pgCode(err) == "23505" { // unique_violation
		return Room{}, ErrRoomExists
	}
	return r, err
}

func (s *PGStore) ListRooms(ctx context.Context, after int64, limit int) ([]Room, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, created_at FROM rooms WHERE id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Room, error) {
		var r Room
		err := row.Scan(&r.ID, &r.Name, &r.CreatedAt)
		return r, err
	})
}

func (s *PGStore) RoomExists(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rooms WHERE id = $1)`, id).Scan(&ok)
	return ok, err
}

func (s *PGStore) PostMessage(ctx context.Context, room int64, author, text string) (Message, error) {
	var m Message
	err := s.pool.QueryRow(ctx, `
INSERT INTO messages (room_id, author, body) VALUES ($1, $2, $3)
RETURNING id, room_id, author, body, created_at`, room, author, text).
		Scan(&m.ID, &m.RoomID, &m.Author, &m.Text, &m.CreatedAt)
	if pgCode(err) == "23503" { // foreign_key_violation: the room is gone
		return Message{}, ErrNoRoom
	}
	return m, err
}

func (s *PGStore) History(ctx context.Context, room int64, before int64, limit int) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, room_id, author, body, created_at FROM messages
WHERE room_id = $1 AND ($2 = 0 OR id < $2)
ORDER BY id DESC LIMIT $3`, room, before, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Message, error) {
		var m Message
		err := row.Scan(&m.ID, &m.RoomID, &m.Author, &m.Text, &m.CreatedAt)
		return m, err
	})
}
