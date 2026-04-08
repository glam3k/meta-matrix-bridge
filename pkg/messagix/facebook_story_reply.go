package messagix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-meta/pkg/messagix/data/responses"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

var storyAttributionRegex = regexp.MustCompile(`StoriesCometSuspenseRoot\.react,comet\.stories\.viewer,[^"\\]+`)

const storyAttributionCacheTTL = 30 * time.Minute

type fbVerifyThreadCapabilitiesVariables struct {
	ID string `json:"id"`
}

type FacebookStoryReplyInput struct {
	AttributionID    string `json:"attribution_id_v2"`
	Message          string `json:"message,omitempty"`
	StoryID          string `json:"story_id"`
	StoryReelID      string `json:"story_reel_id,omitempty"`
	ThreadID         string `json:"thread_id,omitempty"`
	StoryReplyType   string `json:"story_reply_type"`
	ActorID          string `json:"actor_id"`
	ClientMutationID string `json:"client_mutation_id"`
}

type fbStoryReplyVariables struct {
	Input FacebookStoryReplyInput `json:"input"`
}

func (fb *FacebookMethods) getStoryAttributionID(ctx context.Context) (string, error) {
	if fb == nil || fb.client == nil {
		return "", fmt.Errorf("facebook client unavailable")
	}
	fb.storyAttrMu.Lock()
	cached := fb.storyAttributionID
	validUntil := fb.storyAttributionSeen.Add(storyAttributionCacheTTL)
	fb.storyAttrMu.Unlock()
	if cached != "" && time.Now().Before(validUntil) {
		return cached, nil
	}
	headers := fb.client.buildHeaders(true, true)
	storiesURL := fb.client.GetEndpoint("base_url") + "/stories"
	_, body, err := fb.client.MakeRequest(ctx, storiesURL, "GET", headers, nil, types.NONE)
	if err != nil {
		return "", fmt.Errorf("failed to fetch stories bootstrap: %w", err)
	}
	body = bytes.TrimPrefix(body, antiJSPrefix)
	if fb.client.Logger.GetLevel() <= zerolog.DebugLevel {
		preview := body
		if len(preview) > 1024 {
			preview = preview[:1024]
		}
		fb.client.Logger.Debug().Str("stories_bootstrap_sample", string(preview)).Msg("Fetched stories bootstrap")
	}
	match := storyAttributionRegex.Find(body)
	attr := ""
	if match != nil {
		attr = html.UnescapeString(string(match))
	} else {
		fb.client.Logger.Warn().Msg("Stories bootstrap did not include attribution ID, sending replies without it")
	}
	fb.storyAttrMu.Lock()
	fb.storyAttributionID = attr
	fb.storyAttributionSeen = time.Now()
	fb.storyAttrMu.Unlock()
	return attr, nil
}

func (fb *FacebookMethods) VerifyContactCapabilities(ctx context.Context, userID string) (uint64, error) {
	if userID == "" {
		return 0, fmt.Errorf("user id missing for capability lookup")
	}
	if fb == nil || fb.client == nil {
		return 0, fmt.Errorf("facebook client unavailable")
	}
	vars := &fbVerifyThreadCapabilitiesVariables{ID: userID}
	if fb.client.Logger.GetLevel() <= zerolog.DebugLevel {
		if payload, err := json.Marshal(vars); err == nil {
			fb.client.Logger.Debug().RawJSON("story_capability_vars", payload).Msg("Fetching Messenger story capabilities")
		}
	}
	_, data, err := fb.client.makeGraphQLRequest(ctx, "FBVerifyThreadContactCapabilities", vars)
	if err != nil {
		return 0, err
	}
	var resp responses.FBVerifyThreadCapabilitiesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, fmt.Errorf("failed to decode capability response: %w", err)
	}
	if resp.Data.User == nil {
		return 0, fmt.Errorf("capability response missing user block")
	}
	capStr := resp.Data.User.MessageCapabilities2Str
	if capStr == "" {
		return 0, fmt.Errorf("capability response missing value")
	}
	capabilities, err := strconv.ParseUint(capStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse capability mask: %w", err)
	}
	return capabilities, nil
}

func (fb *FacebookMethods) SendStoryReply(ctx context.Context, input *FacebookStoryReplyInput) (string, error) {
	if input == nil {
		return "", fmt.Errorf("story reply input missing")
	}
	if fb == nil || fb.client == nil {
		return "", fmt.Errorf("facebook client unavailable")
	}
	if input.AttributionID == "" {
		attr, err := fb.getStoryAttributionID(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get story attribution: %w", err)
		}
		input.AttributionID = attr
	}
	vars := &fbStoryReplyVariables{Input: *input}
	if fb.client.Logger.GetLevel() <= zerolog.DebugLevel {
		if payload, err := json.Marshal(vars); err == nil {
			fb.client.Logger.Debug().RawJSON("story_reply_vars", payload).Msg("Sending Messenger story reply")
		}
	}
	_, data, err := fb.client.makeGraphQLRequest(ctx, "FBStoriesSendReply", vars)
	if err != nil {
		return "", err
	}
	var resp responses.FBStoryReplyResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("failed to decode story reply response: %w", err)
	}
	if resp.Data.DirectMessageReply == nil {
		if fb.client != nil {
			fb.client.Logger.Warn().RawJSON("response", data).Msg("Messenger story reply response missing data")
		}
		return "", fmt.Errorf("story reply response missing data")
	}
	return resp.Data.DirectMessageReply.ClientMutationID, nil
}
