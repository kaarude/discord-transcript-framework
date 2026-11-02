// Command export writes a transcript bundle for one Discord channel.
//
//	DISCORD_TOKEN=... go run ./examples/export -channel 123456789 -out ./out/123456789
//
// The bot must be able to view the channel and read its message history. For
// member names and role colours it also needs the Guild Members privileged
// intent enabled in the Discord developer portal.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	transcript "github.com/kaarude/discord-transcript-framework"
)

func main() {
	channelID := flag.String("channel", "", "Discord channel ID to export")
	out := flag.String("out", "", "output directory for the transcript bundle")
	limit := flag.Int("limit", 1000, "maximum number of messages to read")
	skipMembers := flag.Bool("no-members", false, "skip member lookups (faster, no nicknames or role colours)")
	flag.Parse()

	if *channelID == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: export -channel <channel-id> -out <directory> [-limit 1000] [-no-members]")
		os.Exit(2)
	}

	client, err := transcript.New(transcript.Config{BotToken: os.Getenv("DISCORD_TOKEN")})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	started := time.Now()
	document, err := client.Export(ctx, *channelID, *out, transcript.Options{
		Limit:             *limit,
		SkipMemberDetails: *skipMembers,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("Exported #%s (%d messages, %d media files, %d skipped/failed) to %s in %s\n",
		document.Channel.Name, len(document.Participants), document.Assets.Cached,
		document.Assets.Skipped+document.Assets.Failed, document.Dir,
		time.Since(started).Round(time.Millisecond))
}
