package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-meta/pkg/messagix/data/responses"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	"go.mau.fi/mautrix-meta/pkg/metaid"
)

var storySettingsEventType = event.Type{Type: "fi.mau.meta.story_settings", Class: event.StateEventType}

const storyMetadataKey = "fi.mau.meta.story"

var storyBodyTemplate = "📖 Story from %s"

type storySettingsContent struct {
	ReceiveStories bool   `json:"receive_stories"`
	GhostID        string `json:"ghost_id,omitempty"`
}

type InstagramStoryPoller struct {
	mc      *MetaClient
	cancel  context.CancelFunc
	running atomic.Bool
}

type storyProcessStats struct {
	total     int
	delivered int
	skipped   int
}

func NewInstagramStoryPoller(mc *MetaClient) *InstagramStoryPoller {
	return &InstagramStoryPoller{mc: mc}
}

func (sp *InstagramStoryPoller) Start(ctx context.Context) {
	if sp.mc == nil || !sp.mc.Main.Config.Stories.Enabled || !sp.mc.LoginMeta.Platform.IsInstagram() {
		return
	}
	if sp.running.Swap(true) {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	sp.cancel = cancel
	go sp.loop(ctx)
}

func (sp *InstagramStoryPoller) Stop() {
	if cancel := sp.cancel; cancel != nil {
		cancel()
	}
}

func (sp *InstagramStoryPoller) loop(ctx context.Context) {
	defer sp.running.Store(false)
	interval := sp.mc.Main.Config.Stories.PollInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sp.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sp.poll(ctx)
		}
	}
}

func (sp *InstagramStoryPoller) poll(ctx context.Context) {
	cli := sp.mc.Client
	if cli == nil || cli.Instagram == nil {
		return
	}
	sp.mc.UserLogin.Log.Debug().Msg("Polling Instagram stories")
	tray, err := cli.Instagram.FetchReelsTray(ctx)
	if err != nil {
		sp.mc.UserLogin.Log.Err(err).Msg("Failed to fetch reels tray")
		return
	}
	if tray == nil {
		return
	}
	totalUsersFound := len(tray.Tray)
	sp.mc.UserLogin.Log.Info().
		Int("story_users_found", totalUsersFound).
		Msg("Starting Instagram story poll")

	var totalUsers int
	totalStats := storyProcessStats{}
	for _, reel := range tray.Tray {
		if reel.User == nil {
			sp.mc.UserLogin.Log.Debug().Msg("Skipping reel without user info")
			continue
		}
		if len(reel.MediaIds) == 0 {
			sp.mc.UserLogin.Log.Debug().Str("ig_user_id", reel.User.Pk).Msg("Skipping reel with no media IDs")
			continue
		}
		stats := sp.processTrayEntry(ctx, reel)
		totalUsers++
		totalStats.total += stats.total
		totalStats.delivered += stats.delivered
		totalStats.skipped += stats.skipped
		sp.mc.UserLogin.Log.Info().
			Str("ig_user_id", reel.User.Pk).
			Str("ig_username", reel.User.Username).
			Int("stories_seen", stats.total).
			Int("stories_delivered", stats.delivered).
			Int("stories_skipped", stats.skipped).
			Msg("Processed Instagram stories for user")
	}
	sp.mc.UserLogin.Log.Info().
		Int("story_users_processed", totalUsers).
		Int("stories_checked", totalStats.total).
		Int("stories_delivered", totalStats.delivered).
		Int("stories_skipped", totalStats.skipped).
		Msg("Instagram story poll completed")
}

func (sp *InstagramStoryPoller) processTrayEntry(ctx context.Context, reel responses.ReelInfo) storyProcessStats {
	resp, err := sp.mc.Client.Instagram.FetchReel(ctx, []string{reel.User.Pk}, "")
	if err != nil {
		sp.mc.UserLogin.Log.Err(err).Str("user_id", reel.User.Pk).Msg("Failed to fetch reel items")
		return storyProcessStats{}
	}
	data, ok := resp.Reels[reel.User.Pk]
	if !ok {
		sp.mc.UserLogin.Log.Warn().Str("user_id", reel.User.Pk).Msg("Reel info missing from response")
		return storyProcessStats{}
	}
	stats := storyProcessStats{}
	items := make([]*responses.ReelItem, 0, len(data.Items))
	for _, item := range data.Items {
		if item == nil {
			stats.skipped++
			sp.mc.UserLogin.Log.Debug().Str("ig_user_id", reel.User.Pk).Msg("Skipping nil story item")
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].TakenAt < items[j].TakenAt
	})
	for _, item := range items {
		stats.total++
		delivered, reason := sp.bridgeStory(ctx, reel, item)
		if delivered {
			stats.delivered++
		} else {
			stats.skipped++
			if reason != "" {
				sp.mc.UserLogin.Log.Debug().
					Str("ig_user_id", reel.User.Pk).
					Str("story_id", item.Pk).
					Str("reason", reason).
					Msg("Skipping Instagram story")
			}
		}
	}
	return stats
}

func makeStoryMessageID(loginID networkid.UserLoginID, igUserID, storyID string) networkid.MessageID {
	if loginID == "" || igUserID == "" || storyID == "" {
		return ""
	}
	return networkid.MessageID(fmt.Sprintf("story:%s:%s:%s", loginID, igUserID, storyID))
}

func (sp *InstagramStoryPoller) bridgeStory(ctx context.Context, reel responses.ReelInfo, item *responses.ReelItem) (bool, string) {
	if sp == nil || sp.mc == nil || sp.mc.Main == nil || sp.mc.Main.Bridge == nil || item == nil || reel.User == nil {
		return false, "missing_context"
	}
	storyMsgID := makeStoryMessageID(sp.mc.UserLogin.ID, reel.User.Pk, item.Pk)
	if storyMsgID == "" {
		return false, "missing_story_id"
	}
	if existing, err := sp.mc.Main.Bridge.DB.Message.GetFirstPartByID(ctx, sp.mc.UserLogin.ID, storyMsgID); err != nil {
		sp.mc.UserLogin.Log.Err(err).
			Str("story_id", item.Pk).
			Msg("Failed to check if story was already bridged")
		return false, "db_error"
	} else if existing != nil {
		return false, "already_bridged"
	}
	ghostMXID, dbPortal := sp.lookupStoryPortal(ctx, reel.User.Pk)
	if dbPortal == nil || ghostMXID == "" {
		return false, "no_portal"
	}
	if sp.deliverStoryToPortal(ctx, dbPortal, ghostMXID, reel, item, storyMsgID) {
		return true, ""
	}
	return false, "deliver_failed"
}

func (sp *InstagramStoryPoller) lookupStoryPortal(ctx context.Context, igUserID string) (id.UserID, *database.Portal) {
	if igUserID == "" {
		return "", nil
	}
	candidates := sp.storyUserIDCandidates(ctx, igUserID)
	for _, ghostID := range candidates {
		if ghostID == "" {
			continue
		}
		dbPortal, err := sp.mc.Main.Bridge.DB.Portal.GetDM(ctx, sp.mc.UserLogin.ID, ghostID)
		if err != nil || dbPortal == nil {
			if err != nil {
				sp.mc.UserLogin.Log.Debug().
					Err(err).
					Str("ig_user_id", igUserID).
					Str("ghost_id", string(ghostID)).
					Msg("Failed to load portal for story delivery")
			}
			continue
		}
		ghost, err := sp.mc.Main.Bridge.GetGhostByID(ctx, ghostID)
		if err != nil {
			sp.mc.UserLogin.Log.Err(err).
				Str("ig_user_id", igUserID).
				Str("ghost_id", string(ghostID)).
				Msg("Failed to load ghost for story delivery")
			continue
		}
		if ghost == nil || ghost.Intent == nil {
			sp.mc.UserLogin.Log.Debug().
				Str("ig_user_id", igUserID).
				Str("ghost_id", string(ghostID)).
				Msg("Ghost missing intent for story delivery")
			continue
		}
		return ghost.Intent.GetMXID(), dbPortal
	}
	sp.mc.UserLogin.Log.Debug().
		Str("ig_user_id", igUserID).
		Msg("No eligible portal found for Instagram story delivery")
	return "", nil
}

func (sp *InstagramStoryPoller) storyUserIDCandidates(ctx context.Context, igUserID string) []networkid.UserID {
	candidates := make([]networkid.UserID, 0, 2)
	if igUserID == "" {
		return candidates
	}
	if fbID, err := sp.mc.getFBIDForIGUser(ctx, igUserID); err != nil {
		sp.mc.UserLogin.Log.Debug().Err(err).Str("ig_user_id", igUserID).Msg("Failed to resolve FBID for IG story user")
	} else if fbID != 0 {
		candidates = append(candidates, metaid.MakeUserID(fbID))
	}
	if parsed, err := metaid.ParseIDFromString(igUserID); err == nil {
		candidate := metaid.MakeUserID(parsed)
		if candidate != "" && !containsStoryUserID(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	} else {
		candidate := networkid.UserID(igUserID)
		if candidate != "" && !containsStoryUserID(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func containsStoryUserID(list []networkid.UserID, id networkid.UserID) bool {
	for _, existing := range list {
		if existing == id {
			return true
		}
	}
	return false
}

func (sp *InstagramStoryPoller) deliverStoryToPortal(ctx context.Context, dbPortal *database.Portal, ghostMXID id.UserID, reel responses.ReelInfo, item *responses.ReelItem, messageID networkid.MessageID) bool {
	portal, err := sp.mc.Main.Bridge.GetPortalByKey(ctx, dbPortal.PortalKey)
	if err != nil {
		sp.mc.UserLogin.Log.Err(err).
			Object("portal_key", dbPortal.PortalKey).
			Msg("Failed to load portal for story delivery")
		return false
	}
	if portal == nil || portal.MXID == "" {
		return false
	}
	enabled, err := sp.mc.Main.getStorySetting(ctx, portal, ghostMXID)
	if err != nil {
		sp.mc.UserLogin.Log.Err(err).
			Object("portal_key", portal.PortalKey).
			Msg("Failed to get story settings")
		return false
	}
	if !enabled {
		return false
	}
	authorID := ""
	if item.User.Pk != "" {
		authorID = item.User.Pk
	} else if reel.User != nil {
		// Some API responses omit item.User; fall back to the tray owner so we still tag metadata.
		// This branch hasn't been exercised in manual testing yet, so treat it as best-effort.
		authorID = reel.User.Pk
	}
	evt := &InstagramStoryEvent{
		mc:                 sp.mc,
		portalKey:          portal.PortalKey,
		sender:             networkid.UserID(reel.User.Pk),
		senderLogin:        sp.mc.UserLogin.ID,
		storyItem:          item,
		storyAuthorID:      authorID,
		disabledReplyTypes: reel.DisabledReplyTypes,
		username:           reel.User.Username,
		storyURL:           fmt.Sprintf("https://www.instagram.com/stories/%s/%s/", reel.User.Username, item.Pk),
		messageID:          messageID,
		timestamp:          time.Unix(int64(item.TakenAt), 0),
	}
	sp.mc.UserLogin.QueueRemoteEvent(evt)
	return true
}

func buildStoryStateKey(id.UserID) string {
	// Room-wide toggle: Matrix treats user-like state keys as user-owned, so leave it blank.
	return ""
}

func (m *MetaConnector) getStorySetting(ctx context.Context, portal *bridgev2.Portal, ghostMXID id.UserID) (bool, error) {
	defaultEnabled := m.Config.Stories.DefaultEnabled
	if portal.MXID == "" {
		return defaultEnabled, nil
	}
	mx, ok := m.Bridge.Matrix.(bridgev2.MatrixConnectorWithArbitraryRoomState)
	if !ok {
		return false, fmt.Errorf("matrix connector does not support state lookups")
	}
	evt, err := mx.GetStateEvent(ctx, portal.MXID, storySettingsEventType, buildStoryStateKey(ghostMXID))
	if err != nil {
		var respErr mautrix.RespError
		if errors.As(err, &respErr) && respErr.ErrCode == "M_NOT_FOUND" {
			return defaultEnabled, nil
		}
		return false, err
	}
	if evt == nil {
		return defaultEnabled, nil
	}
	return decodeStorySetting(evt, defaultEnabled)
}

func decodeStorySetting(evt *event.Event, defaultEnabled bool) (bool, error) {
	var content storySettingsContent
	if evt.Content.VeryRaw != nil {
		if err := json.Unmarshal(evt.Content.VeryRaw, &content); err == nil {
			return content.ReceiveStories, nil
		}
	}
	if evt.Content.Raw != nil {
		if val, ok := evt.Content.Raw["receive_stories"]; ok {
			if enabled, ok := val.(bool); ok {
				return enabled, nil
			}
		}
	}
	return defaultEnabled, nil
}

func (m *MetaConnector) setStorySetting(ctx context.Context, portal *bridgev2.Portal, ghostMXID id.UserID, enabled bool) error {
	if portal.MXID == "" {
		return fmt.Errorf("room does not have a Matrix ID yet")
	}
	content := &event.Content{Parsed: &storySettingsContent{ReceiveStories: enabled, GhostID: string(ghostMXID)}}
	_, err := m.Bridge.Bot.SendState(ctx, portal.MXID, storySettingsEventType, buildStoryStateKey(ghostMXID), content, time.Time{})
	return err
}

type InstagramStoryEvent struct {
	mc                 *MetaClient
	portalKey          networkid.PortalKey
	sender             networkid.UserID
	senderLogin        networkid.UserLoginID
	storyItem          *responses.ReelItem
	storyAuthorID      string
	disabledReplyTypes []string
	username           string
	storyURL           string
	messageID          networkid.MessageID
	timestamp          time.Time
}

func (evt *InstagramStoryEvent) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventMessage
}

func (evt *InstagramStoryEvent) GetPortalKey() networkid.PortalKey {
	return evt.portalKey
}

func (evt *InstagramStoryEvent) AddLogContext(c zerolog.Context) zerolog.Context {
	return c.
		Stringer("portal_key", evt.portalKey).
		Str("story_id", string(evt.messageID)).
		Str("story_user", evt.username)
}

func (evt *InstagramStoryEvent) GetSender() bridgev2.EventSender {
	return bridgev2.EventSender{
		Sender:      evt.sender,
		SenderLogin: evt.senderLogin,
		ForceDMUser: true,
	}
}

func (evt *InstagramStoryEvent) GetID() networkid.MessageID {
	return evt.messageID
}

func (evt *InstagramStoryEvent) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	if evt.mc == nil || evt.mc.Main == nil || evt.mc.Main.MsgConv == nil {
		return nil, fmt.Errorf("message converter unavailable")
	}
	if evt.storyItem == nil {
		return nil, fmt.Errorf("story data missing")
	}
	part, err := evt.mc.Main.MsgConv.InstagramStoryItemToMatrix(ctx, portal, intent, evt.messageID, evt.storyItem)
	if err != nil {
		return nil, err
	}
	if part.Content != nil {
		part.Content.Body = fmt.Sprintf(storyBodyTemplate, evt.username)
	}
	if part.Extra == nil {
		part.Extra = make(map[string]any)
	}
	postedAt := evt.timestamp.UnixMilli()
	expiresAt := int64(evt.storyItem.ExpiringAt) * 1000
	if expiresAt == 0 {
		expiresAt = postedAt
	}
	authorID := evt.storyAuthorID
	if authorID == "" && evt.storyItem.User.Pk != "" {
		authorID = evt.storyItem.User.Pk
	}
	metadata := map[string]any{
		"source_platform": types.Instagram.String(),
		"source_story_id": evt.storyItem.Pk,
		"posted_at":       postedAt,
		"expires_at":      expiresAt,
	}
	if authorID != "" {
		metadata["source_author_id"] = authorID
	}
	if evt.storyItem.VideoDuration > 0 {
		metadata["duration_ms"] = int(evt.storyItem.VideoDuration * 1000)
	}
	part.Extra[storyMetadataKey] = metadata
	meta, _ := part.DBMetadata.(*metaid.MessageMetadata)
	if meta == nil {
		meta = &metaid.MessageMetadata{}
		part.DBMetadata = meta
	}
	meta.Story = &metaid.StoryMetadata{
		Platform:           types.Instagram.String(),
		StoryID:            evt.storyItem.Pk,
		ReelID:             authorID,
		AuthorID:           authorID,
		PostedAt:           postedAt,
		ExpiresAt:          expiresAt,
		CanReply:           ptr.Ptr(evt.storyItem.CanReply),
		DisabledReplyTypes: evt.disabledReplyTypes,
	}
	if evt.storyURL != "" {
		part.Extra["external_url"] = evt.storyURL
	}
	converted := &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{part}}
	return converted, nil
}

func (evt *InstagramStoryEvent) GetTimestamp() time.Time {
	return evt.timestamp
}

func (evt *InstagramStoryEvent) GetStreamOrder() int64 {
	return evt.timestamp.UnixMilli()
}

// ... (rest of file continues with InstagramStoryEvent definition)
