/*
 * This file is part of YukkiMusic.
 *
 * YukkiMusic — A Telegram bot that streams music into group voice chats with seamless playback and control.
 * Copyright (C) 2025 TheTeamVivek
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */
package platforms

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Laky-64/gologging"
	"github.com/amarnathcjd/gogram/telegram"

	"main/internal/core"
	state "main/internal/core/models"
	"main/internal/utils"
)

const (
	PlatformEraApi  state.PlatformName = "EraApi"
	eraApiTryURL                       = "https://eytapi02-fd074525a32f.herokuapp.com/try"
	eraApiStatusURL                    = "https://eytapi02-fd074525a32f.herokuapp.com/status"
	eraMaxPolls                        = 600
)

type eraApiTryResponse struct {
	Link  string `json:"link"`
	JobID string `json:"job_id"`
}

type eraApiStatusResponse struct {
	Status string `json:"status"`
	Link   string `json:"link"`
}

type EraApiPlatform struct{}

func init() {
	Register(75, &EraApiPlatform{})
}

func (*EraApiPlatform) Name() state.PlatformName { return PlatformEraApi }

func (*EraApiPlatform) CanGetTracks(_ string) bool { return false }

func (*EraApiPlatform) GetTracks(_ string, _ bool) ([]*state.Track, error) {
	return nil, errors.New("eraapi is a download-only platform")
}

func (*EraApiPlatform) CanDownload(source state.PlatformName) bool {
	return source == PlatformYouTube
}

func (e *EraApiPlatform) Download(
	ctx context.Context,
	track *state.Track,
	mystic *telegram.NewMessage,
) (string, error) {
	track.Video = false

	if f := findFile(track); f != "" {
		gologging.Debug("EraApi: cache hit → " + f)
		return f, nil
	}

	tryResp, err := e.requestJob(ctx, track.URL)
	if err != nil {
		return "", fmt.Errorf("eraapi: request failed: %w", err)
	}

	dlLink := tryResp.Link
	if dlLink == "" {
		if tryResp.JobID == "" {
			return "", errors.New("eraapi: neither link nor job_id received")
		}
		gologging.InfoF("EraApi: polling job %s", tryResp.JobID)
		dlLink, err = e.pollJob(ctx, tryResp.JobID)
		if err != nil {
			return "", fmt.Errorf("eraapi: poll failed: %w", err)
		}
	}

	if dlLink == "" {
		return "", errors.New("eraapi: empty download link returned by API")
	}

	destPath := getPath(track, ".mp3")

	var pm *telegram.ProgressManager
	if mystic != nil {
		pm = utils.GetProgress(mystic)
	}

	if telegramDLRegex.MatchString(dlLink) {
		destPath, err = e.downloadFromTelegram(ctx, dlLink, destPath, pm)
	} else {
		err = e.downloadFromHTTP(ctx, dlLink, destPath)
	}

	if err != nil {
		return "", err
	}

	if !fileExists(destPath) {
		return "", errors.New("eraapi: downloaded file is empty or missing")
	}

	return destPath, nil
}

func (*EraApiPlatform) requestJob(ctx context.Context, query string) (*eraApiTryResponse, error) {
	var resp eraApiTryResponse

	httpResp, err := rc.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"query": query,
			"vid":   "false",
		}).
		SetResult(&resp).
		Get(eraApiTryURL)

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("/try request failed: %w", err)
	}

	if httpResp.IsError() {
		return nil, fmt.Errorf("/try returned status %d: %s", httpResp.StatusCode(), httpResp.String())
	}

	return &resp, nil
}

func (*EraApiPlatform) pollJob(ctx context.Context, jobID string) (string, error) {
	for i := 0; i < eraMaxPolls; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		var statusResp eraApiStatusResponse

		httpResp, err := rc.R().
			SetContext(ctx).
			SetQueryParam("id", jobID).
			SetResult(&statusResp).
			Get(eraApiStatusURL)

		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			time.Sleep(time.Second)
			continue
		}

		if httpResp.IsError() {
			time.Sleep(time.Second)
			continue
		}

		switch statusResp.Status {
		case "done":
			return statusResp.Link, nil
		case "failed":
			return "", errors.New("eraapi: server reported download failure")
		default:
			time.Sleep(time.Second)
		}
	}

	return "", fmt.Errorf("eraapi: timeout after %d seconds for job %s", eraMaxPolls, jobID)
}

func (*EraApiPlatform) downloadFromHTTP(ctx context.Context, dlURL, destPath string) error {
	resp, err := rc.R().
		SetContext(ctx).
		SetOutputFileName(destPath).
		Get(dlURL)

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("HTTP download failed: %w", err)
	}

	if resp.IsError() {
		return fmt.Errorf("HTTP download failed with status %d", resp.StatusCode())
	}

	return nil
}

func (*EraApiPlatform) downloadFromTelegram(
	ctx context.Context,
	dlURL, destPath string,
	pm *telegram.ProgressManager,
) (string, error) {
	matches := telegramDLRegex.FindStringSubmatch(dlURL)
	if len(matches) < 3 {
		return "", fmt.Errorf("eraapi: invalid Telegram link: %s", dlURL)
	}

	messageID, err := strconv.Atoi(matches[2])
	if err != nil {
		return "", fmt.Errorf("eraapi: invalid message ID: %w", err)
	}

	msg, err := core.Bot.GetMessageByID(matches[1], int32(messageID))
	if err != nil {
		return "", fmt.Errorf("eraapi: failed to fetch Telegram message: %w", err)
	}

	dOpts := &telegram.DownloadOptions{
		FileName: destPath,
		Ctx:      ctx,
	}
	if pm != nil {
		dOpts.ProgressManager = pm
	}

	if _, err = msg.Download(dOpts); err != nil {
		return "", fmt.Errorf("eraapi: Telegram download failed: %w", err)
	}

	return destPath, nil
}

func (*EraApiPlatform) CanSearch() bool                             { return false }
func (*EraApiPlatform) Search(string, bool) ([]*state.Track, error) { return nil, nil }
func (*EraApiPlatform) CanGetRecommendations() bool                 { return false }
func (*EraApiPlatform) GetRecommendations(_ *state.Track) ([]*state.Track, error) {
	return nil, errors.New("recommendations not supported on eraapi")
}
