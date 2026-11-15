package marketing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeStore keeps the ledger in memory and serves canned content.
type fakeStore struct {
	posts   []*Post
	picks   []Pick
	graded  map[int64]string
	goals   []Goal
	scores  map[int64][2]int
	form    []FormResult
	record  MarketRecord
	numbers Numbers
	nextID  int64
}

func (f *fakeStore) find(key, channel string) *Post {
	for _, p := range f.posts {
		if p.Key == key && p.Channel == channel {
			return p
		}
	}
	return nil
}

func (f *fakeStore) byID(id int64) *Post {
	for _, p := range f.posts {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (f *fakeStore) ClaimPost(_ context.Context, post Post) (Post, error) {
	if existing := f.find(post.Key, post.Channel); existing != nil {
		return *existing, nil
	}
	f.nextID++
	post.ID = f.nextID
	post.Status = StatusPending
	post.CreatedAt = time.Now().UTC()
	f.posts = append(f.posts, &post)
	return post, nil
}

func (f *fakeStore) BeginAttempt(_ context.Context, id int64) error {
	p := f.byID(id)
	p.Status = StatusPending
	p.Attempts++
	return nil
}
func (f *fakeStore) MarkSent(_ context.Context, id int64, _ string) error {
	f.byID(id).Status = StatusSent
	return nil
}
func (f *fakeStore) MarkFailed(_ context.Context, id int64, _ string) error {
	f.byID(id).Status = StatusFailed
	return nil
}
func (f *fakeStore) MarkSkipped(_ context.Context, id int64, _ string) error {
	f.byID(id).Status = StatusSkipped
	return nil
}
func (f *fakeStore) KeyExists(_ context.Context, key string) (bool, error) {
	for _, p := range f.posts {
		if p.Key == key {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeStore) PostsByKind(_ context.Context, kind string, _ time.Time) ([]Post, error) {
	seen := map[string]bool{}
	var out []Post
	for _, p := range f.posts {
		if p.Kind == kind && !seen[p.Key] {
			seen[p.Key] = true
			out = append(out, *p)
		}
	}
	return out, nil
}
func (f *fakeStore) CountKeysByPrefix(_ context.Context, prefix string, _ time.Time) (int, error) {
	seen := map[string]bool{}
	for _, p := range f.posts {
		if strings.HasPrefix(p.Key, prefix) {
			seen[p.Key] = true
		}
	}
	return len(seen), nil
}
func (f *fakeStore) RetryablePosts(_ context.Context, _ time.Time) ([]Post, error) {
	var out []Post
	for _, p := range f.posts {
		if p.Status == StatusFailed && p.Attempts < MaxAttempts {
			out = append(out, *p)
		}
	}
	return out, nil
}
func (f *fakeStore) CandidatePicks(_ context.Context, q PickQuery) ([]Pick, error) {
	if len(f.picks) > q.Limit {
		return f.picks[:q.Limit], nil
	}
	return f.picks, nil
}
func (f *fakeStore) GradePicks(_ context.Context, picks []Pick) ([]GradedPick, error) {
	var out []GradedPick
	for _, p := range picks {
		result := f.graded[p.FixtureID]
		if result == "" {
			result = ResultPending
		}
		out = append(out, GradedPick{Pick: p, Result: result})
	}
	return out, nil
}
func (f *fakeStore) RecentGoals(context.Context, GoalQuery) ([]Goal, error) { return f.goals, nil }
func (f *fakeStore) LatestScores(context.Context, []int64) (map[int64][2]int, error) {
	return f.scores, nil
}
func (f *fakeStore) RecentForm(context.Context, int64, time.Time, int) ([]FormResult, error) {
	return f.form, nil
}
func (f *fakeStore) HeadToHead(context.Context, int64, int64, time.Time, int) ([]FormResult, error) {
	return nil, nil
}
func (f *fakeStore) MarketRecord(_ context.Context, q RecordQuery) (MarketRecord, error) {
	record := f.record
	record.Market = q.Market
	return record, nil
}
func (f *fakeStore) WeeklyNumbers(context.Context, time.Time) (Numbers, error) {
	return f.numbers, nil
}

type sent struct {
	chat, kind, caption string
}

type fakeTelegram struct {
	sent []sent
	fail int
}

func (f *fakeTelegram) record(chat, kind, caption string) (string, error) {
	if f.fail > 0 {
		f.fail--
		return "", errors.New("telegram is down")
	}
	f.sent = append(f.sent, sent{chat, kind, caption})
	return fmt.Sprint(len(f.sent)), nil
}
func (f *fakeTelegram) SendPhoto(_ context.Context, chat string, _ Media, caption string) (string, error) {
	return f.record(chat, "photo", caption)
}
func (f *fakeTelegram) SendMessage(_ context.Context, chat, text string) (string, error) {
	return f.record(chat, "message", text)
}
func (f *fakeTelegram) SendMediaGroup(_ context.Context, chat string, items []Media) (string, error) {
	return f.record(chat, fmt.Sprintf("album:%d", len(items)), "")
}
func (f *fakeTelegram) SendVideo(_ context.Context, chat string, _ Media, caption string) (string, error) {
	return f.record(chat, "video", caption)
}
func (f *fakeTelegram) MemberCount(context.Context, string) (int, error) { return 42, nil }

func (f *fakeTelegram) to(chat string) []sent {
	var out []sent
	for _, s := range f.sent {
		if s.chat == chat {
			out = append(out, s)
		}
	}
	return out
}

type fakeFacebook struct{ posts []string }

func (f *fakeFacebook) PostPhoto(_ context.Context, _ Media, message string) (string, error) {
	f.posts = append(f.posts, message)
	return "fb", nil
}

type fakeCards struct{ rendered []Card }

func (f *fakeCards) Render(_ context.Context, token string) (Media, error) {
	payload, _, _ := strings.Cut(token, ".")
	raw, _ := base64Decode(payload)
	var card Card
	_ = json.Unmarshal(raw, &card)
	f.rendered = append(f.rendered, card)
	return Media{Name: "card.png", ContentType: "image/png", Data: []byte("png")}, nil
}

type fakeVideo struct{ built int }

func (f *fakeVideo) Build(context.Context, []Media) (Media, error) {
	f.built++
	return Media{Name: "v.mp4", ContentType: "video/mp4", Data: []byte("mp4")}, nil
}

const (
	ownerChat   = "owner-chat"
	publicChat  = "@sokalens"
	testSecret  = "a-test-secret-that-is-long-enough-to-sign"
	nairobiZone = "Africa/Nairobi"
)

func testService(t *testing.T, mode Mode, at time.Time) (*Service, *fakeStore, *fakeTelegram, *fakeFacebook, *fakeCards) {
	t.Helper()
	location, err := time.LoadLocation(nairobiZone)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{graded: map[int64]string{}}
	telegram := &fakeTelegram{}
	facebook := &fakeFacebook{}
	cards := &fakeCards{}
	cfg := Config{
		Mode: mode, CardSecret: testSecret, SiteURL: "https://sokalens.com", Location: location,
		PicksHour: 8, PicksPerDay: 3, MinLeaguePriority: 60, LiveGoals: true, LiveMaxPerDay: 2,
		TelegramBotToken: "token", TelegramChannel: publicChat, TelegramOwnerChatID: ownerChat,
		FacebookPageID: "page", FacebookPageToken: "fb-token", VideoEnabled: true,
	}
	service := &Service{Config: cfg, Store: store, Cards: cards, Telegram: telegram, Facebook: facebook, Video: &fakeVideo{},
		Now: func() time.Time { return at }}
	return service, store, telegram, facebook, cards
}

func threePicks(kickoff time.Time) []Pick {
	var picks []Pick
	for i := 0; i < 3; i++ {
		p := samplePick()
		p.FixtureID = int64(100 + i)
		p.KickoffAt = kickoff
		picks = append(picks, p)
	}
	return picks
}

// 09:00 in Nairobi on a Wednesday.
var wednesdayMorning = time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)

func TestNothingIsPostedBeforeThePicksHour(t *testing.T) {
	early := time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC) // 07:00 EAT
	service, store, telegram, _, _ := testService(t, ModeLive, early)
	store.picks = threePicks(early.Add(10 * time.Hour))
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(telegram.sent) != 0 || len(store.posts) != 0 {
		t.Fatalf("posted before 08:00: %+v", telegram.sent)
	}
}

func TestLiveModeFansOutAndHandsWhatsAppToTheOwner(t *testing.T) {
	service, store, telegram, facebook, cards := testService(t, ModeLive, wednesdayMorning)
	store.picks = threePicks(wednesdayMorning.Add(10 * time.Hour))
	store.form = []FormResult{{HomeTeamID: 10, AwayTeamID: 1, HomeGoals: 1}, {HomeTeamID: 10, AwayTeamID: 1, HomeGoals: 1}, {HomeTeamID: 10, AwayTeamID: 1}}
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	public := telegram.to(publicChat)
	if len(public) != 1 || public[0].kind != "photo" || !strings.Contains(public[0].caption, "utm_source=telegram") {
		t.Fatalf("public telegram posts = %+v", public)
	}
	if len(facebook.posts) != 1 || !strings.Contains(facebook.posts[0], "utm_source=facebook") {
		t.Fatalf("facebook posts = %q", facebook.posts)
	}
	owner := telegram.to(ownerChat)
	if len(owner) < 3 {
		t.Fatalf("owner should get the WhatsApp card, the pack note, the album and the video: %+v", owner)
	}
	if !strings.Contains(owner[0].caption, "WhatsApp") || !strings.Contains(owner[0].caption, "utm_source=whatsapp") {
		t.Fatalf("owner handoff caption = %q", owner[0].caption)
	}
	kinds := []string{}
	for _, s := range owner {
		kinds = append(kinds, s.kind)
	}
	if got := strings.Join(kinds, ","); got != "photo,message,album:4,video" {
		t.Fatalf("owner deliveries = %s", got)
	}
	if cards.rendered[0].Kind != KindPicks || len(cards.rendered[0].Items) != 3 {
		t.Fatalf("first card = %+v", cards.rendered[0])
	}

	// A second tick in the same morning posts nothing new.
	before := len(telegram.sent)
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(telegram.sent) != before || len(facebook.posts) != 1 {
		t.Fatal("a second tick reposted the picks")
	}
}

func TestPreviewModeOnlyTalksToTheOwner(t *testing.T) {
	service, store, telegram, facebook, _ := testService(t, ModePreview, wednesdayMorning)
	store.picks = threePicks(wednesdayMorning.Add(10 * time.Hour))
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(telegram.to(publicChat)) != 0 || len(facebook.posts) != 0 {
		t.Fatal("preview mode posted in public")
	}
	owner := telegram.to(ownerChat)
	if len(owner) == 0 || !strings.HasPrefix(owner[0].caption, "[PREVIEW") {
		t.Fatalf("owner preview = %+v", owner)
	}
}

func TestAFailedPostIsRetriedButNeverDuplicated(t *testing.T) {
	service, store, telegram, _, _ := testService(t, ModeLive, wednesdayMorning)
	service.Facebook = nil
	service.Config.FacebookPageID, service.Config.FacebookPageToken = "", ""
	store.picks = threePicks(wednesdayMorning.Add(10 * time.Hour))
	telegram.fail = 1 // the public post fails once
	if err := service.Tick(context.Background()); err == nil {
		t.Fatal("the failure should be reported")
	}
	if p := store.find("picks:2026-11-11", ChannelTelegram); p == nil || p.Status != StatusFailed {
		t.Fatalf("ledger row = %+v", p)
	}
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(telegram.to(publicChat)); got != 1 {
		t.Fatalf("public posts after retry = %d, want exactly 1", got)
	}

	// A row whose attempt began but never finished is left alone.
	stuck := store.find("picks:2026-11-11", ChannelTelegram)
	stuck.Status, stuck.Attempts = StatusPending, 1
	if err := service.send(context.Background(), *stuck); err != nil {
		t.Fatal(err)
	}
	if got := len(telegram.to(publicChat)); got != 1 {
		t.Fatal("an attempt of unknown outcome was resent")
	}
}

func TestResultsCardGradesWhatWasPosted(t *testing.T) {
	kickoff := time.Date(2026, 11, 11, 16, 0, 0, 0, time.UTC)
	service, store, telegram, _, cards := testService(t, ModeLive, wednesdayMorning)
	store.picks = threePicks(kickoff)
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	cards.rendered = nil

	store.graded = map[int64]string{100: ResultWon, 101: ResultLost, 102: ResultWon}
	service.Now = func() time.Time { return kickoff.Add(2 * time.Hour) }
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var results *Card
	for i := range cards.rendered {
		if cards.rendered[i].Kind == KindResults {
			results = &cards.rendered[i]
		}
	}
	if results == nil || results.Headline != "2 of 3 correct" || results.Items[1].Result != ResultLost {
		t.Fatalf("results card = %+v", results)
	}
	last := telegram.to(publicChat)
	if !strings.Contains(last[len(last)-1].caption, "LOST") {
		t.Fatal("the results caption must show the loss")
	}
}

func TestGoalsAreCappedAndCorrectionsPosted(t *testing.T) {
	at := time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)
	service, store, telegram, _, _ := testService(t, ModeLive, at)
	goal := func(h, a int) Goal {
		return Goal{FixtureID: 7, HomeGoals: h, AwayGoals: a, Minute: 10 * (h + a), Home: "Arsenal", Away: "Chelsea", League: "Premier League", CapturedAt: at.Add(time.Duration(h+a) * time.Minute)}
	}
	store.goals = []Goal{goal(1, 0), goal(2, 0), goal(3, 0)}
	if err := service.Live(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(telegram.to(publicChat)); got != 2 {
		t.Fatalf("goal posts = %d, want the daily cap of 2", got)
	}

	// VAR takes the second goal away.
	store.goals = nil
	store.scores = map[int64][2]int{7: {1, 0}}
	if err := service.Live(context.Background()); err != nil {
		t.Fatal(err)
	}
	posts := telegram.to(publicChat)
	if len(posts) != 3 || !strings.HasPrefix(posts[2].caption, "CORRECTION") || !strings.Contains(posts[2].caption, "Arsenal 1-0 Chelsea") {
		t.Fatalf("correction = %+v", posts)
	}
	if err := service.Live(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(telegram.to(publicChat)) != 3 {
		t.Fatal("the correction was posted twice")
	}
}

func TestWeeklyReportAndStatDraftGoToTheOwnerOnly(t *testing.T) {
	sunday := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC) // 21:00 EAT
	service, store, telegram, facebook, _ := testService(t, ModeLive, sunday)
	store.numbers = Numbers{Accounts: 9, NewAccounts: 1, VisitorsBySource: []SourceCount{{"telegram", 12}}}
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner := telegram.to(ownerChat)
	report := owner[len(owner)-1].caption
	if !strings.HasPrefix(report, "SokaLens weekly numbers") || !strings.Contains(report, "Accounts: 9 total, 1 new") || !strings.Contains(report, "Telegram channel members: 42") {
		t.Fatalf("report = %+v", owner)
	}

	thursday := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC) // 11:00 EAT
	service.Now = func() time.Time { return thursday }
	store.record = MarketRecord{Correct: 136, Graded: 200}
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner = telegram.to(ownerChat)
	if !strings.HasPrefix(owner[len(owner)-1].caption, "DRAFT") || strings.Contains(owner[len(owner)-1].caption, "100%") {
		t.Fatalf("stat draft = %q", owner[len(owner)-1].caption)
	}
	if len(telegram.to(publicChat)) != 0 || len(facebook.posts) != 0 {
		t.Fatal("owner-only posts reached a public channel")
	}
}

func TestOffModeDoesNothing(t *testing.T) {
	service, store, telegram, _, _ := testService(t, ModeOff, wednesdayMorning)
	store.picks = threePicks(wednesdayMorning.Add(10 * time.Hour))
	if err := errors.Join(service.Tick(context.Background()), service.Live(context.Background())); err != nil {
		t.Fatal(err)
	}
	if len(telegram.sent) != 0 || len(store.posts) != 0 {
		t.Fatal("off mode posted")
	}
}

func TestAOneCardPackIsSentAsAPhoto(t *testing.T) {
	service, store, telegram, _, _ := testService(t, ModeLive, wednesdayMorning)
	store.picks = threePicks(wednesdayMorning.Add(10 * time.Hour))
	store.form = nil // no history, so no preview cards: the pack is the cover alone
	if err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, s := range telegram.to(ownerChat) {
		kinds = append(kinds, s.kind)
	}
	if got := strings.Join(kinds, ","); got != "photo,message,photo,video" {
		t.Fatalf("owner deliveries = %s", got)
	}
}
