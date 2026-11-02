package transcript

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaarude/discord-transcript-framework/internal/discord"
)

// roundTripFunc lets a test send Discord CDN requests to the local test server
// while the URL under test keeps its real cdn.discordapp.com hostname.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/channels/c1", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bot test-token" {
			t.Errorf("Authorization = %q", got)
		}
		writeJSON(t, w, map[string]any{
			"id": "c1", "name": "general", "topic": "Talk here", "guild_id": "g1", "type": 0,
		})
	})
	mux.HandleFunc("/channels/c1/messages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []any{map[string]any{
			"id":        "m1",
			"type":      0,
			"timestamp": "2026-01-02T03:04:05.000000+00:00",
			"content":   "hello **world**",
			"author":    map[string]any{"id": "u1", "username": "alice"},
		}})
	})
	mux.HandleFunc("/guilds/g1/roles", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []any{map[string]any{
			"id": "r1", "name": "Mod", "color": 3447003, "position": 1,
		}})
	})
	mux.HandleFunc("/guilds/g1/members/u1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"user": map[string]any{"id": "u1"}, "nick": "Ali", "roles": []string{"r1"},
		})
	})
	mux.HandleFunc("/attachments/1/2/photo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("not really a png, but the downloader does not care"))
	})
	mux.HandleFunc("/embed/avatars/0.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake default avatar"))
	})
	return httptest.NewServer(mux)
}

func mediaClient(server *httptest.Server) *Client {
	return testClient(server)
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func testClient(server *httptest.Server) *Client {
	return &Client{
		token:   "test-token",
		http:    mediaHTTPClient(server),
		discord: discord.NewWithBaseURL(server.URL, server.Client()),
	}
}

func mediaHTTPClient(server *httptest.Server) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		request = request.Clone(request.Context())
		request.URL.Scheme = "http"
		request.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return http.DefaultTransport.RoundTrip(request)
	})}
}

func TestExportWritesBundle(t *testing.T) {
	server := testServer(t)
	defer server.Close()
	dir := t.TempDir()

	document, err := testClient(server).Export(context.Background(), "c1", dir, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if document.Channel.Name != "general" || document.Channel.GuildID != "g1" {
		t.Fatalf("channel = %+v", document.Channel)
	}
	if len(document.Participants) != 1 || document.Participants[0].DisplayName != "Ali" {
		t.Fatalf("participants = %+v", document.Participants)
	}

	html, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hello <strong>world</strong>", "Ali", "#3498db", "general"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("HTML missing %q", want)
		}
	}

	var raw struct {
		Transcript struct {
			Channel struct {
				Name string `json:"name"`
			} `json:"channel"`
			MessageCount int `json:"messageCount"`
		} `json:"transcript"`
		Messages []map[string]any `json:"messages"`
	}
	jsonBytes, err := os.ReadFile(filepath.Join(dir, "transcript.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jsonBytes, &raw); err != nil {
		t.Fatalf("transcript.json: %v", err)
	}
	if raw.Transcript.Channel.Name != "general" || raw.Transcript.MessageCount != 1 || len(raw.Messages) != 1 {
		t.Fatalf("JSON transcript = %+v", raw)
	}

	text, err := os.ReadFile(filepath.Join(dir, "transcript.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "hello **world**") {
		t.Errorf("text export missing message: %s", text)
	}
}

func TestExportDownloadsAttachments(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/channels/c1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"id": "c1", "name": "general"})
	})
	mux.HandleFunc("/channels/c1/messages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []any{map[string]any{
			"id": "m1", "type": 0, "timestamp": "2026-01-02T03:04:05.000000+00:00", "content": "look",
			"author": map[string]any{"id": "u1", "username": "alice"},
			"attachments": []any{map[string]any{
				"id": "a1", "filename": "photo.png", "content_type": "image/png", "size": 12,
				"url": "https://cdn.discordapp.com/attachments/1/2/photo.png",
			}},
		}})
	})
	mux.HandleFunc("/attachments/1/2/photo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake png bytes"))
	})
	mux.HandleFunc("/embed/avatars/0.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake default avatar"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	dir := t.TempDir()

	document, err := mediaClient(server).Export(context.Background(), "c1", dir, Options{SkipMemberDetails: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	// The attachment and the author's default avatar.
	if document.Assets.Cached != 2 || document.Assets.Failed != 0 {
		t.Fatalf("assets = %+v", document.Assets)
	}
	html, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "https://cdn.discordapp.com/attachments") {
		t.Error("HTML still points at the CDN")
	}
	if !strings.Contains(string(html), `src="assets/`) {
		t.Error("HTML does not use the local asset")
	}
	// One attachment and one default avatar.
	matches, err := filepath.Glob(filepath.Join(dir, "assets", "*.png"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("assets = %v, %v", matches, err)
	}
}

func TestZipContainsBundle(t *testing.T) {
	server := testServer(t)
	defer server.Close()
	dir := t.TempDir()

	document, err := testClient(server).Export(context.Background(), "c1", dir, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var buffer bytes.Buffer
	if err := document.Zip(&buffer); err != nil {
		t.Fatalf("Zip: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, file := range reader.File {
		names[file.Name] = true
	}
	for _, want := range []string{"index.html", "transcript.json", "transcript.txt"} {
		if !names[want] {
			t.Errorf("zip missing %s (has %v)", want, names)
		}
	}
	entry, err := reader.Open("index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	content, err := io.ReadAll(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "hello <strong>world</strong>") {
		t.Error("zipped HTML is missing the message")
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New accepted an empty token")
	}
	client, err := New(Config{BotToken: "Bot abc"})
	if err != nil {
		t.Fatal(err)
	}
	if client.token != "abc" {
		t.Fatalf("token = %q", client.token)
	}
}
