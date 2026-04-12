package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
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

const (
	storyMetadataKey        = "fi.mau.meta.story"
	storyMetadataExpiredKey = "expired"
)

var storyBodyTemplate = "📖 Story from %s"

type storySettingsContent struct {
	ReceiveStories    bool            `json:"receive_stories"`
	GhostID           string          `json:"ghost_id,omitempty"`
	PlatformOverrides map[string]bool `json:"platform_overrides,omitempty"`
}

func newStorySettingsContent(defaultEnabled bool) *storySettingsContent {
	return &storySettingsContent{ReceiveStories: defaultEnabled}
}

func (c *storySettingsContent) clone() *storySettingsContent {
	if c == nil {
		return nil
	}
	clone := &storySettingsContent{
		ReceiveStories: c.ReceiveStories,
		GhostID:        c.GhostID,
	}
	if len(c.PlatformOverrides) > 0 {
		clone.PlatformOverrides = make(map[string]bool, len(c.PlatformOverrides))
		for k, v := range c.PlatformOverrides {
			clone.PlatformOverrides[k] = v
		}
	}
	return clone
}

func (c *storySettingsContent) enabledFor(platform types.Platform) bool {
	if c == nil {
		return false
	}
	if platform.IsValid() {
		if val, ok := c.PlatformOverrides[platform.String()]; ok {
			return val
		}
	}
	return c.ReceiveStories
}

func (c *storySettingsContent) apply(platform types.Platform, applyToAll bool, enabled bool) {
	if c == nil {
		return
	}
	if applyToAll || !platform.IsValid() {
		c.ReceiveStories = enabled
		for key, val := range c.PlatformOverrides {
			if val == enabled {
				delete(c.PlatformOverrides, key)
			}
		}
		if len(c.PlatformOverrides) == 0 {
			c.PlatformOverrides = nil
		}
		return
	}
	if c.PlatformOverrides == nil {
		c.PlatformOverrides = make(map[string]bool)
	}
	key := platform.String()
	c.PlatformOverrides[key] = enabled
	if c.PlatformOverrides[key] == c.ReceiveStories {
		delete(c.PlatformOverrides, key)
	}
	if len(c.PlatformOverrides) == 0 {
		c.PlatformOverrides = nil
	}
}

type StoryPoller interface {
	Start(ctx context.Context)
	Stop()
	Platform() types.Platform
}

type StoryPollerManager struct {
	pollers []StoryPoller
}

func NewStoryPollerManager() *StoryPollerManager {
	return &StoryPollerManager{}
}

func (pm *StoryPollerManager) Add(p StoryPoller) {
	if p == nil {
		return
	}
	pm.pollers = append(pm.pollers, p)
}

func (pm *StoryPollerManager) Start(ctx context.Context) {
	for _, poller := range pm.pollers {
		poller.Start(ctx)
	}
}

func (pm *StoryPollerManager) Stop() {
	for _, poller := range pm.pollers {
		poller.Stop()
	}
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

func (sp *InstagramStoryPoller) Platform() types.Platform {
	return types.Instagram
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
	if !sp.mc.Main.Config.Stories.SkipPollOnStart {
		sp.poll(ctx)
	}
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

func makeStoryMessageID(platform types.Platform, loginID networkid.UserLoginID, ownerID, storyID string) networkid.MessageID {
	if loginID == "" || ownerID == "" || storyID == "" {
		return ""
	}
	var prefix string
	switch {
	case platform.IsInstagram():
		prefix = "ig"
	case platform.IsMessenger():
		prefix = "fb"
	default:
		prefix = "unknown"
	}
	return networkid.MessageID(fmt.Sprintf("story:%s:%s:%s:%s", prefix, loginID, ownerID, storyID))
}

func (sp *InstagramStoryPoller) bridgeStory(ctx context.Context, reel responses.ReelInfo, item *responses.ReelItem) (bool, string) {
	if sp == nil || sp.mc == nil || sp.mc.Main == nil || sp.mc.Main.Bridge == nil || item == nil || reel.User == nil {
		return false, "missing_context"
	}
	storyMsgID := makeStoryMessageID(types.Instagram, sp.mc.UserLogin.ID, reel.User.Pk, item.Pk)
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
	enabled, err := sp.mc.Main.getStorySetting(ctx, portal, ghostMXID, types.Instagram)
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

func (sp *MessengerStoryPoller) bridgeStory(ctx context.Context, bucket responses.FBStoriesBucketNode, story responses.FBStoryNode) (bool, string) {
	if sp == nil || sp.mc == nil || sp.mc.Main == nil || sp.mc.Main.Bridge == nil {
		return false, "missing_context"
	}
	ownerID := bucket.StoryBucketOwner.ID
	storyMsgID := makeStoryMessageID(types.Messenger, sp.mc.UserLogin.ID, ownerID, story.ID)
	if storyMsgID == "" {
		return false, "missing_story_id"
	}
	if existing, err := sp.mc.Main.Bridge.DB.Message.GetFirstPartByID(ctx, sp.mc.UserLogin.ID, storyMsgID); err != nil {
		sp.mc.UserLogin.Log.Err(err).
			Str("story_id", story.ID).
			Msg("Failed to check if Messenger story was already bridged")
		return false, "db_error"
	} else if existing != nil {
		return false, "already_bridged"
	}
	ghostMXID, dbPortal := sp.lookupMessengerStoryPortal(ctx, ownerID)
	if dbPortal == nil || ghostMXID == "" {
		return false, "no_portal"
	}
	if !sp.deliverMessengerStory(ctx, dbPortal, ghostMXID, bucket, story, storyMsgID) {
		return false, "deliver_failed"
	}
	return true, ""
}

func (sp *MessengerStoryPoller) lookupMessengerStoryPortal(ctx context.Context, ownerID string) (id.UserID, *database.Portal) {
	if ownerID == "" {
		return "", nil
	}
	ghostID := networkid.UserID(ownerID)
	dbPortal, err := sp.mc.Main.Bridge.DB.Portal.GetDM(ctx, sp.mc.UserLogin.ID, ghostID)
	if err != nil || dbPortal == nil {
		if err != nil {
			sp.mc.UserLogin.Log.Debug().
				Err(err).
				Str("fb_user_id", ownerID).
				Msg("Failed to load portal for Messenger story")
		}
		return "", nil
	}
	ghost, err := sp.mc.Main.Bridge.GetGhostByID(ctx, ghostID)
	if err != nil || ghost == nil || ghost.Intent == nil {
		if err != nil {
			sp.mc.UserLogin.Log.Err(err).
				Str("fb_user_id", ownerID).
				Msg("Failed to load ghost for Messenger story")
		}
		return "", nil
	}
	return ghost.Intent.GetMXID(), dbPortal
}

func (sp *MessengerStoryPoller) deliverMessengerStory(ctx context.Context, dbPortal *database.Portal, ghostMXID id.UserID, bucket responses.FBStoriesBucketNode, story responses.FBStoryNode, messageID networkid.MessageID) bool {
	portal, err := sp.mc.Main.Bridge.GetPortalByKey(ctx, dbPortal.PortalKey)
	if err != nil {
		sp.mc.UserLogin.Log.Err(err).
			Object("portal_key", dbPortal.PortalKey).
			Msg("Failed to load portal for Messenger story delivery")
		return false
	}
	if portal == nil || portal.MXID == "" {
		return false
	}
	enabled, err := sp.mc.Main.getStorySetting(ctx, portal, ghostMXID, types.Messenger)
	if err != nil {
		sp.mc.UserLogin.Log.Err(err).
			Object("portal_key", portal.PortalKey).
			Msg("Failed to get Messenger story settings")
		return false
	}
	if !enabled {
		return false
	}
	storyURL := story.Url
	if storyURL == "" && story.StoryCardInfo.PermalinkInfo != nil {
		storyURL = story.StoryCardInfo.PermalinkInfo.URI
	}
	storyCopy := story
	evt := &MessengerStoryEvent{
		mc:          sp.mc,
		portalKey:   portal.PortalKey,
		sender:      networkid.UserID(bucket.StoryBucketOwner.ID),
		senderLogin: sp.mc.UserLogin.ID,
		storyItem:   &storyCopy,
		bucketID:    bucket.ID,
		ownerName:   bucket.StoryBucketOwner.Name,
		storyURL:    storyURL,
		messageID:   messageID,
		timestamp:   time.Unix(story.CreationTime, 0),
	}
	sp.mc.UserLogin.QueueRemoteEvent(evt)
	return true
}

func (sp *MessengerStoryPoller) pruneMessengerStoryCache() {
	sp.cacheMu.Lock()
	defer sp.cacheMu.Unlock()
	now := time.Now()
	for storyID, expiry := range sp.seenStories {
		if !expiry.IsZero() && now.After(expiry) {
			delete(sp.seenStories, storyID)
		}
	}
	for bucketID, state := range sp.bucketCache {
		if !state.expiresAt.IsZero() && now.After(state.expiresAt) {
			delete(sp.bucketCache, bucketID)
		}
	}
}

func (sp *MessengerStoryPoller) storyExpiration(story responses.FBStoryNode) time.Time {
	if story.StoryCardInfo.ReplyThreadExpirationTime != "" {
		if exp, err := strconv.ParseInt(story.StoryCardInfo.ReplyThreadExpirationTime, 10, 64); err == nil && exp > 0 {
			return time.Unix(exp, 0)
		}
		if parsed, err := time.Parse(time.RFC3339, story.StoryCardInfo.ReplyThreadExpirationTime); err == nil {
			return parsed
		}
	}
	created := time.Unix(story.CreationTime, 0)
	return created.Add(messengerStoryLifetime)
}

func (sp *MessengerStoryPoller) markStorySeen(story responses.FBStoryNode) {
	if story.ID == "" {
		return
	}
	expiry := sp.storyExpiration(story)
	sp.cacheMu.Lock()
	defer sp.cacheMu.Unlock()
	sp.seenStories[story.ID] = expiry
}

func (sp *MessengerStoryPoller) isStorySeen(storyID string) bool {
	if storyID == "" {
		return false
	}
	sp.cacheMu.Lock()
	defer sp.cacheMu.Unlock()
	expiry, ok := sp.seenStories[storyID]
	if !ok {
		return false
	}
	if !expiry.IsZero() && time.Now().After(expiry) {
		delete(sp.seenStories, storyID)
		return false
	}
	return true
}

func (sp *MessengerStoryPoller) markBucketSeen(bucketID string, cursorTS int64, expiry time.Time) {
	if bucketID == "" || cursorTS == 0 {
		return
	}
	if expiry.IsZero() {
		expiry = time.Unix(cursorTS, 0).Add(messengerStoryLifetime)
	}
	sp.cacheMu.Lock()
	defer sp.cacheMu.Unlock()
	state := sp.bucketCache[bucketID]
	if cursorTS > state.latestTimestamp {
		state.latestTimestamp = cursorTS
	}
	if state.expiresAt.IsZero() || expiry.After(state.expiresAt) {
		state.expiresAt = expiry
	}
	sp.bucketCache[bucketID] = state
}

func (sp *MessengerStoryPoller) shouldSkipBucket(bucketID string, cursorTS int64) bool {
	if bucketID == "" || cursorTS == 0 {
		return false
	}
	sp.cacheMu.Lock()
	defer sp.cacheMu.Unlock()
	state, ok := sp.bucketCache[bucketID]
	if !ok {
		return false
	}
	if !state.expiresAt.IsZero() && time.Now().After(state.expiresAt) {
		delete(sp.bucketCache, bucketID)
		return false
	}
	return state.latestTimestamp >= cursorTS
}

func (sp *MessengerStoryPoller) extractBucketCursorTimestamp(cursor string) int64 {
	if cursor == "" {
		return 0
	}
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.TrimSpace(string(decoded)), ":")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		if ts, err := strconv.ParseInt(part, 10, 64); err == nil && ts > 1000000000 {
			return ts
		}
	}
	return 0
}

func buildStoryStateKey(id.UserID) string {
	// Room-wide toggle: Matrix treats user-like state keys as user-owned, so leave it blank.
	return ""
}

func (m *MetaConnector) getStorySetting(ctx context.Context, portal *bridgev2.Portal, ghostMXID id.UserID, platform types.Platform) (bool, error) {
	content, err := m.getStorySettingsContent(ctx, portal, ghostMXID)
	if err != nil {
		return false, err
	}
	if content == nil {
		return m.Config.Stories.DefaultEnabled, nil
	}
	return content.enabledFor(platform), nil
}

func (m *MetaConnector) getStorySettingsContent(ctx context.Context, portal *bridgev2.Portal, ghostMXID id.UserID) (*storySettingsContent, error) {
	defaultEnabled := m.Config.Stories.DefaultEnabled
	if portal.MXID == "" {
		return newStorySettingsContent(defaultEnabled), nil
	}
	mx, ok := m.Bridge.Matrix.(bridgev2.MatrixConnectorWithArbitraryRoomState)
	if !ok {
		return nil, fmt.Errorf("matrix connector does not support state lookups")
	}
	evt, err := mx.GetStateEvent(ctx, portal.MXID, storySettingsEventType, buildStoryStateKey(ghostMXID))
	if err != nil {
		var respErr mautrix.RespError
		if errors.As(err, &respErr) && respErr.ErrCode == "M_NOT_FOUND" {
			return newStorySettingsContent(defaultEnabled), nil
		}
		return nil, err
	}
	if evt == nil {
		return newStorySettingsContent(defaultEnabled), nil
	}
	return decodeStorySetting(evt, defaultEnabled)
}

func decodeStorySetting(evt *event.Event, defaultEnabled bool) (*storySettingsContent, error) {
	content := newStorySettingsContent(defaultEnabled)
	if evt.Content.VeryRaw != nil {
		if err := json.Unmarshal(evt.Content.VeryRaw, content); err == nil {
			if content.PlatformOverrides == nil {
				content.PlatformOverrides = nil
			}
			return content, nil
		}
	}
	if evt.Content.Raw != nil {
		if val, ok := evt.Content.Raw["receive_stories"]; ok {
			if enabled, ok := val.(bool); ok {
				content.ReceiveStories = enabled
			}
		}
	}
	return content, nil
}

func (m *MetaConnector) setStorySetting(ctx context.Context, portal *bridgev2.Portal, ghostMXID id.UserID, platform types.Platform, applyToAll bool, enabled bool) error {
	if portal.MXID == "" {
		return fmt.Errorf("room does not have a Matrix ID yet")
	}
	content, err := m.getStorySettingsContent(ctx, portal, ghostMXID)
	if err != nil {
		return err
	}
	if content == nil {
		content = newStorySettingsContent(m.Config.Stories.DefaultEnabled)
	}
	content.apply(platform, applyToAll, enabled)
	content.GhostID = string(ghostMXID)
	state := &event.Content{Parsed: content}
	_, err = m.Bridge.Bot.SendState(ctx, portal.MXID, storySettingsEventType, buildStoryStateKey(ghostMXID), state, time.Time{})
	return err
}

func (m *MetaConnector) pickStorySettingsGhost(ctx context.Context, portal *bridgev2.Portal) id.UserID {
	if portal == nil {
		return ""
	}
	if portal.RoomType == database.RoomTypeDM && portal.OtherUserID != "" {
		ghost, err := m.Bridge.GetGhostByID(ctx, portal.OtherUserID)
		if err == nil && ghost != nil && ghost.Intent != nil {
			return ghost.Intent.GetMXID()
		}
	}
	settings, err := m.getStorySettingsContent(ctx, portal, "")
	if err == nil && settings != nil && settings.GhostID != "" {
		return id.UserID(settings.GhostID)
	}
	return ""
}

func (m *MetaConnector) resetStorySettingsForLogin(ctx context.Context, login *bridgev2.UserLogin, enabled bool) (int, error) {
	if login == nil || login.Bridge == nil || login.Bridge.DB == nil {
		return 0, fmt.Errorf("login data unavailable for story reset")
	}
	portals, err := login.Bridge.DB.UserPortal.GetAllForLogin(ctx, login.UserLogin)
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, userPortal := range portals {
		portal, err := m.Bridge.GetPortalByKey(ctx, userPortal.Portal)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Stringer("portal_key", userPortal.Portal).
				Msg("Failed to load portal for story reset")
			continue
		}
		if portal == nil || portal.MXID == "" {
			continue
		}
		ghostMXID := m.pickStorySettingsGhost(ctx, portal)
		var anyPlatform types.Platform
		if err := m.setStorySetting(ctx, portal, ghostMXID, anyPlatform, true, enabled); err != nil {
			zerolog.Ctx(ctx).Err(err).
				Str("room_id", string(portal.MXID)).
				Msg("Failed to reset story settings for room")
			continue
		}
		updated++
	}
	return updated, nil
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
		"source_platform":       types.Instagram.String(),
		"source_story_id":       evt.storyItem.Pk,
		"posted_at":             postedAt,
		"expires_at":            expiresAt,
		storyMetadataExpiredKey: false,
	}
	if evt.username != "" {
		metadata["owner_name"] = evt.username
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
		Body:               part.Content.Body,
		OwnerName:          evt.username,
		Expired:            false,
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

func (evt *InstagramStoryEvent) PostHandle(ctx context.Context, portal *bridgev2.Portal) {
	if evt.mc == nil || evt.mc.Main == nil {
		return
	}
	evt.mc.Main.trackStoryExpiry(ctx, portal, evt.messageID)
}

type MessengerStoryEvent struct {
	mc          *MetaClient
	portalKey   networkid.PortalKey
	sender      networkid.UserID
	senderLogin networkid.UserLoginID
	storyItem   *responses.FBStoryNode
	bucketID    string
	ownerName   string
	storyURL    string
	messageID   networkid.MessageID
	timestamp   time.Time
}

func (evt *MessengerStoryEvent) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventMessage
}

func (evt *MessengerStoryEvent) GetPortalKey() networkid.PortalKey {
	return evt.portalKey
}

func (evt *MessengerStoryEvent) AddLogContext(c zerolog.Context) zerolog.Context {
	return c.
		Stringer("portal_key", evt.portalKey).
		Str("story_id", string(evt.messageID)).
		Str("story_user", evt.ownerName)
}

func (evt *MessengerStoryEvent) GetSender() bridgev2.EventSender {
	return bridgev2.EventSender{
		Sender:      evt.sender,
		SenderLogin: evt.senderLogin,
		ForceDMUser: true,
	}
}

func (evt *MessengerStoryEvent) GetID() networkid.MessageID {
	return evt.messageID
}

func (evt *MessengerStoryEvent) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	if evt.mc == nil || evt.mc.Main == nil || evt.mc.Main.MsgConv == nil {
		return nil, fmt.Errorf("message converter unavailable")
	}
	if evt.storyItem == nil {
		return nil, fmt.Errorf("story data missing")
	}
	part, err := evt.mc.Main.MsgConv.MessengerStoryItemToMatrix(ctx, portal, intent, evt.messageID, evt.storyItem, evt.bucketID)
	if err != nil {
		return nil, err
	}
	if part.Content != nil {
		part.Content.Body = fmt.Sprintf(storyBodyTemplate, evt.ownerName)
	}
	if part.Extra == nil {
		part.Extra = make(map[string]any)
	}
	postedAt := evt.timestamp.UnixMilli()
	expiresAt := postedAt
	if expStr := evt.storyItem.StoryCardInfo.ReplyThreadExpirationTime; expStr != "" {
		if exp, err := strconv.ParseInt(expStr, 10, 64); err == nil {
			expiresAt = exp * 1000
		}
	}
	metadata := map[string]any{
		"source_platform":       types.Messenger.String(),
		"source_story_id":       evt.storyItem.ID,
		"posted_at":             postedAt,
		"expires_at":            expiresAt,
		storyMetadataExpiredKey: false,
	}
	if evt.ownerName != "" {
		metadata["owner_name"] = evt.ownerName
	}
	if evt.storyItem.StoryCardInfo.StoryPlayDuration > 0 {
		metadata["duration_ms"] = int(evt.storyItem.StoryCardInfo.StoryPlayDuration * 1000)
	}
	part.Extra[storyMetadataKey] = metadata
	meta, _ := part.DBMetadata.(*metaid.MessageMetadata)
	if meta == nil {
		meta = &metaid.MessageMetadata{}
		part.DBMetadata = meta
	}
	meta.Story = &metaid.StoryMetadata{
		Platform:  types.Messenger.String(),
		StoryID:   evt.storyItem.ID,
		ReelID:    evt.bucketID,
		AuthorID:  string(evt.sender),
		PostedAt:  postedAt,
		ExpiresAt: expiresAt,
		CanReply:  ptr.Ptr(evt.storyItem.StoryCardInfo.CanViewerTextReply),
		Body:      part.Content.Body,
		OwnerName: evt.ownerName,
		Expired:   false,
	}
	if evt.storyURL != "" {
		part.Extra["external_url"] = evt.storyURL
	}
	return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{part}}, nil
}

func (evt *MessengerStoryEvent) GetTimestamp() time.Time {
	return evt.timestamp
}

func (evt *MessengerStoryEvent) GetStreamOrder() int64 {
	return evt.timestamp.UnixMilli()
}

func (evt *MessengerStoryEvent) PostHandle(ctx context.Context, portal *bridgev2.Portal) {
	if evt.mc == nil || evt.mc.Main == nil {
		return
	}
	evt.mc.Main.trackStoryExpiry(ctx, portal, evt.messageID)
}

type MessengerStoryPoller struct {
	mc      *MetaClient
	cancel  context.CancelFunc
	running atomic.Bool

	cacheMu     sync.Mutex
	seenStories map[string]time.Time
	bucketCache map[string]messengerBucketState
}

const messengerStoryLifetime = 25 * time.Hour

type messengerBucketState struct {
	latestTimestamp int64
	expiresAt       time.Time
}

func NewMessengerStoryPoller(mc *MetaClient) *MessengerStoryPoller {
	return &MessengerStoryPoller{
		mc:          mc,
		seenStories: make(map[string]time.Time),
		bucketCache: make(map[string]messengerBucketState),
	}
}

func (sp *MessengerStoryPoller) Platform() types.Platform {
	return types.Messenger
}

func (sp *MessengerStoryPoller) Start(ctx context.Context) {
	if sp.mc == nil || !sp.mc.Main.Config.Stories.Enabled || !sp.mc.LoginMeta.Platform.IsMessenger() {
		return
	}
	if sp.running.Swap(true) {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	sp.cancel = cancel
	go sp.loop(ctx)
}

func (sp *MessengerStoryPoller) Stop() {
	if cancel := sp.cancel; cancel != nil {
		cancel()
	}
}

func (sp *MessengerStoryPoller) loop(ctx context.Context) {
	defer sp.running.Store(false)
	interval := sp.mc.Main.Config.Stories.PollInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if !sp.mc.Main.Config.Stories.SkipPollOnStart {
		sp.poll(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sp.poll(ctx)
		}
	}
}

func (sp *MessengerStoryPoller) poll(ctx context.Context) {
	cli := sp.mc.Client
	if cli == nil || cli.Facebook == nil {
		return
	}
	sp.pruneMessengerStoryCache()
	sp.mc.UserLogin.Log.Debug().Msg("Polling Messenger stories")
	var cursor *string
	stats := storyProcessStats{}
	for {
		tray, err := cli.Facebook.FetchStoriesTray(ctx, cursor)
		if err != nil {
			sp.mc.UserLogin.Log.Err(err).Msg("Failed to fetch Messenger story tray")
			return
		}
		if tray == nil || tray.Data.Node.UnifiedStoriesBuckets.Edges == nil {
			break
		}
		for _, edge := range tray.Data.Node.UnifiedStoriesBuckets.Edges {
			bucketStats := sp.processBucket(ctx, edge)
			stats.total += bucketStats.total
			stats.delivered += bucketStats.delivered
			stats.skipped += bucketStats.skipped
		}
		if !tray.Data.Node.UnifiedStoriesBuckets.PageInfo.HasNextPage {
			break
		}
		next := tray.Data.Node.UnifiedStoriesBuckets.PageInfo.EndCursor
		cursor = &next
	}
	sp.mc.UserLogin.Log.Info().
		Int("stories_checked", stats.total).
		Int("stories_delivered", stats.delivered).
		Int("stories_skipped", stats.skipped).
		Msg("Messenger story poll completed")
}

func (sp *MessengerStoryPoller) processBucket(ctx context.Context, edge responses.FBStoryBucketEdge) storyProcessStats {
	stats := storyProcessStats{}
	cli := sp.mc.Client
	if cli == nil || cli.Facebook == nil {
		return stats
	}
	bucketCursorTS := sp.extractBucketCursorTimestamp(edge.Cursor)
	if sp.shouldSkipBucket(edge.Node.ID, bucketCursorTS) {
		sp.mc.UserLogin.Log.Debug().
			Str("bucket_id", edge.Node.ID).
			Msg("Skipping Messenger story bucket without changes")
		return stats
	}
	resp, err := cli.Facebook.FetchStoryBuckets(ctx, []string{edge.Node.ID})
	if err != nil {
		sp.mc.UserLogin.Log.Err(err).
			Str("bucket_id", edge.Node.ID).
			Msg("Failed to fetch Messenger story bucket")
		return stats
	}
	if resp == nil || len(resp.Data.Nodes) == 0 {
		return stats
	}
	bucket := resp.Data.Nodes[0]
	items := bucket.UnifiedStoriesWithNotes.Edges
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Node.CreationTime < items[j].Node.CreationTime
	})
	allStoriesSeen := true
	var latestExpiry time.Time
	for _, storyEdge := range items {
		stats.total++
		if storyEdge.Node.ID == "" {
			stats.skipped++
			continue
		}
		expiry := sp.storyExpiration(storyEdge.Node)
		if expiry.After(latestExpiry) {
			latestExpiry = expiry
		}
		if sp.isStorySeen(storyEdge.Node.ID) {
			stats.skipped++
			continue
		}
		if delivered, reason := sp.bridgeStory(ctx, bucket, storyEdge.Node); delivered {
			stats.delivered++
			sp.markStorySeen(storyEdge.Node)
		} else {
			stats.skipped++
			if reason != "" {
				sp.mc.UserLogin.Log.Debug().
					Str("fb_user_id", bucket.StoryBucketOwner.ID).
					Str("story_id", storyEdge.Node.ID).
					Str("reason", reason).
					Msg("Skipping Messenger story")
			}
			allStoriesSeen = false
		}
	}
	if allStoriesSeen {
		sp.markBucketSeen(bucket.ID, bucketCursorTS, latestExpiry)
	}
	return stats
}

// ... (rest of file continues with InstagramStoryEvent definition)
