package responses

type FBVerifyThreadCapabilitiesResponse struct {
	Data struct {
		User *FBVerifyThreadCapabilitiesUser `json:"user"`
	} `json:"data"`
}

type FBVerifyThreadCapabilitiesUser struct {
	MessageCapabilities2Str string `json:"message_capabilities2_str"`
	ID                      string `json:"id"`
}

type FBStoryReplyResponse struct {
	Data struct {
		DirectMessageReply *FBStoryReplyPayload `json:"direct_message_reply"`
	} `json:"data"`
}

type FBStoryReplyPayload struct {
	ClientMutationID string            `json:"client_mutation_id"`
	Story            *FBStoryReplyItem `json:"story"`
}

type FBStoryReplyItem struct {
	ID string `json:"id"`
}
