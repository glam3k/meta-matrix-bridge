package responses

type FBStoriesTrayResponse struct {
	Data struct {
		Node struct {
			UnifiedStoriesBuckets FBStoryBuckets `json:"unified_stories_buckets"`
		} `json:"node"`
	} `json:"data"`
}

type FBStoryBuckets struct {
	Edges    []FBStoryBucketEdge `json:"edges"`
	PageInfo FBPageInfo          `json:"page_info"`
}

type FBPageInfo struct {
	EndCursor   string `json:"end_cursor"`
	HasNextPage bool   `json:"has_next_page"`
}

type FBStoryBucketEdge struct {
	Cursor string        `json:"cursor"`
	Node   FBStoryBucket `json:"node"`
}

type FBStoryBucket struct {
	ID                   string       `json:"id"`
	StoryBucketType      string       `json:"story_bucket_type"`
	AudienceMode         string       `json:"audience_mode"`
	IsBucketSeenByViewer bool         `json:"is_bucket_seen_by_viewer"`
	StoryBucketOwner     FBStoryOwner `json:"story_bucket_owner"`
}

type FBStoryOwner struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	ShortName      string       `json:"short_name"`
	ProfilePicture FBMediaImage `json:"profile_picture"`
}

type FBMediaImage struct {
	URI    string `json:"uri"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type FBStoriesBucketResponse struct {
	Data struct {
		Nodes []FBStoriesBucketNode `json:"nodes"`
	} `json:"data"`
}

type FBStoriesBucketNode struct {
	ID                      string             `json:"id"`
	StoryBucketType         string             `json:"story_bucket_type"`
	StoryBucketOwner        FBStoryOwner       `json:"story_bucket_owner"`
	UnifiedStoriesWithNotes FBStoriesWithNotes `json:"unified_stories_with_notes"`
}

type FBStoriesWithNotes struct {
	Edges []FBStoryEdge `json:"edges"`
}

type FBStoryEdge struct {
	Node FBStoryNode `json:"node"`
}

type FBStoryNode struct {
	ID                     string              `json:"id"`
	StoryCardInfo          FBStoryCardInfo     `json:"story_card_info"`
	Attachments            []FBStoryAttachment `json:"attachments"`
	StoryOverlays          []FBStoryOverlay    `json:"story_overlays"`
	StoryDefaultBackground FBStoryBackground   `json:"story_default_background"`
	CreationTime           int64               `json:"creation_time"`
	Url                    string              `json:"url"`
}

type FBStoryCardInfo struct {
	CanViewerTextReply        bool             `json:"can_viewer_text_reply"`
	StoryPlayDuration         float64          `json:"story_play_duration"`
	StoryThumbnail            FBMediaImage     `json:"story_thumbnail"`
	PermalinkInfo             *FBPermalinkInfo `json:"permalink_info"`
	ReplyThreadExpirationTime string           `json:"reply_thread_expiration_time"`
	Bucket                    FBStoryBucketRef `json:"bucket"`
}

type FBPermalinkInfo struct {
	URI string `json:"uri"`
}

type FBStoryBucketRef struct {
	ID             string `json:"id"`
	CameraPostType string `json:"camera_post_type"`
}

type FBStoryAttachment struct {
	Media FBStoryMedia `json:"media"`
}

type FBStoryMedia struct {
	Typename           string        `json:"__typename"`
	ID                 string        `json:"id"`
	Image              *FBMediaImage `json:"image"`
	PreviewImage       *FBMediaImage `json:"previewImage"`
	PlayableURL        string        `json:"playable_url"`
	PlayableDurationMS int64         `json:"playable_duration_in_ms"`
	ProgressiveURLs    []FBVideoURL  `json:"progressive_urls"`
}

type FBVideoURL struct {
	ProgressiveURL string `json:"progressive_url"`
}

type FBStoryOverlay struct {
	Typename string `json:"__typename"`
}

type FBStoryBackground struct {
	Color string `json:"color"`
}
