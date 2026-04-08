// mautrix-meta - A Matrix-Facebook Messenger and Instagram DM puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package msgconv

import (
	"context"
	"fmt"
	"strconv"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-meta/pkg/messagix/data/responses"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func (mc *MessageConverter) InstagramStoryItemToMatrix(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	messageID networkid.MessageID,
	item *responses.ReelItem,
) (*bridgev2.ConvertedMessagePart, error) {
	if item == nil {
		return nil, fmt.Errorf("story item missing")
	}
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	ctx = context.WithValue(ctx, contextKeyIntent, intent)
	ctx = context.WithValue(ctx, contextKeyMsgID, messageID)
	ctx = context.WithValue(ctx, contextKeyPartID, networkid.PartID("story"))

	refresh := &MediaRefreshMeta{
		StoryMediaID: item.Pk,
		ExpiresAt:    int64(item.ExpiringAt) * 1000,
	}
	if item.User.Pk != "" {
		refresh.StoryReelID = item.User.Pk
	}

	var (
		attachmentType table.AttachmentType
		url            string
		mime           string
		width          int
		height         int
		duration       int
	)
	if item.MediaType == 2 && len(item.VideoVersions) > 0 {
		attachmentType = table.AttachmentTypeVideo
		url, width, height = pickLargestVideo(item.VideoVersions)
		mime = "video/mp4"
		if item.VideoDuration > 0 {
			duration = int(item.VideoDuration * 1000)
		}
	} else {
		attachmentType = table.AttachmentTypeImage
		url, width, height = pickLargestImage(item.ImageVersions2.Candidates)
		mime = "image/jpeg"
	}
	if url == "" {
		return nil, fmt.Errorf("story media URL missing")
	}

	fileName := fmt.Sprintf("story_%s", item.Pk)
	converted, err := mc.reuploadAttachment(ctx, attachmentType, url, fileName, mime, 0, width, height, duration, refresh)
	if err != nil {
		return nil, err
	}
	return converted, nil
}

func pickLargestImage(candidates []responses.Candidates) (string, int, int) {
	var (
		url     string
		maxArea int
		width   int
		height  int
	)
	for _, candidate := range candidates {
		area := candidate.Width * candidate.Height
		if area > maxArea {
			maxArea = area
			url = candidate.URL
			width = candidate.Width
			height = candidate.Height
		}
	}
	return url, width, height
}

func pickLargestVideo(versions []responses.VideoVersions) (string, int, int) {
	var (
		url     string
		maxArea int
		width   int
		height  int
	)
	for _, version := range versions {
		area := version.Width * version.Height
		if area > maxArea {
			maxArea = area
			url = version.URL
			width = version.Width
			height = version.Height
		}
	}
	return url, width, height
}

func (mc *MessageConverter) MessengerStoryItemToMatrix(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	messageID networkid.MessageID,
	item *responses.FBStoryNode,
	bucketID string,
) (*bridgev2.ConvertedMessagePart, error) {
	if item == nil {
		return nil, fmt.Errorf("story item missing")
	}
	if len(item.Attachments) == 0 {
		return nil, fmt.Errorf("story attachment missing")
	}
	media := item.Attachments[0].Media
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	ctx = context.WithValue(ctx, contextKeyIntent, intent)
	ctx = context.WithValue(ctx, contextKeyMsgID, messageID)
	ctx = context.WithValue(ctx, contextKeyPartID, networkid.PartID("story"))

	refresh := &MediaRefreshMeta{
		StoryMediaID: item.ID,
		StoryReelID:  bucketID,
	}
	if expStr := item.StoryCardInfo.ReplyThreadExpirationTime; expStr != "" {
		if exp, err := strconv.ParseInt(expStr, 10, 64); err == nil {
			refresh.ExpiresAt = exp * 1000
		}
	}

	var (
		attachmentType table.AttachmentType
		url            string
		mime           string
		width          int
		height         int
		duration       int
	)
	if media.Typename == "Video" {
		attachmentType = table.AttachmentTypeVideo
		url = media.PlayableURL
		if url == "" && len(media.ProgressiveURLs) > 0 {
			url = media.ProgressiveURLs[0].ProgressiveURL
		}
		mime = "video/mp4"
		if media.Image != nil {
			width = media.Image.Width
			height = media.Image.Height
		}
		if media.PlayableDurationMS > 0 {
			duration = int(media.PlayableDurationMS)
		}
	} else {
		attachmentType = table.AttachmentTypeImage
		if media.Image != nil {
			url = media.Image.URI
			width = media.Image.Width
			height = media.Image.Height
		}
		mime = "image/jpeg"
	}
	if url == "" {
		return nil, fmt.Errorf("story media URL missing")
	}
	fileName := fmt.Sprintf("story_%s", item.ID)
	converted, err := mc.reuploadAttachment(ctx, attachmentType, url, fileName, mime, 0, width, height, duration, refresh)
	if err != nil {
		return nil, err
	}
	return converted, nil
}
