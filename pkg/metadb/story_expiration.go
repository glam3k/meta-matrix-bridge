package metadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-meta/pkg/metaid"
)

const storyExpiryBatchLimit = 128

type StoryExpirationEntry struct {
	MessageRowID  int64
	Room          networkid.PortalKey
	RoomMXID      id.RoomID
	EventMXID     id.EventID
	SenderMXID    id.UserID
	ExpiresAt     time.Time
	StoryMetadata *metaid.MessageMetadata
}

func (db *MetaDB) InsertStoryExpiration(ctx context.Context, entry *StoryExpirationEntry) error {
	if entry == nil || entry.MessageRowID == 0 || entry.RoomMXID == "" || entry.EventMXID == "" {
		return nil
	}
	_, err := db.Exec(ctx, `
		INSERT INTO story_expiration (
		    bridge_id, message_rowid, room_id, room_receiver, room_mxid,
		    event_mxid, sender_mxid, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT(message_rowid) DO UPDATE SET
		    event_mxid = excluded.event_mxid,
		    sender_mxid = excluded.sender_mxid,
		    expires_at = excluded.expires_at,
            room_mxid = excluded.room_mxid
    `, db.BridgeID, entry.MessageRowID, entry.Room.ID, entry.Room.Receiver, entry.RoomMXID, entry.EventMXID, entry.SenderMXID, entry.ExpiresAt.UnixMilli())
	return err
}

func (db *MetaDB) GetDueStoryExpirations(ctx context.Context, now time.Time, limit int) ([]*StoryExpirationEntry, error) {
	if limit <= 0 || limit > storyExpiryBatchLimit {
		limit = storyExpiryBatchLimit
	}
	rows, err := db.Query(ctx, `
		SELECT se.message_rowid, se.room_id, se.room_receiver, se.room_mxid,
		       se.event_mxid, se.sender_mxid, se.expires_at, m.metadata
        FROM story_expiration se
        JOIN message m ON m.bridge_id = se.bridge_id AND m.rowid = se.message_rowid
        WHERE se.bridge_id = $1 AND se.marked_at IS NULL AND se.expires_at <= $2
        ORDER BY se.expires_at ASC
        LIMIT $3
    `, db.BridgeID, now.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*StoryExpirationEntry
	for rows.Next() {
		var entry StoryExpirationEntry
		var roomID, roomReceiver, roomMXID, eventMXID, senderMXID string
		var expiresAt int64
		var metadataRaw sql.NullString
		if err := rows.Scan(&entry.MessageRowID, &roomID, &roomReceiver, &roomMXID, &eventMXID, &senderMXID, &expiresAt, &metadataRaw); err != nil {
			return nil, err
		}
		entry.Room = networkid.PortalKey{ID: networkid.PortalID(roomID), Receiver: networkid.UserLoginID(roomReceiver)}
		entry.RoomMXID = id.RoomID(roomMXID)
		entry.EventMXID = id.EventID(eventMXID)
		entry.SenderMXID = id.UserID(senderMXID)
		entry.ExpiresAt = time.UnixMilli(expiresAt)
		if metadataRaw.Valid {
			var meta metaid.MessageMetadata
			if err := json.Unmarshal([]byte(metadataRaw.String), &meta); err == nil {
				entry.StoryMetadata = &meta
			}
		}
		entries = append(entries, &entry)
	}
	return entries, rows.Err()
}

func (db *MetaDB) MarkStoryExpiration(ctx context.Context, messageRowID int64, markedAt time.Time) error {
	if messageRowID == 0 {
		return fmt.Errorf("story expiration message row missing")
	}
	_, err := db.Exec(ctx, `
		UPDATE story_expiration SET marked_at = $3
		WHERE bridge_id = $1 AND message_rowid = $2
	`, db.BridgeID, messageRowID, markedAt.UnixMilli())
	return err
}

func (db *MetaDB) CleanupStoryExpirations(ctx context.Context, before time.Time) (int64, error) {
	res, err := db.Exec(ctx, `
        DELETE FROM story_expiration
        WHERE bridge_id = $1 AND marked_at IS NOT NULL AND marked_at <= $2
    `, db.BridgeID, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (db *MetaDB) BackfillStoryExpirations(ctx context.Context, cutoff time.Time) (int, error) {
	rows, err := db.Query(ctx, `
		SELECT m.rowid, m.room_id, m.room_receiver, p.mxid, m.mxid, m.sender_mxid, m.metadata
		FROM message m
		JOIN portal p ON p.bridge_id = m.bridge_id AND p.id = m.room_id AND p.receiver = m.room_receiver
		LEFT JOIN story_expiration se ON se.bridge_id = m.bridge_id AND se.message_rowid = m.rowid
		WHERE m.bridge_id = $1 AND se.message_rowid IS NULL AND m.timestamp >= $2
	`, db.BridgeID, cutoff.UnixNano())
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var rowID int64
		var roomID, roomReceiver, roomMXID, eventMXID, senderMXID string
		var metadataRaw sql.NullString
		if err := rows.Scan(&rowID, &roomID, &roomReceiver, &roomMXID, &eventMXID, &senderMXID, &metadataRaw); err != nil {
			return count, err
		}
		if !metadataRaw.Valid {
			continue
		}
		var meta metaid.MessageMetadata
		if err := json.Unmarshal([]byte(metadataRaw.String), &meta); err != nil {
			continue
		}
		if meta.Story == nil || meta.Story.ExpiresAt <= 0 || roomMXID == "" || eventMXID == "" {
			continue
		}
		entry := StoryExpirationEntry{
			MessageRowID: rowID,
			Room:         networkid.PortalKey{ID: networkid.PortalID(roomID), Receiver: networkid.UserLoginID(roomReceiver)},
			RoomMXID:     id.RoomID(roomMXID),
			EventMXID:    id.EventID(eventMXID),
			SenderMXID:   id.UserID(senderMXID),
			ExpiresAt:    time.UnixMilli(meta.Story.ExpiresAt),
		}
		if err := db.InsertStoryExpiration(ctx, &entry); err == nil {
			count++
		}
	}
	return count, rows.Err()
}
