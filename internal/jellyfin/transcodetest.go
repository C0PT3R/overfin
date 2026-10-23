package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// VideoSource is one film's or episode's video, as a transcoding test
// needs it.
type VideoSource struct {
	ItemID, SourceID string
	Name             string
	Codec            string // "h264", "hevc", "av1"…
	BitDepth         int
	HDR              bool
}

// VideoSources is a random sample of up to limit films and episodes' first
// videos. Admin only.
func (c *Client) VideoSources(ctx context.Context, admin Auth, limit int) ([]VideoSource, error) {
	var res struct {
		Items []struct {
			Id, Name     string
			MediaSources []struct {
				Id           string
				MediaStreams []struct {
					Type, Codec, VideoRange string
					BitDepth                int
				}
			}
		}
	}
	q := url.Values{"IncludeItemTypes": {"Movie,Episode"}, "Recursive": {"true"}, "Fields": {"MediaSources"},
		"SortBy": {"Random"}, "Limit": {strconv.Itoa(limit)}}
	if err := c.doQuery(ctx, "video sources", http.MethodGet, "/Items", q, admin, nil, &res); err != nil {
		return nil, err
	}
	var out []VideoSource
	for _, it := range res.Items {
		if len(it.MediaSources) == 0 {
			continue
		}
		ms := it.MediaSources[0]
		item, err1 := NormalizeID(it.Id)
		source, err2 := NormalizeID(ms.Id)
		if err1 != nil || err2 != nil {
			continue
		}
		for _, s := range ms.MediaStreams {
			if s.Type == "Video" {
				out = append(out, VideoSource{ItemID: item, SourceID: source, Name: it.Name, Codec: strings.ToLower(s.Codec),
					BitDepth: s.BitDepth, HDR: s.VideoRange == "HDR"})
				break
			}
		}
	}
	return out, nil
}

// TestTranscode starts transcoding src to 720p H.264 at 4 Mb/s, as a
// stream to read; the caller closes it and stops the encoding
// (StopEncoding) with the same device and session.
func (c *Client) TestTranscode(ctx context.Context, admin Auth, src VideoSource, session string) (*http.Response, error) {
	item, err := NormalizeID(src.ItemID)
	if err != nil {
		return nil, err
	}
	q := url.Values{"static": {"false"}, "MediaSourceId": {src.SourceID}, "DeviceId": {admin.DeviceID}, "PlaySessionId": {session},
		"VideoCodec": {"h264"}, "AudioCodec": {"aac"}, "MaxWidth": {"1280"}, "VideoBitrate": {"4000000"}, "AudioBitrate": {"128000"}}
	return c.Open(ctx, admin, "/Videos/"+item+"/stream.ts", q, "")
}

// LogFile is one of Jellyfin's log files.
type LogFile struct {
	Name     string
	Modified time.Time
}

// TranscodeLogs is Jellyfin's transcoding logs, one per transcode. Admin
// only.
func (c *Client) TranscodeLogs(ctx context.Context, admin Auth) ([]LogFile, error) {
	var res []struct {
		Name         string
		DateModified time.Time
	}
	if err := c.do(ctx, "logs", http.MethodGet, "/System/Logs", admin, nil, &res); err != nil {
		return nil, err
	}
	var logs []LogFile
	for _, l := range res {
		if transcodeLog.MatchString(l.Name) {
			logs = append(logs, LogFile{Name: l.Name, Modified: l.DateModified})
		}
	}
	return logs, nil
}

var transcodeLog = regexp.MustCompile(`^FFmpeg\.Transcode-[0-9_-]+_[0-9a-f]{32}_[0-9a-f]+\.log$`)

// TranscodeLog reads one transcoding log, up to 1 MiB. Admin only.
func (c *Client) TranscodeLog(ctx context.Context, admin Auth, name string) (string, error) {
	if !transcodeLog.MatchString(name) {
		return "", errors.New("jellyfin: not a transcoding log")
	}
	b, err := c.ReadSmall(ctx, admin, "/System/Logs/Log", url.Values{"name": {name}}, 1<<20)
	return string(b), err
}
