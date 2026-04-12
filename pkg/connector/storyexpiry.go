package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-meta/pkg/metadb"
	"go.mau.fi/mautrix-meta/pkg/metaid"
)

const (
	storyExpiryBatchSize = 128
)

type StoryExpiryManager struct {
	mc *MetaConnector

	running atomic.Bool
	cancel  context.CancelFunc
	log     zerolog.Logger
}

func NewStoryExpiryManager(mc *MetaConnector) *StoryExpiryManager {
	if mc == nil {
		return nil
	}
	return &StoryExpiryManager{
		mc:  mc,
		log: mc.Bridge.Log.With().Str("component", "story-expiry").Logger(),
	}
}

func (sem *StoryExpiryManager) Enabled() bool {
	if sem == nil || sem.mc == nil {
		return false
	}
	return sem.mc.Config.Stories.ExpiryMarker.Enabled
}

func (sem *StoryExpiryManager) Start(ctx context.Context) {
	if !sem.Enabled() {
		return
	}
	if sem.running.Swap(true) {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	sem.cancel = cancel
	go sem.loop(ctx)
}

func (sem *StoryExpiryManager) Stop() {
	if cancel := sem.cancel; cancel != nil {
		cancel()
	}
}

func (sem *StoryExpiryManager) loop(ctx context.Context) {
	defer sem.running.Store(false)
	cfg := sem.mc.Config.Stories.ExpiryMarker
	sem.backfill(ctx)
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = defaultStoryExpiryPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sem.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sem.runOnce(ctx)
		}
	}
}

func (sem *StoryExpiryManager) runOnce(ctx context.Context) {
	if err := sem.markDueStories(ctx); err != nil {
		sem.log.Err(err).Msg("Failed to mark expired stories")
	}
	if err := sem.cleanupMarked(ctx); err != nil {
		sem.log.Err(err).Msg("Failed to cleanup story expiration rows")
	}
}

func (sem *StoryExpiryManager) markDueStories(ctx context.Context) error {
	now := time.Now()
	entries, err := sem.mc.DB.GetDueStoryExpirations(ctx, now, storyExpiryBatchSize)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.RoomMXID == "" || entry.EventMXID == "" {
			_ = sem.mc.DB.MarkStoryExpiration(ctx, entry.MessageRowID, now)
			continue
		}
		if err := sem.sendExpiryEdit(ctx, entry); err != nil {
			sem.log.Warn().Err(err).
				Str("room_id", string(entry.RoomMXID)).
				Str("event_id", string(entry.EventMXID)).
				Msg("Failed to edit expired story")
			_ = sem.mc.DB.MarkStoryExpiration(ctx, entry.MessageRowID, now)
			continue
		}
		if err := sem.mc.DB.MarkStoryExpiration(ctx, entry.MessageRowID, now); err != nil {
			sem.log.Err(err).Msg("Failed to mark story expiration row")
		}
	}
	return nil
}

func (sem *StoryExpiryManager) cleanupMarked(ctx context.Context) error {
	delay := sem.mc.Config.Stories.ExpiryMarker.CleanupDelay
	if delay <= 0 {
		delay = defaultStoryExpiryCleanupDelay
	}
	cutoff := time.Now().Add(-delay)
	_, err := sem.mc.DB.CleanupStoryExpirations(ctx, cutoff)
	return err
}

func (sem *StoryExpiryManager) backfill(ctx context.Context) {
	delay := sem.mc.Config.Stories.ExpiryMarker.CleanupDelay
	if delay <= 0 {
		delay = defaultStoryExpiryCleanupDelay
	}
	cutoff := time.Now().Add(-delay)
	count, err := sem.mc.DB.BackfillStoryExpirations(ctx, cutoff)
	if err != nil {
		sem.log.Err(err).Msg("Failed to backfill story expiration table")
		return
	}
	if count > 0 {
		sem.log.Info().Int("rows", count).Msg("Backfilled story expiration entries")
	}
}

func (sem *StoryExpiryManager) sendExpiryEdit(ctx context.Context, entry *metadb.StoryExpirationEntry) error {
	intent, err := sem.intentForSender(ctx, entry.SenderMXID)
	if err != nil {
		return err
	}
	origEvt, err := intent.GetEvent(ctx, entry.RoomMXID, entry.EventMXID)
	if err != nil {
		return sem.sendFallbackExpiry(ctx, intent, entry)
	}
	origContent, raw := extractMessageContent(origEvt)
	if origContent == nil {
		return sem.sendFallbackExpiry(ctx, intent, entry)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	origStoryMeta := markStoryMetadataExpired(raw[storyMetadataKey])
	updated := cloneMessageContent(origContent)
	updated.Body = sem.annotateBody(updated.Body)
	if updated.FormattedBody != "" {
		updated.FormattedBody = sem.annotateFormattedBody(updated.FormattedBody)
	}
	edit := cloneMessageContent(updated)
	edit.RelatesTo = &event.RelatesTo{Type: event.RelReplace, EventID: entry.EventMXID}
	edit.NewContent = updated
	raw[storyMetadataKey] = origStoryMeta
	_, err = intent.SendMessage(ctx, entry.RoomMXID, origEvt.Type, &event.Content{
		Parsed: edit,
		Raw:    raw,
	}, nil)
	if err != nil {
		return sem.sendFallbackExpiry(ctx, intent, entry)
	}
	return nil
}

func (sem *StoryExpiryManager) buildExpiredBody(story *metaid.StoryMetadata) string {
	body := ""
	if story != nil {
		body = story.Body
		if body == "" && story.OwnerName != "" {
			body = fmt.Sprintf(storyBodyTemplate, story.OwnerName)
		}
	}
	if body == "" {
		body = "📖 Story"
	}
	return sem.annotateBody(body)
}

func (sem *StoryExpiryManager) annotateBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		trimmed = "📖 Story"
	}
	if strings.Contains(strings.ToLower(trimmed), "expired") {
		return trimmed
	}
	return trimmed + " ❌ (expired)"
}

func (sem *StoryExpiryManager) annotateFormattedBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return body
	}
	if strings.Contains(strings.ToLower(trimmed), "expired") {
		return trimmed
	}
	return trimmed + " ❌ (expired)"
}

func (sem *StoryExpiryManager) intentForSender(ctx context.Context, sender id.UserID) (bridgev2.MatrixAPI, error) {
	if sender == "" {
		return nil, fmt.Errorf("sender missing")
	}
	ghost, err := sem.mc.Bridge.GetGhostByMXID(ctx, sender)
	if err != nil {
		return nil, err
	}
	if ghost == nil || ghost.Intent == nil {
		return nil, fmt.Errorf("ghost intent unavailable for %s", sender)
	}
	return ghost.Intent, nil
}

func (sem *StoryExpiryManager) TrackEvent(ctx context.Context, portal *bridgev2.Portal, messageID networkid.MessageID) {
	if !sem.Enabled() || portal == nil || messageID == "" || portal.MXID == "" {
		return
	}
	messages, err := sem.mc.Bridge.DB.Message.GetAllPartsByID(ctx, portal.Receiver, messageID)
	if err != nil {
		sem.log.Err(err).Msg("Failed to load story messages for expiry tracking")
		return
	}
	for _, msg := range messages {
		sem.trackMessage(ctx, portal, msg)
	}
}

func (sem *StoryExpiryManager) trackMessage(ctx context.Context, portal *bridgev2.Portal, msg *database.Message) {
	if msg == nil || msg.RowID == 0 || msg.MXID == "" {
		return
	}
	meta, _ := msg.Metadata.(*metaid.MessageMetadata)
	if meta == nil || meta.Story == nil {
		return
	}
	expiresAt := time.UnixMilli(meta.Story.ExpiresAt)
	if expiresAt.IsZero() {
		return
	}
	entry := &metadb.StoryExpirationEntry{
		MessageRowID:  msg.RowID,
		Room:          msg.Room,
		RoomMXID:      portal.MXID,
		EventMXID:     msg.MXID,
		SenderMXID:    msg.SenderMXID,
		ExpiresAt:     expiresAt,
		StoryMetadata: meta,
	}
	if err := sem.mc.DB.InsertStoryExpiration(ctx, entry); err != nil {
		sem.log.Err(err).Msg("Failed to insert story expiration entry")
	}
}

func (m *MetaConnector) trackStoryExpiry(ctx context.Context, portal *bridgev2.Portal, messageID networkid.MessageID) {
	if m == nil || m.storyExpiry == nil {
		return
	}
	m.storyExpiry.TrackEvent(ctx, portal, messageID)
}

func (sem *StoryExpiryManager) sendFallbackExpiry(ctx context.Context, intent bridgev2.MatrixAPI, entry *metadb.StoryExpirationEntry) error {
	var story *metaid.StoryMetadata
	if entry.StoryMetadata != nil {
		story = entry.StoryMetadata.Story
	}
	body := sem.buildExpiredBody(story)
	storyMap := markStoryMetadataExpired(buildStoryMetadata(story))
	edit := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
		RelatesTo: &event.RelatesTo{
			Type:    event.RelReplace,
			EventID: entry.EventMXID,
		},
		NewContent: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    body,
		},
	}
	_, err := intent.SendMessage(ctx, entry.RoomMXID, event.EventMessage, &event.Content{
		Parsed: edit,
		Raw: map[string]any{
			storyMetadataKey: storyMap,
		},
	}, nil)
	return err
}

func extractMessageContent(evt *event.Event) (*event.MessageEventContent, map[string]any) {
	if evt == nil {
		return nil, nil
	}
	raw := cloneRawMap(evt.Content.Raw)
	if msg, ok := evt.Content.Parsed.(*event.MessageEventContent); ok && msg != nil {
		return cloneMessageContent(msg), raw
	}
	var parsed event.MessageEventContent
	if evt.Content.VeryRaw != nil {
		if err := json.Unmarshal(evt.Content.VeryRaw, &parsed); err == nil && parsed.MsgType != "" {
			return &parsed, raw
		}
	}
	if len(raw) > 0 {
		data, err := json.Marshal(raw)
		if err == nil {
			var decoded event.MessageEventContent
			if err := json.Unmarshal(data, &decoded); err == nil && decoded.MsgType != "" {
				return &decoded, raw
			}
		}
	}
	return nil, raw
}

func cloneMessageContent(orig *event.MessageEventContent) *event.MessageEventContent {
	if orig == nil {
		return nil
	}
	data, err := json.Marshal(orig)
	if err != nil {
		copy := *orig
		return &copy
	}
	var copy event.MessageEventContent
	if err := json.Unmarshal(data, &copy); err != nil {
		dup := *orig
		return &dup
	}
	return &copy
}

func cloneRawMap(orig map[string]any) map[string]any {
	if len(orig) == 0 {
		return map[string]any{}
	}
	data, err := json.Marshal(orig)
	if err != nil {
		return map[string]any{}
	}
	var copy map[string]any
	if err := json.Unmarshal(data, &copy); err != nil {
		return map[string]any{}
	}
	return copy
}

func markStoryMetadataExpired(val any) map[string]any {
	switch meta := val.(type) {
	case map[string]any:
		copy := cloneRawMap(meta)
		copy[storyMetadataExpiredKey] = true
		return copy
	case nil:
		return map[string]any{storyMetadataExpiredKey: true}
	default:
		data, err := json.Marshal(meta)
		if err != nil {
			return map[string]any{storyMetadataExpiredKey: true}
		}
		var copy map[string]any
		if err := json.Unmarshal(data, &copy); err != nil || copy == nil {
			copy = map[string]any{}
		}
		copy[storyMetadataExpiredKey] = true
		return copy
	}
}

func mergeRawContent(orig map[string]any, story map[string]any) map[string]any {
	copy := cloneRawMap(orig)
	if copy == nil {
		copy = map[string]any{}
	}
	copy[storyMetadataKey] = story
	return copy
}

func buildStoryMetadata(story *metaid.StoryMetadata) map[string]any {
	metadata := map[string]any{}
	if story == nil {
		return metadata
	}
	if story.Platform != "" {
		metadata["source_platform"] = story.Platform
	}
	if story.StoryID != "" {
		metadata["source_story_id"] = story.StoryID
	}
	if story.AuthorID != "" {
		metadata["source_author_id"] = story.AuthorID
	}
	if story.ReelID != "" {
		metadata["reel_id"] = story.ReelID
	}
	if story.PostedAt != 0 {
		metadata["posted_at"] = story.PostedAt
	}
	if story.ExpiresAt != 0 {
		metadata["expires_at"] = story.ExpiresAt
	}
	if story.OwnerName != "" {
		metadata["owner_name"] = story.OwnerName
	}
	return metadata
}
