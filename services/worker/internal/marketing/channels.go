package marketing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Media is one image or video to attach.
type Media struct {
	Name        string
	ContentType string
	Data        []byte
	Caption     string
}

// Telegram is the small part of the Bot API the routine uses. Plain text only:
// no parse mode, so a team called "Atlético_B" cannot break a post.
type Telegram interface {
	SendPhoto(ctx context.Context, chatID string, photo Media, caption string) (string, error)
	SendMessage(ctx context.Context, chatID, text string) (string, error)
	SendMediaGroup(ctx context.Context, chatID string, items []Media) (string, error)
	SendVideo(ctx context.Context, chatID string, video Media, caption string) (string, error)
	MemberCount(ctx context.Context, chatID string) (int, error)
}

// Facebook posts a photo to a Page the owner administers.
type Facebook interface {
	PostPhoto(ctx context.Context, photo Media, message string) (string, error)
}

// CardRenderer turns a signed token into PNG bytes.
type CardRenderer interface {
	Render(ctx context.Context, token string) (Media, error)
}

// telegramCaptionLimit is the Bot API limit on a photo caption.
const telegramCaptionLimit = 1024

// TelegramClient calls api.telegram.org.
type TelegramClient struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// NewTelegramClient returns a client with a bounded timeout.
func NewTelegramClient(token string) *TelegramClient {
	return &TelegramClient{Token: token, BaseURL: "https://api.telegram.org", HTTP: &http.Client{Timeout: 60 * time.Second}}
}

type telegramResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (c *TelegramClient) call(ctx context.Context, method, contentType string, body io.Reader) (json.RawMessage, error) {
	endpoint := c.BaseURL + "/bot" + c.Token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The token is in the URL; never let it reach a log line.
		return nil, fmt.Errorf("telegram %s: %s", method, redact(err.Error(), c.Token))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var decoded telegramResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("telegram %s: HTTP %d", method, resp.StatusCode)
	}
	if !decoded.OK {
		return nil, fmt.Errorf("telegram %s: %s", method, decoded.Description)
	}
	return decoded.Result, nil
}

func redact(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "<redacted>")
}

func messageID(result json.RawMessage) string {
	var single struct {
		MessageID int64 `json:"message_id"`
	}
	if json.Unmarshal(result, &single) == nil && single.MessageID != 0 {
		return strconv.FormatInt(single.MessageID, 10)
	}
	var many []struct {
		MessageID int64 `json:"message_id"`
	}
	if json.Unmarshal(result, &many) == nil && len(many) > 0 {
		return strconv.FormatInt(many[0].MessageID, 10)
	}
	return ""
}

func (c *TelegramClient) multipart(ctx context.Context, method string, fields map[string]string, files []Media, fileField func(int) string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return "", err
		}
	}
	for i, file := range files {
		part, err := writer.CreatePart(map[string][]string{
			"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fileField(i), file.Name)},
			"Content-Type":        {file.ContentType},
		})
		if err != nil {
			return "", err
		}
		if _, err := part.Write(file.Data); err != nil {
			return "", err
		}
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	result, err := c.call(ctx, method, writer.FormDataContentType(), &body)
	if err != nil {
		return "", err
	}
	return messageID(result), nil
}

// SendPhoto posts an image. A caption over the Bot API limit is sent as a
// follow-up message instead of being cut, because the cut part would be the
// compliance line at the end.
func (c *TelegramClient) SendPhoto(ctx context.Context, chatID string, photo Media, caption string) (string, error) {
	fields := map[string]string{"chat_id": chatID}
	long := len([]rune(caption)) > telegramCaptionLimit
	if !long && caption != "" {
		fields["caption"] = caption
	}
	id, err := c.multipart(ctx, "sendPhoto", fields, []Media{photo}, func(int) string { return "photo" })
	if err != nil {
		return "", err
	}
	if long {
		if _, err := c.SendMessage(ctx, chatID, caption); err != nil {
			return id, err
		}
	}
	return id, nil
}

// SendVideo posts a video file.
func (c *TelegramClient) SendVideo(ctx context.Context, chatID string, video Media, caption string) (string, error) {
	fields := map[string]string{"chat_id": chatID, "supports_streaming": "true"}
	if caption != "" && len([]rune(caption)) <= telegramCaptionLimit {
		fields["caption"] = caption
	}
	return c.multipart(ctx, "sendVideo", fields, []Media{video}, func(int) string { return "video" })
}

// SendMediaGroup posts up to ten images as one album.
func (c *TelegramClient) SendMediaGroup(ctx context.Context, chatID string, items []Media) (string, error) {
	if len(items) == 0 {
		return "", errors.New("empty media group")
	}
	if len(items) > 10 {
		items = items[:10]
	}
	type inputMedia struct {
		Type    string `json:"type"`
		Media   string `json:"media"`
		Caption string `json:"caption,omitempty"`
	}
	media := make([]inputMedia, len(items))
	for i, item := range items {
		media[i] = inputMedia{Type: "photo", Media: "attach://file" + strconv.Itoa(i)}
		if len([]rune(item.Caption)) <= telegramCaptionLimit {
			media[i].Caption = item.Caption
		}
	}
	encoded, err := json.Marshal(media)
	if err != nil {
		return "", err
	}
	return c.multipart(ctx, "sendMediaGroup", map[string]string{"chat_id": chatID, "media": string(encoded)}, items,
		func(i int) string { return "file" + strconv.Itoa(i) })
}

// SendMessage posts text with link previews off: a caption with three links
// would otherwise unfurl the first one under the card.
func (c *TelegramClient) SendMessage(ctx context.Context, chatID, text string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
	if err != nil {
		return "", err
	}
	result, err := c.call(ctx, "sendMessage", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	return messageID(result), nil
}

// MemberCount is a channel's subscriber count.
func (c *TelegramClient) MemberCount(ctx context.Context, chatID string) (int, error) {
	payload, _ := json.Marshal(map[string]string{"chat_id": chatID})
	result, err := c.call(ctx, "getChatMemberCount", "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	var count int
	if err := json.Unmarshal(result, &count); err != nil {
		return 0, err
	}
	return count, nil
}

// FacebookClient posts to a Page through the Graph API.
type FacebookClient struct {
	PageID  string
	Token   string
	Version string
	BaseURL string
	HTTP    *http.Client
}

// NewFacebookClient returns a client with a bounded timeout.
func NewFacebookClient(pageID, token, version string) *FacebookClient {
	return &FacebookClient{PageID: pageID, Token: token, Version: version, BaseURL: "https://graph.facebook.com", HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// PostPhoto uploads the card with its caption as a Page photo post.
func (c *FacebookClient) PostPhoto(ctx context.Context, photo Media, message string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("message", message)
	_ = writer.WriteField("published", "true")
	_ = writer.WriteField("access_token", c.Token)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="source"; filename="%s"`, photo.Name)},
		"Content-Type":        {photo.ContentType},
	})
	if err != nil {
		return "", err
	}
	if _, err := part.Write(photo.Data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	endpoint := c.BaseURL + "/" + url.PathEscape(c.Version) + "/" + url.PathEscape(c.PageID) + "/photos"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("facebook photo: %s", redact(err.Error(), c.Token))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var decoded struct {
		ID     string `json:"id"`
		PostID string `json:"post_id"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &decoded)
	if resp.StatusCode >= 300 || decoded.Error != nil {
		message := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if decoded.Error != nil {
			message = decoded.Error.Message
		}
		return "", fmt.Errorf("facebook photo: %s", redact(message, c.Token))
	}
	if decoded.PostID != "" {
		return decoded.PostID, nil
	}
	return decoded.ID, nil
}

// HTTPCardRenderer fetches cards from the web app's card route.
type HTTPCardRenderer struct {
	BaseURL string
	HTTP    *http.Client
}

// NewHTTPCardRenderer returns a renderer with a bounded timeout.
func NewHTTPCardRenderer(baseURL string) *HTTPCardRenderer {
	return &HTTPCardRenderer{BaseURL: baseURL, HTTP: &http.Client{Timeout: 45 * time.Second}}
}

// Render fetches one card. Anything but a PNG is an error: the route answers
// an unverifiable token with the plain brand card, and posting that in place
// of the day's picks would be a silent failure in public.
func (r *HTTPCardRenderer) Render(ctx context.Context, token string) (Media, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+CardPath(token), nil)
	if err != nil {
		return Media{}, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return Media{}, fmt.Errorf("render card: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Media{}, fmt.Errorf("read card: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/png") {
		return Media{}, fmt.Errorf("render card: HTTP %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Campaign-Card") != "verified" {
		return Media{}, errors.New("render card: the web route did not verify the token (check MARKETING_CARD_SECRET on web and worker)")
	}
	return Media{Name: "sokalens.png", ContentType: "image/png", Data: data}, nil
}
