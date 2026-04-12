package connector

import (
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/util/ptr"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	"go.mau.fi/mautrix-meta/pkg/metaid"
)

var cmdToggleEncryption = &commands.FullHandler{
	Func: fnToggleEncryption,
	Name: "toggle-encryption",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Toggle Messenger-side encryption for the current room",
	},
	RequiresPortal: true,
	RequiresLogin:  true,
}

var cmdStorySettings = &commands.FullHandler{
	Func: fnStorySettings,
	Name: "stories",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Manage story delivery in this room",
	},
	RequiresPortal: true,
	RequiresLogin:  true,
}

var cmdStoriesGlobal = &commands.FullHandler{
	Func: fnStoriesGlobal,
	Name: "stories-global",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionGeneral,
		Description: "Toggle story polling for a login (bot management room)",
	},
	RequiresPortal: false,
	RequiresLogin:  true,
}

func fnToggleEncryption(ce *commands.Event) {
	conn := ce.Bridge.Network.(*MetaConnector)
	if !conn.Config.Mode.IsMessenger() {
		ce.Reply("Instagram does not support encryption")
		return
	} else if ce.Portal.RoomType != database.RoomTypeDM {
		ce.Reply("Only private chats can be toggled between encrypted and unencrypted")
		return
	}
	login, _, err := ce.Portal.FindPreferredLogin(ce.Ctx, ce.User, false)
	if err != nil {
		ce.Reply("Failed to find login for room")
		ce.Log.Err(err).Msg("Failed to find login for room")
		return
	}
	cli := login.Client.(*MetaClient)
	meta := ce.Portal.Metadata.(*metaid.PortalMetadata)
	if meta.ThreadType.IsWhatsApp() {
		meta.ThreadType = table.ONE_TO_ONE
		ce.Reply("Messages in this room will now be sent unencrypted over Messenger")
	} else {
		if len(ce.Args) == 0 || ce.Args[0] != "--force" {
			threadID := metaid.ParseFBPortalID(ce.Portal.ID)
			err = cli.CreateWhatsAppDM(ce.Ctx, threadID)
			if err != nil {
				ce.Log.Err(err).Msg("Failed to create WhatsApp thread")
				ce.Reply("Failed to create WhatsApp thread")
			}
		}
		meta.ThreadType = table.ENCRYPTED_OVER_WA_ONE_TO_ONE
		ce.Reply("Messages in this room will now be sent encrypted over WhatsApp")
	}
	err = ce.Portal.Save(ce.Ctx)
	if err != nil {
		ce.Log.Err(err).Msg("Failed to update portal in database")
	}
}

func fnStorySettings(ce *commands.Event) {
	conn := ce.Bridge.Network.(*MetaConnector)
	if !conn.Config.Stories.Enabled {
		ce.Reply("Story bridging is disabled in the bridge configuration")
		return
	}
	if len(ce.Args) == 0 {
		ce.Reply("Usage: !bridge stories <on|off|status> [@ghost]")
		return
	}
	action := strings.ToLower(ce.Args[0])
	login, _, err := ce.Portal.FindPreferredLogin(ce.Ctx, ce.User, false)
	if err != nil {
		ce.Log.Err(err).Msg("Failed to find login for command")
		ce.Reply("Failed to find login for this room")
		return
	} else if login == nil {
		ce.Reply("You must be logged in to manage story settings")
		return
	}
	loginPlatform := login.Metadata.(*metaid.UserLoginMetadata).Platform
	if !loginPlatform.IsInstagram() && !loginPlatform.IsMessenger() {
		ce.Reply("Stories can only be managed for Instagram or Messenger logins")
		return
	}
	platformArgIdx := 1
	platformName := ""
	if len(ce.Args) > 1 {
		candidate := strings.ToLower(ce.Args[1])
		if candidate == "all" {
			platformName = candidate
			platformArgIdx = 2
		} else if !strings.HasPrefix(candidate, "@") {
			platformName = candidate
			platformArgIdx = 2
		}
	}
	var targetPlatform types.Platform
	applyToAll := false
	if platformName == "all" {
		applyToAll = true
	} else if platformName != "" {
		targetPlatform = types.PlatformFromString(platformName)
		if !targetPlatform.IsValid() {
			ce.Reply("Unknown platform %s. Use instagram, messenger, or all.", platformName)
			return
		}
	}
	if !applyToAll && !targetPlatform.IsValid() {
		targetPlatform = loginPlatform
	}
	if !applyToAll && !storyPlatformAllowed(loginPlatform, targetPlatform) {
		ce.Reply("This login cannot manage %s stories", formatStoryPlatform(targetPlatform))
		return
	}
	target := ""
	if len(ce.Args) > platformArgIdx {
		target = ce.Args[platformArgIdx]
	}
	ghost, err := conn.resolveStoryGhost(ce, target)
	if err != nil {
		ce.Reply(err.Error())
		return
	}
	if ghost == nil {
		ce.Reply("Ghost user is not available in this room yet")
		return
	}
	if ghost.Intent == nil {
		ce.Reply("Ghost %s is not provisioned yet", ghost.ID)
		return
	}
	switch action {
	case "on", "enable", "yes", "true":
		err = conn.setStorySetting(ce.Ctx, ce.Portal, ghost.Intent.GetMXID(), targetPlatform, applyToAll, true)
		if err != nil {
			ce.Log.Err(err).Msg("Failed to enable stories")
			ce.Reply("Failed to enable stories for %s", ghost.Intent.GetMXID())
			return
		}
		if applyToAll {
			ce.Reply("Stories from %s enabled for all platforms in this room", ghost.Intent.GetMXID())
		} else {
			ce.Reply("%s stories from %s enabled in this room", formatStoryPlatform(targetPlatform), ghost.Intent.GetMXID())
		}
	case "off", "disable", "no", "false":
		err = conn.setStorySetting(ce.Ctx, ce.Portal, ghost.Intent.GetMXID(), targetPlatform, applyToAll, false)
		if err != nil {
			ce.Log.Err(err).Msg("Failed to disable stories")
			ce.Reply("Failed to disable stories for %s", ghost.Intent.GetMXID())
			return
		}
		if applyToAll {
			ce.Reply("Stories from %s disabled for all platforms in this room", ghost.Intent.GetMXID())
		} else {
			ce.Reply("%s stories from %s disabled in this room", formatStoryPlatform(targetPlatform), ghost.Intent.GetMXID())
		}
	case "status":
		settings, err := conn.getStorySettingsContent(ce.Ctx, ce.Portal, ghost.Intent.GetMXID())
		if err != nil {
			ce.Log.Err(err).Msg("Failed to read story settings")
			ce.Reply("Failed to read story settings")
			return
		}
		conn.replyStoryStatus(ce, ghost, settings, loginPlatform, targetPlatform, applyToAll)
	default:
		ce.Reply("Usage: !bridge stories <on|off|status> [platform] [@ghost]")
	}
}

func fnStoriesGlobal(ce *commands.Event) {
	conn := ce.Bridge.Network.(*MetaConnector)
	if !conn.Config.Stories.Enabled {
		ce.Reply("Story bridging is disabled in the bridge configuration")
		return
	}
	if len(ce.Args) == 0 {
		ce.Reply("Usage: !bridge stories-global <on|off|status> <login> [--reset-rooms[=<on|off>]]")
		return
	}
	action := strings.ToLower(ce.Args[0])
	opts, err := parseStoriesGlobalArgs(ce.Args[1:])
	if err != nil {
		ce.Reply(err.Error())
		return
	}
	login, err := conn.resolveStoriesLogin(ce, opts.loginArg)
	if err != nil {
		ce.Reply(err.Error())
		return
	}
	if login == nil {
		ce.Reply("You must be logged in to use this command")
		return
	}
	loginMeta := login.Metadata.(*metaid.UserLoginMetadata)
	if !loginMeta.Platform.IsInstagram() && !loginMeta.Platform.IsMessenger() {
		ce.Reply("Stories can only be managed for Instagram or Messenger logins")
		return
	}
	switch action {
	case "status":
		ce.Reply(conn.describeGlobalStoriesStatus(login))
	case "on", "enable", "yes", "true":
		conn.handleStoriesGlobalToggle(ce, login, true, opts)
	case "off", "disable", "no", "false":
		conn.handleStoriesGlobalToggle(ce, login, false, opts)
	default:
		ce.Reply("Usage: !bridge stories-global <on|off|status> <login> [--reset-rooms[=<on|off>]]")
	}
}

func (m *MetaConnector) resolveStoryGhost(ce *commands.Event, target string) (*bridgev2.Ghost, error) {
	if target == "" {
		if ce.Portal.RoomType != database.RoomTypeDM {
			return nil, fmt.Errorf("Specify which ghost user to manage in this room")
		}
		if ce.Portal.OtherUserID == "" {
			return nil, fmt.Errorf("Room is not linked to a DM user yet")
		}
		ghost, err := ce.Bridge.GetGhostByID(ce.Ctx, ce.Portal.OtherUserID)
		if err != nil {
			return nil, fmt.Errorf("Failed to load ghost: %w", err)
		}
		if ghost == nil {
			return nil, fmt.Errorf("Room ghost is not available yet")
		}
		return ghost, nil
	}
	mxid := id.UserID(strings.TrimSpace(target))
	ghost, err := ce.Bridge.GetGhostByMXID(ce.Ctx, mxid)
	if err != nil {
		return nil, fmt.Errorf("Failed to load ghost %s: %w", target, err)
	} else if ghost == nil {
		return nil, fmt.Errorf("Unknown ghost user %s", target)
	}
	return ghost, nil
}

func (m *MetaConnector) replyStoryStatus(ce *commands.Event, ghost *bridgev2.Ghost, settings *storySettingsContent, loginPlatform types.Platform, targetPlatform types.Platform, applyToAll bool) {
	if settings == nil {
		settings = newStorySettingsContent(m.Config.Stories.DefaultEnabled)
	}
	describe := func(val bool) string {
		if val {
			return "enabled"
		}
		return "disabled"
	}
	ghostMXID := ghost.Intent.GetMXID()
	if applyToAll {
		ce.Reply("Default stories from %s are %s in this room", ghostMXID, describe(settings.ReceiveStories))
		return
	}
	if targetPlatform.IsValid() && targetPlatform != loginPlatform {
		ce.Reply("%s stories from %s are %s in this room", formatStoryPlatform(targetPlatform), ghostMXID, describe(settings.enabledFor(targetPlatform)))
		return
	}
	lines := []string{fmt.Sprintf("%s stories from %s are %s in this room", formatStoryPlatform(loginPlatform), ghostMXID, describe(settings.enabledFor(loginPlatform)))}
	for platformStr, val := range settings.PlatformOverrides {
		platform := types.PlatformFromString(platformStr)
		if platform == loginPlatform {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s stories override: %s", formatStoryPlatform(platform), describe(val)))
	}
	ce.Reply(strings.Join(lines, "\n"))
}

func storyPlatformAllowed(loginPlatform, target types.Platform) bool {
	if target.IsInstagram() {
		return loginPlatform.IsInstagram()
	}
	if target.IsMessenger() {
		return loginPlatform.IsMessenger()
	}
	return true
}

func formatStoryPlatform(platform types.Platform) string {
	switch {
	case platform.IsInstagram():
		return "Instagram"
	case platform.IsMessenger():
		return "Messenger"
	default:
		return "Unknown"
	}
}

type storiesGlobalOptions struct {
	loginArg   string
	resetRooms bool
	resetValue *bool
}

func parseStoriesGlobalArgs(args []string) (storiesGlobalOptions, error) {
	opts := storiesGlobalOptions{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--reset-rooms="):
			opts.resetRooms = true
			val := strings.TrimPrefix(arg, "--reset-rooms=")
			if val != "" {
				parsed, err := parseOnOffValue(val)
				if err != nil {
					return opts, fmt.Errorf("invalid --reset-rooms value %q", val)
				}
				opts.resetValue = ptr.Ptr(parsed)
			}
		case arg == "--reset-rooms":
			opts.resetRooms = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				parsed, err := parseOnOffValue(args[i+1])
				if err != nil {
					return opts, fmt.Errorf("invalid --reset-rooms value %q", args[i+1])
				}
				opts.resetValue = ptr.Ptr(parsed)
				i++
			}
		case strings.HasPrefix(arg, "--"):
			return opts, fmt.Errorf("unknown flag %s", arg)
		default:
			if opts.loginArg == "" {
				opts.loginArg = arg
			} else {
				return opts, fmt.Errorf("too many positional arguments")
			}
		}
	}
	return opts, nil
}

func parseOnOffValue(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "on", "yes", "true", "enable", "enabled":
		return true, nil
	case "off", "no", "false", "disable", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("unknown on/off value %q", value)
	}
}

func (m *MetaConnector) resolveStoriesLogin(ce *commands.Event, loginArg string) (*bridgev2.UserLogin, error) {
	if loginArg == "" {
		if ce.Portal == nil {
			return nil, fmt.Errorf("Specify which login to manage")
		}
		login, _, err := ce.Portal.FindPreferredLogin(ce.Ctx, ce.User, false)
		if err != nil {
			return nil, fmt.Errorf("Failed to find login for this room")
		} else if login == nil {
			return nil, fmt.Errorf("You must be logged in to manage story settings")
		}
		return login, nil
	}
	logins := ce.User.GetUserLogins()
	if len(logins) == 0 {
		return nil, fmt.Errorf("You don't have any active logins")
	}
	loginArgLower := strings.ToLower(loginArg)
	var platformMatches []*bridgev2.UserLogin
	for _, login := range logins {
		if strings.EqualFold(string(login.ID), loginArg) || strings.EqualFold(login.RemoteName, loginArg) {
			return login, nil
		}
		meta := login.Metadata.(*metaid.UserLoginMetadata)
		if strings.EqualFold(meta.Platform.String(), loginArgLower) {
			platformMatches = append(platformMatches, login)
		}
	}
	switch len(platformMatches) {
	case 1:
		return platformMatches[0], nil
	case 0:
		return nil, fmt.Errorf("Unknown login %s", loginArg)
	default:
		return nil, fmt.Errorf("Multiple %s logins found. Specify the login ID.", loginArg)
	}
}

func (m *MetaConnector) describeGlobalStoriesStatus(login *bridgev2.UserLogin) string {
	configState := "enabled"
	if !m.Config.Stories.Enabled {
		configState = "disabled"
	}
	meta := login.Metadata.(*metaid.UserLoginMetadata)
	runtime := "following bridge config"
	if meta.StoriesGlobalOverride != nil {
		if *meta.StoriesGlobalOverride {
			runtime = "forced on"
		} else {
			runtime = "forced off"
		}
	}
	return fmt.Sprintf("Stories for %s are %s (bridge config: %s)", login.RemoteName, runtime, configState)
}

func (m *MetaConnector) handleStoriesGlobalToggle(ce *commands.Event, login *bridgev2.UserLogin, enable bool, opts storiesGlobalOptions) {
	meta := login.Metadata.(*metaid.UserLoginMetadata)
	if meta.StoriesGlobalOverride != nil && *meta.StoriesGlobalOverride == enable {
		ce.Reply("Stories are already %s for %s", describeEnableState(enable), login.RemoteName)
		return
	}
	meta.StoriesGlobalOverride = ptr.Ptr(enable)
	if err := login.Save(ce.Ctx); err != nil {
		ce.Log.Err(err).Msg("Failed to save global story override")
		ce.Reply("Failed to update story override: %v", err)
		return
	}
	metaClient := login.Client.(*MetaClient)
	if enable {
		metaClient.startStoryPollers(ce.Ctx)
	} else {
		metaClient.stopStoryPollers()
	}
	resetMsg := ""
	if opts.resetRooms {
		resetValue := enable
		if opts.resetValue != nil {
			resetValue = *opts.resetValue
		}
		count, err := m.resetStorySettingsForLogin(ce.Ctx, login, resetValue)
		if err != nil {
			ce.Log.Err(err).Msg("Failed to reset story settings while toggling global stories")
			ce.Reply("Updated story override, but failed to reset rooms: %v", err)
			return
		}
		resetMsg = fmt.Sprintf(" Reset %d rooms.", count)
	}
	ce.Log.Info().
		Str("login_id", string(login.ID)).
		Bool("stories_enabled", enable).
		Bool("reset_rooms", opts.resetRooms).
		Msg("Updated global stories override")
	ce.Reply("Stories for %s %s.%s", login.RemoteName, describeEnableState(enable), resetMsg)
}

func describeEnableState(enable bool) string {
	if enable {
		return "will now be polled"
	}
	return "have polling disabled"
}
