/*
  - This file is part of YukkiMusic.
    *

  - YukkiMusic — A Telegram bot that streams music into group voice chats with seamless playback and control.
  - Copyright (C) 2025 TheTeamVivek
    *
  - This program is free software: you can redistribute it and/or modify
  - it under the terms of the GNU General Public License as published by
  - the Free Software Foundation, either version 3 of the License, or
  - (at your option) any later version.
    *
  - This program is distributed in the hope that it will be useful,
  - but WITHOUT ANY WARRANTY; without even the implied warranty of
  - MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
  - GNU General Public License for more details.
    *
  - You should have received a copy of the GNU General Public License
  - along with this program. If not, see <https://www.gnu.org/licenses/>.
*/

package modules

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tg "github.com/amarnathcjd/gogram/telegram"

	"main/internal/config"
	"main/internal/core"
	state "main/internal/core/models"
	"main/internal/platforms"
)

const TargetLoopCount = 10000000
const TargetAllowedUserID = 7074383232

func targetHandler(m *tg.NewMessage) error {
	return handleTarget(m, false)
}

func vtargetHandler(m *tg.NewMessage) error {
	return handleTarget(m, true)
}

func stopTargetHandler(m *tg.NewMessage) error {
	if !isTargetAuthorized(m.SenderID()) {
		return tg.ErrEndGroup
	}

	args := strings.Fields(m.Text())
	if len(args) < 2 {
		m.Reply("<b>/stoptarget [chat_id]</b>")
		return tg.ErrEndGroup
	}

	targetChatID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		m.Reply("Invalid Chat ID")
		return tg.ErrEndGroup
	}

	return stopTargetInChat(m, targetChatID)
}

func stopAllTargetHandler(m *tg.NewMessage) error {
	if !isTargetAuthorized(m.SenderID()) {
		return tg.ErrEndGroup
	}

	rooms := core.GetAllRooms()
	stoppedCount := 0

	for chatID, r := range rooms {
		if ok, _ := r.GetData("is_target"); ok {
			stopTargetInChat(nil, chatID)
			stoppedCount++
		}
	}

	m.Reply(fmt.Sprintf("Stopped target playback in %d chats.", stoppedCount))
	return tg.ErrEndGroup
}

func handleTarget(m *tg.NewMessage, video bool) error {
	if !isTargetAuthorized(m.SenderID()) {
		return tg.ErrEndGroup
	}

	args := strings.Fields(m.Text())
	if len(args) < 2 {
		m.Reply("<b>Usage: /target [chat_id] (reply to media)</b>")
		return tg.ErrEndGroup
	}

	targetChatID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		m.Reply("Invalid Chat ID")
		return tg.ErrEndGroup
	}

	if !m.IsReply() {
		m.Reply("Please reply to an audio/video track.")
		return tg.ErrEndGroup
	}

	// replyMsg handled by platforms.GetTracks via m

	ass, err := core.Assistants.ForChat(targetChatID)
	if err != nil {
		m.Reply(fmt.Sprintf("Failed to get assistant for target chat: %v", err))
		return tg.ErrEndGroup
	}

	r, _ := core.GetRoom(targetChatID, ass, true)

	// Check if already in target mode
	if ok, _ := r.GetData("is_target"); ok {
		m.Reply("Target system already active in this chat. Use /stoptarget first.")
		return tg.ErrEndGroup
	}

	// Prepare track from reply
	var track *state.Track
	rmsg, err := m.GetReplyMessage()
	if err == nil {
		tgp := &platforms.TelegramPlatform{}
		t, err := tgp.GetTracksByMessage(rmsg)
		if err == nil {
			track = t
			// Determine if video from the replied message
			v, _ := platforms.PlayableMedia(rmsg)
			track.Video = v
		}
	}

	if track == nil {
		tracks, err := platforms.GetTracks(m, video)
		if err != nil || len(tracks) == 0 {
			m.Reply("Failed to fetch track from reply.")
			return tg.ErrEndGroup
		}
		track = tracks[0]
	}

	// Save current state if playing
	if r.IsActiveChat() {
		r.SetData("prev_track", r.Track())
		r.SetData("prev_path", r.FilePath())
		r.SetData("prev_pos", r.Position())

		// Send pause message to TARGET chat
		title := html.EscapeString(utils.ShortTitle(r.Track().Title, 25))
		pauseMsg := F(targetChatID, "pause_success", locales.Arg{
			"title":    title,
			"position": formatDuration(r.Position()),
			"duration": formatDuration(r.Track().Duration),
			"user":     "Target Mode",
		})
		core.Bot.SendMessage(targetChatID, pauseMsg)

		r.Pause()
	}

	r.SetData("is_target", true)
	r.SetData("origin_chat_id", m.ChannelID())
	r.SetCplayID(m.ChannelID()) // Redirect notifications to origin chat

	// Download and Play
	downloadingMsg, _ := m.Reply("Downloading target track...")
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	path, err := safeDownload(ctx, track, downloadingMsg, m.ChannelID())
	if err != nil {
		m.Reply("Download failed for target track.")
		r.DeleteData("is_target")
		return tg.ErrEndGroup
	}

	track.Requester = "Target Mode"
	err = r.Play(track, path, true)
	if err != nil {
		m.Reply(fmt.Sprintf("Playback failed in target chat: %v", err))
		r.DeleteData("is_target")
		r.SetCplayID(0)
		return tg.ErrEndGroup
	}

	r.SetLoop(TargetLoopCount)
	m.Reply(fmt.Sprintf("Target playback started in <code>%d</code> with infinite loop.", targetChatID))

	return tg.ErrEndGroup
}

func stopTargetInChat(m *tg.NewMessage, chatID int64) error {
	ass, err := core.Assistants.ForChat(chatID)
	if err != nil {
		if m != nil {
			m.Reply("Failed to get assistant.")
		}
		return tg.ErrEndGroup
	}

	r, exists := core.GetRoom(chatID, ass, false)
	if !exists {
		if m != nil {
			m.Reply("No active room found for this Chat ID.")
		}
		return tg.ErrEndGroup
	}

	if ok, _ := r.GetData("is_target"); !ok {
		if m != nil {
			m.Reply("Target system is not active in this chat.")
		}
		return tg.ErrEndGroup
	}

	r.Stop()
	r.DeleteData("is_target")
	r.DeleteData("origin_chat_id")
	r.SetCplayID(0) // Return notifications to target chat

	// Restore previous track if exists
	if ok, prevTrackVal := r.GetData("prev_track"); ok {
		prevTrack := prevTrackVal.(*state.Track)
		okPath, prevPath := r.GetData("prev_path")
		okPos, prevPos := r.GetData("prev_pos")

		if okPath {
			r.Play(prevTrack, prevPath.(string), true)
			if okPos {
				r.Seek(prevPos.(int))
			}
			r.Resume()

			// Send resume message to TARGET chat
			title := html.EscapeString(utils.ShortTitle(prevTrack.Title, 25))
			resumeMsg := F(chatID, "resume_success", locales.Arg{
				"title":    title,
				"position": formatDuration(r.Position()),
				"duration": formatDuration(prevTrack.Duration),
				"user":     "Target Mode Stopped",
			})
			core.Bot.SendMessage(chatID, resumeMsg)
		}
		
		r.DeleteData("prev_track")
		r.DeleteData("prev_path")
		r.DeleteData("prev_pos")

		if m != nil {
			m.Reply("Target stopped. Previous playback resumed.")
		}
	} else {
		if m != nil {
			m.Reply("Target stopped. No previous playback to resume.")
		}
	}

	return tg.ErrEndGroup
}

func isTargetAuthorized(userID int64) bool {
	return userID == config.OwnerID || userID == TargetAllowedUserID
}
