package connector

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
	"time"

	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"

	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	RawMode string         `yaml:"mode"`
	Mode    types.Platform `yaml:"-"`

	RawAllowedModes []string         `yaml:"allowed_modes"`
	AllowedModes    []types.Platform `yaml:"-"`

	AllowMessengerComOnFB bool `yaml:"allow_messenger_com_on_fb"`

	Proxy        string `yaml:"proxy"`
	GetProxyFrom string `yaml:"get_proxy_from"`
	ProxyMedia   bool   `yaml:"proxy_media"`
	ProxyE2EE    bool   `yaml:"proxy_e2ee"`

	DisableXMABackfill bool `yaml:"disable_xma_backfill"`
	DisableXMAAlways   bool `yaml:"disable_xma_always"`

	MinFullReconnectIntervalSeconds int  `yaml:"min_full_reconnect_interval_seconds"`
	ForceRefreshIntervalSeconds     int  `yaml:"force_refresh_interval_seconds"`
	CacheConnectionState            bool `yaml:"cache_connection_state"`

	DisplaynameTemplate string             `yaml:"displayname_template"`
	displaynameTemplate *template.Template `yaml:"-"`

	// Only affects E2EE chats right now.
	SendPresenceOnTyping             bool `yaml:"send_presence_on_typing"`
	ReceiveInstagramTypingIndicators bool `yaml:"receive_instagram_typing_indicators"`
	DisableViewOnce                  bool `yaml:"disable_view_once"`
	MarketplaceSpace                 bool `yaml:"marketplace_space"`

	ThreadBackfill ThreadBackfillConfig `yaml:"thread_backfill"`
	Stories        StoriesConfig        `yaml:"stories"`
}

const defaultStoryPollInterval = 5 * time.Minute

type ThreadBackfillConfig struct {
	BatchCount int           `yaml:"batch_count"`
	BatchDelay time.Duration `yaml:"batch_delay"`
}

type StoriesConfig struct {
	Enabled         bool              `yaml:"enabled"`
	DefaultEnabled  bool              `yaml:"default_enabled"`
	PollInterval    time.Duration     `yaml:"poll_interval"`
	Helper          StoryHelperConfig `yaml:"helper"`
	SkipPollOnStart bool              `yaml:"skip_poll_on_start"`
	ExpiryMarker    StoryExpiryConfig `yaml:"expiry_marker"`
}

type StoryHelperConfig struct {
	BaseURL string        `yaml:"base_url"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
}

const defaultStoryHelperTimeout = 10 * time.Second

type StoryExpiryConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	CleanupDelay time.Duration `yaml:"cleanup_delay"`
}

const (
	defaultStoryExpiryPollInterval = 5 * time.Minute
	defaultStoryExpiryCleanupDelay = 48 * time.Hour
)

type umConfig Config

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	err := node.Decode((*umConfig)(c))
	if err != nil {
		return err
	}
	return c.PostProcess()
}

func (c *Config) PostProcess() (err error) {
	c.Mode = types.PlatformFromString(c.RawMode)
	c.AllowedModes = []types.Platform{}
	for _, rawMode := range c.RawAllowedModes {
		mode := types.PlatformFromString(rawMode)
		if !mode.IsValid() {
			return fmt.Errorf("unknown mode in allowed_modes: %q", rawMode)
		}
		if mode == types.FacebookTor {
			return fmt.Errorf("cannot use facebook-tor in allowed_modes, set mode instead")
		}
		c.AllowedModes = append(c.AllowedModes, mode)
	}
	c.displaynameTemplate, err = template.New("displayname").Parse(c.DisplaynameTemplate)
	if c.Stories.PollInterval == 0 {
		c.Stories.PollInterval = defaultStoryPollInterval
	}
	if c.Stories.Helper.Timeout == 0 {
		c.Stories.Helper.Timeout = defaultStoryHelperTimeout
	}
	if c.Stories.ExpiryMarker.PollInterval == 0 {
		c.Stories.ExpiryMarker.PollInterval = defaultStoryExpiryPollInterval
	}
	if c.Stories.ExpiryMarker.CleanupDelay == 0 {
		c.Stories.ExpiryMarker.CleanupDelay = defaultStoryExpiryCleanupDelay
	}
	return err
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "mode")
	helper.Copy(up.Bool, "allow_messenger_com_on_fb")
	helper.Copy(up.List, "allowed_modes")
	helper.Copy(up.Str, "displayname_template")
	helper.Copy(up.Str|up.Null, "proxy")
	helper.Copy(up.Str|up.Null, "get_proxy_from")
	helper.Copy(up.Bool, "proxy_media")
	helper.Copy(up.Bool, "proxy_e2ee")
	helper.Copy(up.Int, "min_full_reconnect_interval_seconds")
	helper.Copy(up.Int, "force_refresh_interval_seconds")
	helper.Copy(up.Bool, "cache_connection_state")
	helper.Copy(up.Bool, "disable_xma_backfill")
	helper.Copy(up.Bool, "disable_xma_always")
	helper.Copy(up.Bool, "send_presence_on_typing")
	helper.Copy(up.Bool, "receive_instagram_typing_indicators")
	helper.Copy(up.Bool, "disable_view_once")
	helper.Copy(up.Bool, "marketplace_space")
	helper.Copy(up.Int, "thread_backfill", "batch_count")
	helper.Copy(up.Str|up.Int, "thread_backfill", "batch_delay")
	helper.Copy(up.Bool, "stories", "enabled")
	helper.Copy(up.Bool, "stories", "default_enabled")
	helper.Copy(up.Str|up.Int, "stories", "poll_interval")
	helper.Copy(up.Bool, "stories", "skip_poll_on_start")
	helper.Copy(up.Str|up.Null, "stories", "helper", "base_url")
	helper.Copy(up.Str|up.Null, "stories", "helper", "token")
	helper.Copy(up.Str|up.Int, "stories", "helper", "timeout")
	helper.Copy(up.Bool, "stories", "expiry_marker", "enabled")
	helper.Copy(up.Str|up.Int, "stories", "expiry_marker", "poll_interval")
	helper.Copy(up.Str|up.Int, "stories", "expiry_marker", "cleanup_delay")
}

func (m *MetaConnector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &m.Config, up.SimpleUpgrader(upgradeConfig)
}

func (m *MetaConnector) ValidateConfig() error {
	if m.Config.Mode == types.Unset && m.Config.RawMode != "" {
		return fmt.Errorf("invalid mode %q", m.Config.RawMode)
	}
	return nil
}

type DisplaynameParams struct {
	DisplayName string
	Username    string
	ID          int64
}

func (c *Config) FormatDisplayname(params DisplaynameParams) string {
	var buffer strings.Builder
	_ = c.displaynameTemplate.Execute(&buffer, params)
	return buffer.String()
}
