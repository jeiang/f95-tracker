package web

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeiang/f95-tracker/internal/downloads"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

var platformText = map[string]string{
	downloads.PlatformLinux: "Linux", downloads.PlatformWinLinux: "Win/Linux", downloads.PlatformWin: "Win",
	downloads.PlatformMac: "Mac", downloads.PlatformAndroid: "Android",
}

func mirrorRows(j downloads.Job) []ui.MirrorRow {
	rows := make([]ui.MirrorRow, len(j.Mirrors))
	for i, m := range j.Mirrors {
		rows[i] = ui.MirrorRow{
			Position: m.Position, Host: m.Host, URL: m.URL, Platform: platformText[m.Platform], Supported: m.Supported,
			State: m.State, Error: m.Error, Active: m.ID == j.ActiveMirrorID,
		}
	}
	return rows
}

func jobRow(j downloads.Job) ui.DownloadJobRow {
	r := ui.DownloadJobRow{
		ID: j.ID, GameID: j.GameID, GameName: j.GameName, Version: j.TargetVersion, State: string(j.State),
		Reason: j.NeedsHumanReason, Trigger: string(j.Trigger), Result: j.ResultPath, Updated: minutePart(j.UpdatedAt) + " UTC",
		Mirrors:   mirrorRows(j),
		CanPaste:  j.State == downloads.AwaitingLinks || j.State == downloads.NeedsHuman,
		CanCancel: downloads.Allowed(j.State, downloads.Cancelled, downloads.ActorUser),
		CanDone:   j.State == downloads.NeedsHuman,
		CanRetry:  j.State == downloads.NeedsHuman,
	}
	if j.BytesDone != nil {
		r.Progress = humanBytes(*j.BytesDone)
		if j.BytesTotal != nil {
			r.Progress += " of " + humanBytes(*j.BytesTotal)
		}
	}
	return r
}

func (s *Server) buildDownloads(ctx context.Context, msg string) (ui.DownloadsView, error) {
	jobs, err := s.downloads.List(ctx)
	if err != nil {
		return ui.DownloadsView{}, err
	}
	v := ui.DownloadsView{Message: msg}
	for _, j := range jobs {
		v.Jobs = append(v.Jobs, jobRow(j))
	}
	return v, nil
}

func (s *Server) buildPasteLinks(j downloads.Job, links, platform, errMsg string) ui.PasteLinksView {
	return ui.PasteLinksView{
		JobID: j.ID, GameID: j.GameID, GameName: j.GameName, Version: j.TargetVersion, State: string(j.State),
		Links: links, Platform: platform, Error: errMsg, Mirrors: mirrorRows(j),
	}
}

// parseLinkLines reads the paste textarea: one link per line, optionally followed by a platform
// token or a free label. A token wins; else a platform the label or URL implies; else the
// select's platform.
func parseLinkLines(text, defaultPlatform string) []downloads.LinkInput {
	var out []downloads.LinkInput
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		in := downloads.LinkInput{URL: f[0]}
		if rest := strings.Join(f[1:], " "); platformText[strings.ToLower(rest)] != "" {
			in.Platform = strings.ToLower(rest)
		} else {
			in.Label = rest
			if downloads.GuessPlatform(rest+" "+f[0]) == "" {
				in.Platform = defaultPlatform
			}
		}
		out = append(out, in)
	}
	return out
}
