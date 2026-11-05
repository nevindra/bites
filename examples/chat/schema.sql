-- The schema of the chat example. It is applied at start-up and is safe to apply again: every
-- statement is IF NOT EXISTS, and the store takes an advisory lock around it so two replicas
-- starting together do not race.
CREATE TABLE IF NOT EXISTS rooms (
	id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	name       text NOT NULL UNIQUE,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS messages (
	id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	room_id    bigint NOT NULL REFERENCES rooms (id),
	author     text NOT NULL,
	body       text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS messages_room_id_idx ON messages (room_id, id DESC);
