package marketing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// secondsPerSlide is long enough to read three short lines on a phone.
const secondsPerSlide = 4

// VideoBuilder turns story cards into a short vertical video.
type VideoBuilder interface {
	Build(ctx context.Context, slides []Media) (Media, error)
}

// ErrNoFFmpeg means the binary is missing; the routine then sends the story
// cards on their own, which TikTok and Reels both accept as a photo post.
var ErrNoFFmpeg = errors.New("ffmpeg is not installed")

// FFmpegVideoBuilder renders a silent 1080x1920 MP4.
//
// Silent on purpose. TikTok and Reels favour posts that use a sound from
// their own library, and a sound can only be added in the app, so the owner
// adds a trending one when posting rather than us baking in music.
type FFmpegVideoBuilder struct {
	Path string
}

// Build writes the slides to a temporary directory, renders, and cleans up.
func (b FFmpegVideoBuilder) Build(ctx context.Context, slides []Media) (Media, error) {
	if len(slides) == 0 {
		return Media{}, errors.New("no slides")
	}
	binary, err := exec.LookPath(b.Path)
	if err != nil {
		return Media{}, ErrNoFFmpeg
	}
	dir, err := os.MkdirTemp("", "sokalens-video-")
	if err != nil {
		return Media{}, err
	}
	defer os.RemoveAll(dir)

	args := []string{"-y", "-loglevel", "error"}
	for i, slide := range slides {
		path := filepath.Join(dir, fmt.Sprintf("slide-%02d.png", i))
		if err := os.WriteFile(path, slide.Data, 0o600); err != nil {
			return Media{}, err
		}
		args = append(args, "-loop", "1", "-t", strconv.Itoa(secondsPerSlide), "-i", path)
	}
	var filter strings.Builder
	for i := range slides {
		// Every input is scaled and padded to the same frame, so one card of
		// a slightly different size cannot make concat refuse the whole video.
		fmt.Fprintf(&filter, "[%d:v]scale=1080:1920:force_original_aspect_ratio=decrease,pad=1080:1920:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30[v%d];", i, i)
	}
	for i := range slides {
		fmt.Fprintf(&filter, "[v%d]", i)
	}
	fmt.Fprintf(&filter, "concat=n=%d:v=1:a=0,format=yuv420p[out]", len(slides))
	output := filepath.Join(dir, "video.mp4")
	args = append(args, "-filter_complex", filter.String(), "-map", "[out]",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-movflags", "+faststart", output)

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Media{}, fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return Media{}, err
	}
	return Media{Name: "sokalens-today.mp4", ContentType: "video/mp4", Data: data}, nil
}
