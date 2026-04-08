package messagix

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"go.mau.fi/mautrix-meta/pkg/messagix/data/responses"
)

type fbVerifyThreadCapabilitiesVariables struct {
	ID string `json:"id"`
}

type FacebookStoryReplyInput struct {
	AttributionID    string `json:"attribution_id_v2"`
	Message          string `json:"message,omitempty"`
	StoryID          string `json:"story_id"`
	StoryReplyType   string `json:"story_reply_type"`
	ActorID          string `json:"actor_id"`
	ClientMutationID string `json:"client_mutation_id"`
}

type fbStoryReplyVariables struct {
	Input FacebookStoryReplyInput `json:"input"`
}

func (fb *FacebookMethods) VerifyContactCapabilities(ctx context.Context, userID string) (uint64, error) {
	if userID == "" {
		return 0, fmt.Errorf("user id missing for capability lookup")
	}
	if fb == nil || fb.client == nil {
		return 0, fmt.Errorf("facebook client unavailable")
	}
	vars := &fbVerifyThreadCapabilitiesVariables{ID: userID}
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
	vars := &fbStoryReplyVariables{Input: *input}
	_, data, err := fb.client.makeGraphQLRequest(ctx, "FBStoriesSendReply", vars)
	if err != nil {
		return "", err
	}
	var resp responses.FBStoryReplyResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("failed to decode story reply response: %w", err)
	}
	if resp.Data.DirectMessageReply == nil {
		return "", fmt.Errorf("story reply response missing data")
	}
	return resp.Data.DirectMessageReply.ClientMutationID, nil
}
