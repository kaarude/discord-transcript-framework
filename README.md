# Discord Transcript Framework

A small Go library that turns a Discord channel into a transcript bundle you can
upload, zip or archive. It is made to be dropped into a Discord bot:

```go
client, _ := transcript.New(transcript.Config{BotToken: os.Getenv("DISCORD_TOKEN")})
doc, err := client.Export(ctx, channelID, "./out/general", transcript.Options{})
```

That writes:

```
out/general/
├── index.html         rendered transcript (dark and light theme)
├── transcript.json    raw messages plus channel, roles, participants
├── transcript.txt     plain text
└── assets/            every attachment, avatar, emoji and role icon
```

Everything in `assets/` is downloaded during the export, so the transcript keeps
working after the channel or message is deleted. Discord CDN links are only used
when a download fails.

## What this is not

This is the core engine from
[discord-transcript-api](https://github.com/kaarude/discord-transcript-api)
without the service around it. There is no HTTP server, no API token, no passkey
login, no admin panel, no database, no expiry, no download menu and no automatic
updates. Your bot already has the token and the event handler, so none of that is
needed.

## Install

```sh
go get github.com/kaarude/discord-transcript-framework
```

The library uses only the Go standard library. Go 1.26.6 or newer.

Your bot needs **View Channel** and **Read Message History** in the channel. For
nicknames, role colours and role icons it also needs the **Guild Members**
privileged intent enabled in the Discord developer portal. Without member access,
set `SkipMemberDetails: true` so the export still succeeds.

## Quick start from the command line

```sh
DISCORD_TOKEN=... go run ./examples/export -channel 123456789012345678 -out ./out/general
```

Open `out/general/index.html` in a browser. Everything loads from the folder.

## Use it in a bot

The export is synchronous and can take a few seconds on large channels, so defer
the interaction first. This example uses
[discordgo](https://github.com/bwmarrin/discordgo).

```go
import (
    "bytes"
    "context"
    "os"

    "github.com/bwmarrin/discordgo"
    transcript "github.com/kaarude/discord-transcript-framework"
)

client, err := transcript.New(transcript.Config{BotToken: os.Getenv("DISCORD_TOKEN")})

func onTranscript(s *discordgo.Session, i *discordgo.InteractionCreate) {
    if i.ApplicationCommandData().Name != "transcript" {
        return
    }
    _ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
    })

    dir, err := os.MkdirTemp("", "transcript-*")
    if err != nil {
        return
    }
    defer os.RemoveAll(dir)

    doc, err := client.Export(context.Background(), i.ChannelID, dir, transcript.Options{Limit: 5000})
    if err != nil {
        return
    }

    var archive bytes.Buffer
    if err := doc.Zip(&archive); err != nil {
        return
    }
    _, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
        Content: "Transcript for #" + doc.Channel.Name,
        Files: []*discordgo.File{{
            Name:        "transcript.zip",
            ContentType: "application/zip",
            Reader:      bytes.NewReader(archive.Bytes()),
        }},
    })
}
```

Discord caps uploads (10 MiB for most servers). A long channel with video
attachments can exceed that. Either lower `Limit`, raise `MaxAssetBytes` limits
deliberately, or host the bundle elsewhere and send a link.

### Wiring it with an AI agent

Give the agent this instruction:

> Add a `/transcript` slash command. Use
> `github.com/kaarude/discord-transcript-framework`. Defer the interaction, call
> `client.Export` for the current channel into a temporary directory, then zip it
> with `doc.Zip` and upload `transcript.zip` as a follow-up. Remove the temporary
> directory after the upload. Do not add a web server or a database.

## API

### `transcript.New(Config) (*Client, error)`

`Config.BotToken` is required. A leading `Bot ` is optional. `Config.HTTPClient`
is reused for both Discord and media downloads.

### `Client.Export(ctx, channelID, dir string, Options) (*Transcript, error)`

Fetches the channel, up to `Options.Limit` messages (default 1000), guild roles
and members, downloads all referenced media into `dir/assets`, and writes the
three files. It returns when the bundle is complete.

`dir` may already exist. Existing files with the same names are replaced, and
asset filenames are content hashes, so a re-export is safe. Stale files from an
older export are not deleted.

### `Transcript`

| Field | Meaning |
| --- | --- |
| `Dir` | Bundle directory |
| `Channel` | ID, name, topic, guild ID, type |
| `Participants` | Authors, most active first, with message counts |
| `Assets` | Cached, skipped, failed counts and total bytes |
| `CreatedAt` | Export time |

`Transcript.Zip(io.Writer)` streams the whole folder as a zip archive.

### `Options`

| Field | Default | Purpose |
| --- | --- | --- |
| `Limit` | `1000` | Messages to read, newest first. Discord caps this at 50000. |
| `SkipMemberDetails` | `false` | Skip one request per author. Loses nicknames, role colours, role icons. |
| `MaxAssetBytes` | 25 MiB | Largest single downloaded file. |
| `MaxTotalAssetBytes` | 250 MiB | Total media budget for one transcript. |

Files over a limit are skipped and counted in `Assets.Skipped`; the transcript
still renders, using the original CDN URL for that item.

## What the HTML renderer keeps

The renderer is a port of the original service's transcript view and handles:

- grouped messages and channel date dividers
- Discord markdown: bold, italic, underline, strike, spoilers, code blocks,
  inline code, quotes, headings, lists and masked links
- mentions, custom emoji, role colours and role icons
- replies, threads, system messages and slash command context
- embeds, buttons, select menus, attachments, images, video and audio
- reactions and a member profile card
- image lightbox, theme toggle, message permalinks

Avatars, emoji, role icons, embed images and attachments are rewritten to local
`assets/` paths. The HTML is rendered with a strict Content Security Policy:
inline styles and scripts only, media from the local folder or Discord's CDN.

## How it works

1. `GET /channels/:id` and paginated `GET /channels/:id/messages` fetch the
   channel and up to `Limit` messages through the Discord REST API.
2. Guild roles and member records are fetched once, so messages can show
   nicknames and role colours.
3. Every media URL is collected, deduplicated, downloaded with size and host
   checks, and stored under a SHA-256 filename in `assets/`.
4. Message payloads are rewritten to point at those local files.
5. The renderer emits one HTML file; the exporters emit JSON and text.
6. Files are written through a temporary file and renamed, so a crash does not
   leave a half-written transcript.

Media downloads only follow HTTPS URLs on `cdn.discordapp.com` and
`media.discordapp.net`, at most three redirects, one 10 second timeout per file.

## Security notes

- Bot tokens are read from your config and sent only to Discord. They are never
  written to disk.
- Transcripts contain user generated content. Discord markdown is escaped and
  rendered through a small allowlist, and the CSP blocks remote scripts, so a
  malicious message cannot run code in the viewer.
- Bundles are archives of private conversations. Treat the output folder with the
  same care as the channel itself.
- Downloaded media comes from Discord's CDN only. Other hosts are skipped.

## Attribution

Derived from [discord-transcript-api](https://github.com/kaarude/discord-transcript-api),
also MIT licensed.

## License

[MIT](LICENSE)
