package connector

import (
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
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
		Description: "Manage Instagram story delivery in this room",
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
	if !login.Metadata.(*metaid.UserLoginMetadata).Platform.IsInstagram() {
		ce.Reply("Stories can only be managed for Instagram logins")
		return
	}
	target := ""
	if len(ce.Args) > 1 {
		target = ce.Args[1]
	}
	ghost, err := conn.resolveStoryGhost(ce, target)
	if err != nil {
		ce.Reply(err.Error())
		return
	}
	switch action {
	case "on", "enable", "yes", "true":
		err = conn.setStorySetting(ce.Ctx, ce.Portal, ghost.Intent.GetMXID(), true)
		if err != nil {
			ce.Log.Err(err).Msg("Failed to enable stories")
			ce.Reply("Failed to enable stories for %s", ghost.Intent.GetMXID())
			return
		}
		ce.Reply("Stories from %s enabled in this room", ghost.Intent.GetMXID())
	case "off", "disable", "no", "false":
		err = conn.setStorySetting(ce.Ctx, ce.Portal, ghost.Intent.GetMXID(), false)
		if err != nil {
			ce.Log.Err(err).Msg("Failed to disable stories")
			ce.Reply("Failed to disable stories for %s", ghost.Intent.GetMXID())
			return
		}
		ce.Reply("Stories from %s disabled in this room", ghost.Intent.GetMXID())
	case "status":
		conn.replyStoryStatus(ce, ghost)
	default:
		ce.Reply("Usage: !bridge stories <on|off|status> [@ghost]")
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

func (m *MetaConnector) replyStoryStatus(ce *commands.Event, ghost *bridgev2.Ghost) {
	enabled, err := m.getStorySetting(ce.Ctx, ce.Portal, ghost.Intent.GetMXID())
	if err != nil {
		ce.Log.Err(err).Msg("Failed to read story settings")
		ce.Reply("Failed to read story settings")
		return
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	ce.Reply("Stories from %s are %s in this room", ghost.Intent.GetMXID(), state)
}
