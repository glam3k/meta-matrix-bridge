package messagix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"go.mau.fi/mautrix-meta/pkg/messagix/data/responses"
)

type fbStoriesTrayVariables struct {
	ID                string   `json:"id"`
	BucketsCount      int      `json:"bucketsCount"`
	Cursor            *string  `json:"cursor"`
	HideSelfBucket    bool     `json:"hideSelfBucket"`
	ShowNavPane       bool     `json:"showNavPane"`
	StoriesTrayType   string   `json:"storiesTrayType"`
	Scale             int      `json:"scale"`
	PinnedIDs         []string `json:"pinnedIDs"`
	IsFbNotesIncluded bool     `json:"isFbNotesIncluded"`
}

type fbStoriesBucketsVariables struct {
	BucketIDs                         []string    `json:"bucketIDs"`
	Scale                             int         `json:"scale"`
	Blur                              int         `json:"blur"`
	ShouldEnableArmadilloStoryReply   bool        `json:"shouldEnableArmadilloStoryReply"`
	ShouldEnableLiveInStories         bool        `json:"shouldEnableLiveInStories"`
	FeedbackSource                    int         `json:"feedbackSource"`
	UseDefaultActor                   bool        `json:"useDefaultActor"`
	FeedLocation                      string      `json:"feedLocation"`
	FocusCommentID                    interface{} `json:"focusCommentID"`
	ShouldDeferLoad                   bool        `json:"shouldDeferLoad"`
	IsStoriesArchive                  bool        `json:"isStoriesArchive"`
	IsFbNotesIncluded                 bool        `json:"isFbNotesIncluded"`
	IncludeFbNotesProvider            bool        `json:"__relay_internal__pv__StoriesShouldIncludeFbNotesrelayprovider"`
	ProfessionalCard3DProvider        bool        `json:"__relay_internal__pv__StoriesProfessionalCard3DEnabledrelayprovider"`
	MenuEntryPointProvider            bool        `json:"__relay_internal__pv__StoriesThreeDotsMenuEntryPoint_enable_entrypoint_qerelayprovider"`
	BakedInTextStoriesProvider        bool        `json:"__relay_internal__pv__ShouldEnableBakedInTextStoriesrelayprovider"`
	MenuRelay3DProvider               bool        `json:"__relay_internal__pv__StoriesThreeDotsMenuRelay3D_enable_relay3d_qerelayprovider"`
	CommentAutoTranslationType        string      `json:"__relay_internal__pv__CometUFICommentAutoTranslationTyperelayprovider"`
	CommentAvatarStickerProvider      bool        `json:"__relay_internal__pv__CometUFICommentAvatarStickerAnimatedImagerelayprovider"`
	CommentActionLinksRewriteProvider bool        `json:"__relay_internal__pv__CometUFICommentActionLinksRewriteEnabledrelayprovider"`
	IsWorkUserProvider                bool        `json:"__relay_internal__pv__IsWorkUserrelayprovider"`
}

func (fb *FacebookMethods) FetchStoriesTray(ctx context.Context, cursor *string) (*responses.FBStoriesTrayResponse, error) {
	c := fb.client
	if c == nil || c.configs == nil {
		return nil, fmt.Errorf("client not ready")
	}
	vars := &fbStoriesTrayVariables{
		ID:                c.configs.BrowserConfigTable.CurrentUserInitialData.UserID,
		BucketsCount:      9,
		Cursor:            cursor,
		HideSelfBucket:    false,
		ShowNavPane:       true,
		StoriesTrayType:   "TOP_OF_FEED_TRAY",
		Scale:             2,
		PinnedIDs:         []string{""},
		IsFbNotesIncluded: true,
	}
	_, data, err := c.makeGraphQLRequest(ctx, "FBStoriesTray", vars)
	if err != nil {
		return nil, err
	}
	var resp responses.FBStoriesTrayResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode tray response: %w", err)
	}
	return &resp, nil
}

func (fb *FacebookMethods) FetchStoryBuckets(ctx context.Context, bucketIDs []string) (*responses.FBStoriesBucketResponse, error) {
	if len(bucketIDs) == 0 {
		return nil, fmt.Errorf("no bucket IDs provided")
	}
	vars := &fbStoriesBucketsVariables{
		BucketIDs:                         bucketIDs,
		Scale:                             2,
		Blur:                              20,
		ShouldEnableArmadilloStoryReply:   true,
		ShouldEnableLiveInStories:         true,
		FeedbackSource:                    65,
		UseDefaultActor:                   false,
		FeedLocation:                      "COMET_MEDIA_VIEWER",
		FocusCommentID:                    nil,
		ShouldDeferLoad:                   false,
		IsStoriesArchive:                  false,
		IsFbNotesIncluded:                 true,
		IncludeFbNotesProvider:            true,
		ProfessionalCard3DProvider:        true,
		MenuEntryPointProvider:            true,
		BakedInTextStoriesProvider:        false,
		MenuRelay3DProvider:               true,
		CommentAutoTranslationType:        "ORIGINAL",
		CommentAvatarStickerProvider:      false,
		CommentActionLinksRewriteProvider: false,
		IsWorkUserProvider:                false,
	}
	_, data, err := fb.client.makeGraphQLRequest(ctx, "FBStoriesViewer", vars)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var resp responses.FBStoriesBucketResponse
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode story bucket response: %w", err)
	}
	return &resp, nil
}
