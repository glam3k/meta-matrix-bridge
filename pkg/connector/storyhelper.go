package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

type storyHelperClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	log        zerolog.Logger
	timeout    time.Duration
}

type storyHelperRequest struct {
	StoryURL  string              `json:"story_url"`
	Message   string              `json:"message"`
	Cookies   []storyHelperCookie `json:"cookies"`
	TimeoutMS int                 `json:"timeout_ms,omitempty"`
	TraceID   string              `json:"trace_id,omitempty"`
}

type storyHelperCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
	Secure bool   `json:"secure"`
}

type storyHelperResponse struct {
	Status  string `json:"status"`
	StoryID string `json:"story_id,omitempty"`
	Details string `json:"details,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func newStoryHelperClient(cfg StoryHelperConfig, log zerolog.Logger) *storyHelperClient {
	if cfg.BaseURL == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultStoryHelperTimeout
	}
	return &storyHelperClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		token:   cfg.Token,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		log:     log,
		timeout: timeout,
	}
}

func (c *storyHelperClient) SendReply(ctx context.Context, req *storyHelperRequest) (*storyHelperResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("story helper not configured")
	}
	if req == nil {
		return nil, fmt.Errorf("story helper request missing")
	}
	if req.TimeoutMS == 0 {
		req.TimeoutMS = int(c.timeout / time.Millisecond)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := c.baseURL + "/reply"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("X-Helper-Token", c.token)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("helper returned status %d", resp.StatusCode)
	}
	var helperResp storyHelperResponse
	if err := json.NewDecoder(resp.Body).Decode(&helperResp); err != nil {
		return nil, err
	}
	if helperResp.Status != "ok" {
		reason := helperResp.Reason
		if reason == "" {
			reason = "unknown"
		}
		return nil, fmt.Errorf("helper reported error: %s", reason)
	}
	return &helperResp, nil
}
