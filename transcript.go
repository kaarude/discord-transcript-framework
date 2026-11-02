// Package transcript turns a Discord channel into a portable archive: one
// folder with a rendered HTML transcript, raw JSON, plain text, and every
// attachment, avatar, emoji and role icon downloaded next to it.
//
// It is a library, not a service. There is no HTTP server, no auth, no
// database and no dashboard. You call Export from your bot, then upload or
// store the resulting folder.
package transcript

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaarude/discord-transcript-framework/internal/discord"
	"github.com/kaarude/discord-transcript-framework/internal/exporter"
	"github.com/kaarude/discord-transcript-framework/internal/media"
	"github.com/kaarude/discord-transcript-framework/internal/model"
	"github.com/kaarude/discord-transcript-framework/internal/render"
)

// Config is what New needs. BotToken is the only required value.
type Config struct {
	// BotToken is a Discord bot token. A leading "Bot " is optional.
	BotToken string
	// HTTPClient is reused for Discord API calls and media downloads. When
	// nil, a client with a 45 second timeout is used.
	HTTPClient *http.Client
}

// Client talks to the Discord REST API with one bot token.
type Client struct {
	token   string
	http    *http.Client
	discord *discord.Client
}

// New validates the config and returns a client you can reuse for many
// exports.
func New(cfg Config) (*Client, error) {
	token := strings.TrimSpace(cfg.BotToken)
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bot "))
	if token == "" {
		return nil, errors.New("transcript: Config.BotToken is required")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Second}
	}
	return &Client{
		token:   token,
		http:    httpClient,
		discord: discord.NewWithBaseURL(discord.DefaultBaseURL, httpClient),
	}, nil
}

// Options controls one export. The zero value is the recommended default:
// the newest 1000 messages, with member names and role colours, and media
// capped at 25 MiB per file and 250 MiB per transcript.
type Options struct {
	// Limit is how many messages to read, newest first. Defaults to 1000 and
	// is capped at 50000 by the Discord API.
	Limit int
	// SkipMemberDetails skips one extra request per unique author. Nicknames,
	// role colours and role icons are then missing from the transcript.
	SkipMemberDetails bool
	// MaxAssetBytes caps one downloaded file. Defaults to 25 MiB.
	MaxAssetBytes int64
	// MaxTotalAssetBytes caps all downloaded media in the transcript.
	// Defaults to 250 MiB.
	MaxTotalAssetBytes int64
}

// Channel is the part of a Discord channel a transcript needs.
type Channel struct {
	ID      string
	Name    string
	Topic   string
	GuildID string
	Type    int
}

// Message is one raw Discord message payload, exactly as returned by the
// REST API. Keeping the payload raw means new Discord features keep rendering
// without a library update.
type Message = map[string]any

// Participant is one author who appears in a transcript.
type Participant = model.Participant

// APIError is returned for a rejected Discord REST call. Use errors.As to
// inspect Status or Code.
type APIError = discord.APIError

// AssetReport summarises media downloads.
type AssetReport struct {
	Cached  int
	Skipped int
	Failed  int
	Bytes   int64
}

// Transcript describes a finished export. Read the files from Dir, or call
// Zip to stream the whole bundle as one archive.
type Transcript struct {
	// Dir is the folder containing index.html, transcript.json,
	// transcript.txt and assets/.
	Dir string
	// Channel is the channel that was exported.
	Channel Channel
	// Participants lists authors, most active first.
	Participants []Participant
	// Assets reports what was downloaded and what was skipped.
	Assets AssetReport
	// CreatedAt is when the export finished.
	CreatedAt time.Time
}

// Export downloads a channel and writes a self-contained bundle to dir.
//
// The final layout is:
//
//	dir/index.html
//	dir/transcript.json
//	dir/transcript.txt
//	dir/assets/<content-hash>.<ext>
//
// Every attachment, avatar, emoji and role icon is downloaded, so the bundle
// keeps working after the channel is deleted. Discord CDN links are only kept
// for media that could not be fetched, and those failures are counted in
// Assets.
func (c *Client) Export(ctx context.Context, channelID, dir string, opts Options) (*Transcript, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, errors.New("transcript: channel ID is required")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("transcript: output directory is required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	if limit > 50000 {
		limit = 50000
	}

	channel, err := c.discord.Channel(ctx, c.token, channelID)
	if err != nil {
		return nil, fmt.Errorf("transcript: fetch channel: %w", err)
	}
	messages, err := c.discord.Messages(ctx, c.token, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("transcript: fetch messages: %w", err)
	}

	guildID := model.String(channel["guild_id"])
	roles := []model.Object(nil)
	if !opts.SkipMemberDetails && guildID != "" {
		if fetched, roleErr := c.discord.Roles(ctx, c.token, guildID); roleErr == nil {
			roles = fetched
		}
		members := c.discord.Members(ctx, c.token, guildID, discord.AuthorIDs(messages))
		messages = discord.AttachMembers(messages, members)
	}

	assetsDir := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		return nil, fmt.Errorf("transcript: create output directory: %w", err)
	}
	cached, err := media.Cache(ctx, messages, media.Options{
		AssetsDir:      assetsDir,
		PublicAssetURL: "assets",
		GuildID:        guildID,
		Roles:          roles,
		FileLimit:      opts.MaxAssetBytes,
		TotalLimit:     opts.MaxTotalAssetBytes,
		HTTPClient:     c.http,
	})
	if err != nil {
		return nil, fmt.Errorf("transcript: cache media: %w", err)
	}

	createdAt := time.Now().UTC()
	participants := exporter.Participants(cached.Messages)
	html := render.Transcript(cached.Messages, channelID, channel, render.Options{Roles: cached.Roles})

	document := &Transcript{
		Dir: dir,
		Channel: Channel{
			ID:      channelID,
			Name:    model.String(channel["name"]),
			Topic:   model.String(channel["topic"]),
			GuildID: guildID,
			Type:    model.Int(channel["type"]),
		},
		Participants: participants,
		Assets:       report(cached.Manifest),
		CreatedAt:    createdAt,
	}
	created := createdAt.Format(time.RFC3339)
	if err := writeFile(filepath.Join(dir, "index.html"), []byte(html)); err != nil {
		return nil, fmt.Errorf("transcript: write HTML: %w", err)
	}
	if err := writeFileFunc(filepath.Join(dir, "transcript.json"), func(w io.Writer) error {
		return exporter.WriteJSON(w, channelID, channel, cached.Roles, cached.Messages, participants, created)
	}); err != nil {
		return nil, fmt.Errorf("transcript: write JSON: %w", err)
	}
	if err := writeFileFunc(filepath.Join(dir, "transcript.txt"), func(w io.Writer) error {
		return exporter.WriteText(w, channelID, channel, cached.Messages, created)
	}); err != nil {
		return nil, fmt.Errorf("transcript: write text: %w", err)
	}
	return document, nil
}

// Zip streams the bundle as a zip archive, with paths relative to Dir. The
// caller owns w and should close it after this returns.
func (t *Transcript) Zip(w io.Writer) error {
	archive := zip.NewWriter(w)
	err := filepath.WalkDir(t.Dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(t.Dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Method = zip.Deflate
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = archive.Close()
		return err
	}
	return archive.Close()
}

func report(manifest media.Manifest) AssetReport {
	result := AssetReport{Bytes: manifest.TotalBytes}
	for _, asset := range manifest.Assets {
		switch asset.Status {
		case "cached":
			result.Cached++
		case "failed":
			result.Failed++
		default:
			result.Skipped++
		}
	}
	return result
}

func writeFile(path string, data []byte) error {
	return writeFileFunc(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// writeFileFunc writes through a temporary file so a crash never leaves a
// half-written transcript behind.
func writeFileFunc(path string, write func(io.Writer) error) error {
	temp := path + ".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	writeErr := write(file)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(temp)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(temp)
		return closeErr
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}
