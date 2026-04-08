package connector

import (
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"

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
	return false
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
