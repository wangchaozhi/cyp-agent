-- Durable, idempotent order event journal. Existing rows predate the Go
-- journal and intentionally remain outside replay until explicitly migrated.
ALTER TABLE order_events ADD COLUMN IF NOT EXISTS event_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS order_events_event_id_uidx
    ON order_events (event_id)
    WHERE event_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS order_events_client_ts_idx
    ON order_events (client_id, ts);
