-- v7: story expiration tracking
CREATE TABLE IF NOT EXISTS story_expiration (
    bridge_id TEXT NOT NULL,
    message_rowid INTEGER NOT NULL,
    room_id TEXT NOT NULL,
    room_receiver TEXT NOT NULL,
    room_mxid TEXT NOT NULL,
    event_mxid TEXT NOT NULL,
    sender_mxid TEXT NOT NULL,
    expires_at BIGINT NOT NULL,
    marked_at BIGINT,
    CONSTRAINT story_expiration_message_fkey FOREIGN KEY (message_rowid)
        REFERENCES message (rowid) ON DELETE CASCADE,
    CONSTRAINT story_expiration_message_unique UNIQUE (message_rowid)
);
CREATE INDEX IF NOT EXISTS story_expiration_due_idx
    ON story_expiration (bridge_id, expires_at)
    WHERE marked_at IS NULL;
CREATE INDEX IF NOT EXISTS story_expiration_cleanup_idx
    ON story_expiration (bridge_id, marked_at)
    WHERE marked_at IS NOT NULL;
