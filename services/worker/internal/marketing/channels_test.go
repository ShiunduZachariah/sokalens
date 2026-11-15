package marketing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCardRendererOnlyAcceptsVerifiedCards(t *testing.T) {
	verified := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/og/campaign" || r.URL.Query().Get("t") != "tok.sig" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Header().Set("Content-Type", "image/png")
		if verified {
			w.Header().Set("X-Campaign-Card", "verified")
		}
		_, _ = w.Write([]byte("png-bytes"))
	}))
	defer server.Close()

	renderer := NewHTTPCardRenderer(server.URL)
	media, err := renderer.Render(context.Background(), "tok.sig")
	if err != nil || string(media.Data) != "png-bytes" {
		t.Fatalf("render = %q, %v", media.Data, err)
	}
	// The route's fallback is a perfectly good PNG of the brand card. Posting
	// it in place of the day's picks would be a silent public failure.
	verified = false
	if _, err := renderer.Render(context.Background(), "tok.sig"); err == nil || !strings.Contains(err.Error(), "MARKETING_CARD_SECRET") {
		t.Fatalf("an unverified card must be refused, got %v", err)
	}
}

func TestTelegramSplitsLongCaptionsAndHidesItsToken(t *testing.T) {
	var methods []string
	var captions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/botSECRET/") {
			t.Errorf("path %s", r.URL.Path)
		}
		method := strings.TrimPrefix(r.URL.Path, "/botSECRET/")
		methods = append(methods, method)
		if method == "sendPhoto" {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			captions = append(captions, r.FormValue("caption"))
			if _, _, err := r.FormFile("photo"); err != nil {
				t.Errorf("no photo: %v", err)
			}
		}
		if method == "sendMessage" {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"is_disabled":true`) {
				t.Errorf("link previews should be off: %s", body)
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
	}))
	defer server.Close()

	client := &TelegramClient{Token: "SECRET", BaseURL: server.URL, HTTP: server.Client()}
	photo := Media{Name: "c.png", ContentType: "image/png", Data: []byte("png")}
	id, err := client.SendPhoto(context.Background(), "@chan", photo, "short caption")
	if err != nil || id != "77" {
		t.Fatalf("send = %q, %v", id, err)
	}
	if _, err := client.SendPhoto(context.Background(), "@chan", photo, strings.Repeat("x", 1500)+Compliance); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "sendPhoto,sendPhoto,sendMessage" || captions[0] != "short caption" || captions[1] != "" {
		t.Fatalf("methods = %v captions = %q", methods, captions)
	}

	// A transport error must never carry the bot token into a log line.
	client.BaseURL = "http://127.0.0.1:1"
	_, err = client.SendMessage(context.Background(), "@chan", "hi")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaked the token or did not fail: %v", err)
	}
}

func TestFacebookReportsGraphErrors(t *testing.T) {
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v23.0/PAGE/photos" {
			t.Errorf("path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("message") != "caption" || r.FormValue("access_token") != "TOKEN" {
			t.Errorf("fields = %v", r.MultipartForm.Value)
		}
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token TOKEN"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"1","post_id":"PAGE_99"}`))
	}))
	defer server.Close()
	client := &FacebookClient{PageID: "PAGE", Token: "TOKEN", Version: "v23.0", BaseURL: server.URL, HTTP: server.Client()}
	photo := Media{Name: "c.png", ContentType: "image/png", Data: []byte("png")}
	if id, err := client.PostPhoto(context.Background(), photo, "caption"); err != nil || id != "PAGE_99" {
		t.Fatalf("post = %q, %v", id, err)
	}
	fail = true
	_, err := client.PostPhoto(context.Background(), photo, "caption")
	if err == nil || !strings.Contains(err.Error(), "Invalid OAuth") || strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("graph error = %v", err)
	}
}

func TestVideoBuilderRendersAnMP4(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	// A 1x1 PNG is enough: the builder scales and pads every slide.
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0, 0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82}
	if path := os.Getenv("MARKETING_TEST_SLIDE"); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			png = data
		}
	}
	slide := Media{Name: "s.png", ContentType: "image/png", Data: png}
	video, err := FFmpegVideoBuilder{Path: "ffmpeg"}.Build(context.Background(), []Media{slide, slide})
	if err != nil {
		t.Fatal(err)
	}
	if video.ContentType != "video/mp4" || len(video.Data) < 1000 || string(video.Data[4:8]) != "ftyp" {
		t.Fatalf("not an mp4: %d bytes", len(video.Data))
	}
	if out := os.Getenv("MARKETING_TEST_VIDEO_OUT"); out != "" {
		_ = os.WriteFile(out, video.Data, 0o644)
	}
	if _, err := (FFmpegVideoBuilder{Path: "definitely-not-ffmpeg"}).Build(context.Background(), []Media{slide}); err != ErrNoFFmpeg {
		t.Fatalf("missing binary = %v", err)
	}
}
