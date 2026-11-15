package marketing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// KindPack and KindReport are owner-only posts with no single card.
const (
	KindPack   = "pack"
	KindReport = "report"
	KindNote   = "note"
)

// Service runs the routine. Tick handles the clock-driven posts and Live the
// goal updates; both are safe to call as often as the scheduler likes, because
// every post is keyed in the ledger and only the first call for a key composes it.
type Service struct {
	Config   Config
	Store    Store
	Cards    CardRenderer
	Telegram Telegram
	Facebook Facebook
	Video    VideoBuilder
	Now      func() time.Time
	Logger   *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Tick runs every clock-driven step. One step failing does not stop the
// others: a Facebook outage should not cost the Telegram results card.
func (s *Service) Tick(ctx context.Context) error {
	if !s.Config.Enabled() {
		return nil
	}
	now := s.now()
	var errs []error
	for _, step := range []struct {
		name string
		run  func(context.Context, time.Time) error
	}{
		{"retry", s.retryFailed},
		{"daily picks", s.dailyPicks},
		{"results", s.results},
		{"weekly record", s.weeklyRecord},
		{"stat draft", s.statDraft},
		{"weekly report", s.weeklyReport},
	} {
		if err := step.run(ctx, now); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", step.name, err))
		}
	}
	return errors.Join(errs...)
}

// Live posts goals in watched matches and corrections when VAR takes one away.
func (s *Service) Live(ctx context.Context) error {
	if !s.Config.Enabled() || !s.Config.LiveGoals {
		return nil
	}
	if s.Config.Mode == ModeLive && !s.Config.TelegramChannelEnabled() {
		return nil
	}
	now := s.now()
	return errors.Join(s.liveGoals(ctx, now), s.corrections(ctx, now))
}

// outgoing is one post before it is fanned out to channels.
type outgoing struct {
	Key  string
	Kind string
	Card *Card
	// Caption is built per channel, because the tracked link differs.
	Caption func(source string) string
	// Public posts go to the public channels in live mode.
	Public   bool
	Facebook bool
	// Handoff, when set, also sends the post to the owner in live mode with
	// this line on top: how WhatsApp gets its daily card.
	Handoff string
	Payload any
}

func (s *Service) channelsFor(out outgoing) []string {
	if s.Config.Mode == ModePreview || !out.Public {
		return []string{ChannelOwner}
	}
	var channels []string
	if s.Config.TelegramChannelEnabled() {
		channels = append(channels, ChannelTelegram)
	}
	if out.Facebook && s.Config.FacebookEnabled() {
		channels = append(channels, ChannelFacebook)
	}
	if out.Handoff != "" {
		channels = append(channels, ChannelOwner)
	}
	return channels
}

func (s *Service) ownerCaption(out outgoing) string {
	if !out.Public {
		return out.Caption("owner")
	}
	if s.Config.Mode == ModePreview {
		return "[PREVIEW: in live mode this posts to the public channels]\n\n" + out.Caption("telegram")
	}
	return out.Handoff + "\n\n" + out.Caption("whatsapp")
}

// publish records the post for every channel, then sends each row once.
func (s *Service) publish(ctx context.Context, out outgoing) error {
	token := ""
	if out.Card != nil {
		var err error
		if token, err = SignCard(s.Config.CardSecret, *out.Card); err != nil {
			return fmt.Errorf("sign %s card: %w", out.Kind, err)
		}
	}
	payload, err := json.Marshal(out.Payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", out.Kind, err)
	}
	if out.Payload == nil {
		payload = []byte(`{}`)
	}
	var errs []error
	for _, channel := range s.channelsFor(out) {
		caption := ""
		switch channel {
		case ChannelOwner:
			caption = s.ownerCaption(out)
		default:
			caption = out.Caption(channel)
		}
		post, err := s.Store.ClaimPost(ctx, Post{Key: out.Key, Channel: channel, Kind: out.Kind, Caption: caption, CardToken: token, Payload: payload})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.send(ctx, post); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// send delivers one ledger row, at most MaxAttempts times. A row left pending
// after an attempt began is not resent; see RetryablePosts.
func (s *Service) send(ctx context.Context, post Post) error {
	switch {
	case post.Status == StatusSent || post.Status == StatusSkipped:
		return nil
	case post.Status == StatusPending && post.Attempts > 0:
		return nil
	case post.Attempts >= MaxAttempts:
		return nil
	}
	if err := s.Store.BeginAttempt(ctx, post.ID); err != nil {
		return err
	}
	externalID, err := s.deliver(ctx, post)
	if err != nil {
		s.logger().Warn("marketing post failed", "key", post.Key, "channel", post.Channel, "attempt", post.Attempts+1, "error", err)
		if markErr := s.Store.MarkFailed(ctx, post.ID, err.Error()); markErr != nil {
			return errors.Join(err, markErr)
		}
		if post.Attempts+1 >= MaxAttempts && post.Channel != ChannelOwner {
			s.tellOwner(ctx, fmt.Sprintf("A %s post to %s failed %d times and will not be retried: %v", post.Kind, post.Channel, MaxAttempts, err))
		}
		return fmt.Errorf("%s to %s: %w", post.Key, post.Channel, err)
	}
	s.logger().Info("marketing post sent", "key", post.Key, "channel", post.Channel)
	return s.Store.MarkSent(ctx, post.ID, externalID)
}

func (s *Service) tellOwner(ctx context.Context, text string) {
	if _, err := s.Telegram.SendMessage(ctx, s.Config.TelegramOwnerChatID, text); err != nil {
		s.logger().Warn("marketing owner notice failed", "error", err)
	}
}

func (s *Service) deliver(ctx context.Context, post Post) (string, error) {
	if post.Kind == KindPack {
		return s.deliverPack(ctx, post)
	}
	var card *Media
	if post.CardToken != "" {
		media, err := s.Cards.Render(ctx, post.CardToken)
		if err != nil {
			return "", err
		}
		card = &media
	}
	switch post.Channel {
	case ChannelTelegram, ChannelOwner:
		chat := s.Config.TelegramChannel
		if post.Channel == ChannelOwner {
			chat = s.Config.TelegramOwnerChatID
		}
		if card == nil {
			return s.Telegram.SendMessage(ctx, chat, post.Caption)
		}
		return s.Telegram.SendPhoto(ctx, chat, *card, post.Caption)
	case ChannelFacebook:
		if s.Facebook == nil || card == nil {
			return "", errors.New("facebook is not configured or the post has no card")
		}
		return s.Facebook.PostPhoto(ctx, *card, post.Caption)
	}
	return "", fmt.Errorf("unknown channel %q", post.Channel)
}

type packPayload struct {
	Tokens []string `json:"tokens"`
	Video  bool     `json:"video"`
}

// deliverPack sends the owner the day's story cards as one album, then the
// same cards as a video when ffmpeg is available.
func (s *Service) deliverPack(ctx context.Context, post Post) (string, error) {
	var pack packPayload
	if err := json.Unmarshal(post.Payload, &pack); err != nil {
		return "", fmt.Errorf("decode pack: %w", err)
	}
	slides := make([]Media, 0, len(pack.Tokens))
	for i, token := range pack.Tokens {
		media, err := s.Cards.Render(ctx, token)
		if err != nil {
			return "", err
		}
		media.Name = fmt.Sprintf("sokalens-story-%d.png", i+1)
		slides = append(slides, media)
	}
	if len(slides) == 0 {
		return "", errors.New("empty pack")
	}
	chat := s.Config.TelegramOwnerChatID
	if _, err := s.Telegram.SendMessage(ctx, chat, post.Caption); err != nil {
		return "", err
	}
	// An album needs at least two items; a day whose matches had too little
	// history for a preview card has only the cover.
	var id string
	var err error
	if len(slides) == 1 {
		id, err = s.Telegram.SendPhoto(ctx, chat, slides[0], "")
	} else {
		id, err = s.Telegram.SendMediaGroup(ctx, chat, slides)
	}
	if err != nil {
		return "", err
	}
	if pack.Video && s.Video != nil {
		video, err := s.Video.Build(ctx, slides)
		switch {
		case errors.Is(err, ErrNoFFmpeg):
			s.logger().Info("marketing video skipped: ffmpeg not installed")
		case err != nil:
			// The album already went out; a failed video is reported, not retried,
			// so the owner is not sent the album twice.
			s.tellOwner(ctx, "Today's video could not be built: "+err.Error())
		default:
			if _, err := s.Telegram.SendVideo(ctx, chat, video, "Today's video: add a trending sound in TikTok or Reels, then post."); err != nil {
				s.tellOwner(ctx, "Today's video could not be sent: "+err.Error())
			}
		}
	}
	return id, nil
}

func (s *Service) retryFailed(ctx context.Context, now time.Time) error {
	posts, err := s.Store.RetryablePosts(ctx, now.Add(-6*time.Hour))
	if err != nil {
		return err
	}
	var errs []error
	for _, post := range posts {
		if err := s.send(ctx, post); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// localDay returns the local calendar day as a key, and its start in UTC.
func (s *Service) localDay(t time.Time) (string, time.Time) {
	local := t.In(s.Config.Location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.Config.Location)
	return start.Format("2006-01-02"), start.UTC()
}

func isoWeekKey(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

type picksPayload struct {
	Day   string `json:"day"`
	Picks []Pick `json:"picks"`
}

func (s *Service) dailyPicks(ctx context.Context, now time.Time) error {
	local := now.In(s.Config.Location)
	if local.Hour() < s.Config.PicksHour || local.Hour() >= 22 {
		return nil
	}
	day, dayStart := s.localDay(now)
	key := "picks:" + day
	if exists, err := s.Store.KeyExists(ctx, key); err != nil || exists {
		return err
	}
	// Tonight runs past midnight here: a 21:00 kick-off in London is 00:00
	// in Nairobi, and belongs to today's card, not tomorrow's.
	picks, err := s.Store.CandidatePicks(ctx, PickQuery{
		From:              now.Add(30 * time.Minute),
		To:                dayStart.Add(30 * time.Hour),
		LeagueIDs:         s.Config.LeagueIDs,
		MinLeaguePriority: s.Config.MinLeaguePriority,
		Limit:             s.Config.PicksPerDay,
	})
	if err != nil {
		return err
	}
	if len(picks) == 0 {
		return s.publish(ctx, outgoing{
			Key: key, Kind: KindPicks, Payload: picksPayload{Day: day},
			Caption: func(string) string {
				return "No published picks in the covered leagues today, so nothing was posted. The results card is skipped too."
			},
		})
	}

	label := LocalDay(now, s.Config.Location)
	card := Card{Kind: KindPicks, Format: FormatSquare, Eyebrow: "Today's picks · " + label, Note: "Graded tonight: wins and losses"}
	for _, p := range picks {
		card.Items = append(card.Items, CardItem{
			League: p.League, Home: p.Home, Away: p.Away, HomeLogo: p.HomeLogo, AwayLogo: p.AwayLogo,
			Kickoff: LocalTime(p.KickoffAt, s.Config.Location), Pick: PickLabel(p.Market, p.Selection, p.Home, p.Away),
			Confidence: Percent(p.Confidence),
		})
	}
	err = s.publish(ctx, outgoing{
		Key: key, Kind: KindPicks, Card: &card, Public: true, Facebook: true,
		Handoff: "For your WhatsApp Channel: forward this image with the text below.",
		Payload: picksPayload{Day: day, Picks: picks},
		Caption: func(source string) string {
			return PicksCaption(picks, label, s.Config.Location, s.Config.SiteURL, source)
		},
	})
	// The story pack is built even if a public channel failed: it goes to the
	// owner, and the retry step will take care of the public post.
	return errors.Join(err, s.storyPack(ctx, day, label, picks, card))
}

func (s *Service) storyPack(ctx context.Context, day, label string, picks []Pick, picksCard Card) error {
	key := "pack:" + day
	if exists, err := s.Store.KeyExists(ctx, key); err != nil || exists {
		return err
	}
	story := picksCard
	story.Format = FormatStory
	tokens := []string{}
	cover, err := SignCard(s.Config.CardSecret, story)
	if err != nil {
		return err
	}
	tokens = append(tokens, cover)
	for _, p := range picks {
		preview, ok, err := s.previewCard(ctx, p)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		token, err := SignCard(s.Config.CardSecret, preview)
		if err != nil {
			return err
		}
		tokens = append(tokens, token)
	}
	short := fmt.Sprintf("Today's %d picks, graded tonight. Full analysis at sokalens.com (link in bio). 18+ · Not a bookmaker.", len(picks))
	return s.publish(ctx, outgoing{
		Key: key, Kind: KindPack, Payload: packPayload{Tokens: tokens, Video: s.Config.VideoEnabled},
		Caption: func(string) string {
			return "Story pack for " + label + ": post these to WhatsApp Status, TikTok, Reels, Shorts and Instagram Story. " +
				"In TikTok and Reels, add a trending sound before posting.\n\nCaption to use:\n" + short
		},
	})
}

// previewCard builds the "3 stats before the match" story card. ok is false
// when there is too little history to say two true things.
func (s *Service) previewCard(ctx context.Context, p Pick) (Card, bool, error) {
	homeForm, err := s.Store.RecentForm(ctx, p.HomeTeamID, p.KickoffAt, 5)
	if err != nil {
		return Card{}, false, err
	}
	awayForm, err := s.Store.RecentForm(ctx, p.AwayTeamID, p.KickoffAt, 5)
	if err != nil {
		return Card{}, false, err
	}
	meetings, err := s.Store.HeadToHead(ctx, p.HomeTeamID, p.AwayTeamID, p.KickoffAt, 5)
	if err != nil {
		return Card{}, false, err
	}
	lines := PreviewLines(p, homeForm, awayForm, meetings)
	if len(lines) < 2 {
		return Card{}, false, nil
	}
	return Card{
		Kind: KindPreview, Format: FormatStory, Eyebrow: "Before kick-off",
		Match: &CardMatch{
			League: p.League, Home: p.Home, Away: p.Away, HomeLogo: p.HomeLogo, AwayLogo: p.AwayLogo,
			Kickoff: LocalDay(p.KickoffAt, s.Config.Location) + " · " + LocalTime(p.KickoffAt, s.Config.Location),
			Pick:    PickLabel(p.Market, p.Selection, p.Home, p.Away),
		},
		Lines: lines,
	}, true, nil
}

func (s *Service) results(ctx context.Context, now time.Time) error {
	posts, err := s.Store.PostsByKind(ctx, KindPicks, now.Add(-48*time.Hour))
	if err != nil {
		return err
	}
	var errs []error
	for _, post := range posts {
		var payload picksPayload
		if err := json.Unmarshal(post.Payload, &payload); err != nil || len(payload.Picks) == 0 {
			continue
		}
		key := "results:" + payload.Day
		if exists, err := s.Store.KeyExists(ctx, key); err != nil || exists {
			if err != nil {
				errs = append(errs, err)
			}
			continue
		}
		graded, err := s.Store.GradePicks(ctx, payload.Picks)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !resultsDue(graded, now) {
			continue
		}
		dayLabel := LocalDay(payload.Picks[0].KickoffAt, s.Config.Location)
		if day, err := time.ParseInLocation("2006-01-02", payload.Day, s.Config.Location); err == nil {
			dayLabel = LocalDay(day, s.Config.Location)
		}
		card := Card{Kind: KindResults, Format: FormatSquare, Eyebrow: "Results · " + dayLabel, Headline: ResultsHeadline(graded), Note: "Every pick we post is graded here, the misses too"}
		for _, g := range graded {
			card.Items = append(card.Items, CardItem{
				League: g.League, Home: g.Home, Away: g.Away, HomeLogo: g.HomeLogo, AwayLogo: g.AwayLogo,
				Pick: PickLabel(g.Market, g.Selection, g.Home, g.Away), Score: g.Score(), Result: g.Result,
			})
		}
		errs = append(errs, s.publish(ctx, outgoing{
			Key: key, Kind: KindResults, Card: &card, Public: true, Facebook: true,
			Handoff: "Tonight's results for your WhatsApp Channel: forward this image with the text below.",
			Payload: map[string]any{"day": payload.Day},
			Caption: func(source string) string { return ResultsCaption(graded, dayLabel, s.Config.SiteURL, source) },
		}))
	}
	return errors.Join(errs...)
}

// resultsDue waits until every pick is decided, but not forever: six hours
// after the last kick-off the card goes out with anything undecided marked
// pending, so a stuck feed cannot silently swallow a bad night.
func resultsDue(graded []GradedPick, now time.Time) bool {
	var last time.Time
	pending := false
	for _, g := range graded {
		if g.KickoffAt.After(last) {
			last = g.KickoffAt
		}
		if g.Result == ResultPending {
			pending = true
		}
	}
	if now.Before(last.Add(105 * time.Minute)) {
		return false
	}
	return !pending || now.After(last.Add(6*time.Hour))
}

func (s *Service) weeklyRecord(ctx context.Context, now time.Time) error {
	local := now.In(s.Config.Location)
	if local.Weekday() != time.Monday || local.Hour() < 9 || local.Hour() >= 22 {
		return nil
	}
	_, todayStart := s.localDay(now)
	weekStart := todayStart.AddDate(0, 0, -7)
	key := "record:" + isoWeekKey(weekStart.In(s.Config.Location))
	if exists, err := s.Store.KeyExists(ctx, key); err != nil || exists {
		return err
	}
	posts, err := s.Store.PostsByKind(ctx, KindPicks, weekStart)
	if err != nil {
		return err
	}
	type day struct {
		label  string
		graded []GradedPick
	}
	var days []day
	var all []GradedPick
	for _, post := range posts {
		if !post.CreatedAt.Before(todayStart) {
			continue
		}
		var payload picksPayload
		if json.Unmarshal(post.Payload, &payload) != nil || len(payload.Picks) == 0 {
			continue
		}
		graded, err := s.Store.GradePicks(ctx, payload.Picks)
		if err != nil {
			return err
		}
		label := payload.Day
		if parsed, err := time.ParseInLocation("2006-01-02", payload.Day, s.Config.Location); err == nil {
			label = LocalDay(parsed, s.Config.Location)
		}
		days = append(days, day{label: label, graded: graded})
		all = append(all, graded...)
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(days, func(i, j int) bool { return days[i].graded[0].KickoffAt.Before(days[j].graded[0].KickoffAt) })
	won, decided := Tally(all)
	span := LocalDay(weekStart, s.Config.Location) + " to " + LocalDay(todayStart.Add(-time.Hour), s.Config.Location)
	card := Card{Kind: KindRecord, Format: FormatSquare, Eyebrow: "Last week · " + span, Headline: ResultsHeadline(all), Note: "Every pick we posted, graded"}
	if decided > 0 {
		card.Body = fmt.Sprintf("%d%% of graded picks", (won*100+decided/2)/decided)
	}
	for _, d := range days {
		dayWon, dayDecided := Tally(d.graded)
		card.Lines = append(card.Lines, fmt.Sprintf("%s: %d of %d", d.label, dayWon, dayDecided))
	}
	return s.publish(ctx, outgoing{
		Key: key, Kind: KindRecord, Card: &card, Public: true, Facebook: true,
		Handoff: "Last week's record for your WhatsApp Channel: forward this image with the text below.",
		Payload: map[string]any{"won": won, "decided": decided},
		Caption: func(source string) string {
			return fmt.Sprintf("Last week (%s): %s.\n\nWe grade every pick we post, wins and losses. The full public record:\n%s\n\n%s",
				span, ResultsHeadline(all), Link(s.Config.SiteURL, "/results", source, "weekly_record", isoWeekKey(weekStart.In(s.Config.Location))), Compliance)
		},
	})
}

// statMarkets rotate one per week, in a fixed order, so the weekly stat is
// never chosen by looking for the best-looking number.
var statMarkets = []struct{ key, name string }{
	{"1x2", "match result"},
	{"btts", "both-teams-to-score"},
	{"total_goals", "over/under goals"},
	{"double_chance", "double chance"},
}

func (s *Service) statDraft(ctx context.Context, now time.Time) error {
	local := now.In(s.Config.Location)
	if local.Weekday() != time.Thursday || local.Hour() < 10 || local.Hour() >= 22 {
		return nil
	}
	week := isoWeekKey(local)
	key := "stat:" + week
	if exists, err := s.Store.KeyExists(ctx, key); err != nil || exists {
		return err
	}
	_, isoWeek := local.ISOWeek()
	for offset := range statMarkets {
		market := statMarkets[(isoWeek+offset)%len(statMarkets)]
		record, err := s.Store.MarketRecord(ctx, RecordQuery{Market: market.key, Since: now.AddDate(0, 0, -90), LeagueIDs: s.Config.LeagueIDs, MinLeaguePriority: s.Config.MinLeaguePriority})
		if err != nil {
			return err
		}
		if record.Graded < 30 {
			continue
		}
		rate := (record.Correct*100 + record.Graded/2) / record.Graded
		card := Card{
			Kind: KindStat, Format: FormatSquare, Eyebrow: "Our record",
			Headline: fmt.Sprintf("%d%%", rate),
			Body:     fmt.Sprintf("of our top-league %s picks were right", market.name),
			Note:     fmt.Sprintf("%d of %d graded picks · last 90 days", record.Correct, record.Graded),
		}
		return s.publish(ctx, outgoing{
			Key: key, Kind: KindStat, Card: &card, Payload: record,
			Caption: func(string) string {
				return fmt.Sprintf("DRAFT for this week's data post. Check it against the public record before posting.\n\n"+
					"Suggested caption:\n%d%% of our top-league %s picks were right over the last 90 days (%d of %d graded). "+
					"Every pick is graded in public: %s\n\n%s",
					rate, market.name, record.Correct, record.Graded,
					Link(s.Config.SiteURL, "/results", "owner", "weekly_stat", week), Compliance)
			},
		})
	}
	return s.publish(ctx, outgoing{
		Key: key, Kind: KindNote,
		Caption: func(string) string {
			return "No weekly data post this week: no market has 30 graded top-league picks in the last 90 days yet."
		},
	})
}

func (s *Service) weeklyReport(ctx context.Context, now time.Time) error {
	local := now.In(s.Config.Location)
	if local.Weekday() != time.Sunday || local.Hour() < 20 {
		return nil
	}
	key := "report:" + isoWeekKey(local)
	if exists, err := s.Store.KeyExists(ctx, key); err != nil || exists {
		return err
	}
	numbers, err := s.Store.WeeklyNumbers(ctx, now.AddDate(0, 0, -7))
	if err != nil {
		return err
	}
	members := -1
	if s.Config.TelegramChannel != "" {
		if count, err := s.Telegram.MemberCount(ctx, s.Config.TelegramChannel); err == nil {
			members = count
		} else {
			s.logger().Warn("marketing telegram member count", "error", err)
		}
	}
	text := WeeklyReportText(numbers, members, isoWeekKey(local))
	return s.publish(ctx, outgoing{Key: key, Kind: KindReport, Payload: numbers, Caption: func(string) string { return text }})
}

// WeeklyReportText is the Sunday message: the decision numbers the database
// can answer, and a reminder of the ones it cannot.
func WeeklyReportText(n Numbers, telegramMembers int, week string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SokaLens weekly numbers · %s (last 7 days)\n\n", week)
	fmt.Fprintf(&b, "Accounts: %d total, %d new\n", n.Accounts, n.NewAccounts)
	fmt.Fprintf(&b, "Returning accounts (created earlier, active this week): %d\n", n.ReturningAccounts)
	fmt.Fprintf(&b, "Visitors: %d new, %d active\n", n.NewVisitors, n.ActiveVisitors)
	if telegramMembers >= 0 {
		fmt.Fprintf(&b, "Telegram channel members: %d\n", telegramMembers)
	}
	if len(n.VisitorsBySource) > 0 {
		b.WriteString("\nNew visitors by first source:\n")
		for _, source := range n.VisitorsBySource {
			fmt.Fprintf(&b, "- %s: %d\n", source.Source, source.Count)
		}
	}
	if len(n.PostsByChannel) > 0 {
		b.WriteString("\nAutomated posts:\n")
		for _, posts := range n.PostsByChannel {
			fmt.Fprintf(&b, "- %s %s: %d\n", posts.Channel, posts.Status, posts.Count)
		}
	}
	b.WriteString("\nAdd by hand: page views per day (Cloudflare Web Analytics), search impressions per day (Search Console), WhatsApp Channel followers.")
	return b.String()
}

type goalPayload struct {
	FixtureID int64  `json:"fixture_id"`
	HomeGoals int    `json:"home_goals"`
	AwayGoals int    `json:"away_goals"`
	League    string `json:"league"`
	Home      string `json:"home"`
	Away      string `json:"away"`
}

func (s *Service) liveTargets() bool {
	return s.Config.Mode == ModePreview || s.Config.TelegramChannelEnabled()
}

func (s *Service) liveGoals(ctx context.Context, now time.Time) error {
	if !s.liveTargets() {
		return nil
	}
	_, dayStart := s.localDay(now)
	posted, err := s.Store.CountKeysByPrefix(ctx, "goal:", dayStart)
	if err != nil || posted >= s.Config.LiveMaxPerDay {
		return err
	}
	picks, err := s.Store.PostsByKind(ctx, KindPicks, now.Add(-30*time.Hour))
	if err != nil {
		return err
	}
	var fixtureIDs []int64
	for _, post := range picks {
		var payload picksPayload
		if json.Unmarshal(post.Payload, &payload) == nil {
			for _, p := range payload.Picks {
				fixtureIDs = append(fixtureIDs, p.FixtureID)
			}
		}
	}
	goals, err := s.Store.RecentGoals(ctx, GoalQuery{
		Since: now.Add(-20 * time.Minute), FixtureIDs: fixtureIDs,
		LeagueIDs: s.Config.LiveLeagueIDs, MinLeaguePriority: s.Config.LiveMinLeaguePriority,
	})
	if err != nil {
		return err
	}
	sort.Slice(goals, func(i, j int) bool { return goals[i].CapturedAt.Before(goals[j].CapturedAt) })
	var errs []error
	for _, goal := range goals {
		if goal.HomeGoals+goal.AwayGoals == 0 {
			continue
		}
		if exists, err := s.Store.KeyExists(ctx, goal.Key()); err != nil || exists {
			if err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if posted >= s.Config.LiveMaxPerDay {
			break
		}
		posted++
		card := Card{
			Kind: KindGoal, Format: FormatSquare, Eyebrow: "Goal · " + goal.MinuteLabel(),
			Match: &CardMatch{League: goal.League, Home: goal.Home, Away: goal.Away, HomeLogo: goal.HomeLogo, AwayLogo: goal.AwayLogo,
				Score: fmt.Sprintf("%d-%d", goal.HomeGoals, goal.AwayGoals), Minute: goal.MinuteLabel(), Event: goal.Scorer},
			Probs: goal.Probs,
		}
		errs = append(errs, s.publish(ctx, outgoing{
			Key: goal.Key(), Kind: KindGoal, Card: &card, Public: true,
			Payload: goalPayload{FixtureID: goal.FixtureID, HomeGoals: goal.HomeGoals, AwayGoals: goal.AwayGoals, League: goal.League, Home: goal.Home, Away: goal.Away},
			Caption: func(source string) string { return GoalCaption(goal, s.Config.SiteURL, source) },
		}))
	}
	return errors.Join(errs...)
}

// corrections posts once when the latest scoreline in a match is below one we
// announced: the goal was taken away. The original post stays; corrections
// are visible, never silent.
func (s *Service) corrections(ctx context.Context, now time.Time) error {
	if !s.liveTargets() {
		return nil
	}
	posts, err := s.Store.PostsByKind(ctx, KindGoal, now.Add(-3*time.Hour))
	if err != nil || len(posts) == 0 {
		return err
	}
	byFixture := map[int64][]goalPayload{}
	var ids []int64
	for _, post := range posts {
		var g goalPayload
		if json.Unmarshal(post.Payload, &g) != nil || g.FixtureID == 0 {
			continue
		}
		if _, seen := byFixture[g.FixtureID]; !seen {
			ids = append(ids, g.FixtureID)
		}
		byFixture[g.FixtureID] = append(byFixture[g.FixtureID], g)
	}
	latest, err := s.Store.LatestScores(ctx, ids)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		score, ok := latest[id]
		if !ok {
			continue
		}
		for _, g := range byFixture[id] {
			if g.HomeGoals <= score[0] && g.AwayGoals <= score[1] {
				continue
			}
			key := fmt.Sprintf("var:%d:%d-%d", g.FixtureID, g.HomeGoals, g.AwayGoals)
			if exists, err := s.Store.KeyExists(ctx, key); err != nil || exists {
				continue
			}
			now := Goal{FixtureID: g.FixtureID, HomeGoals: score[0], AwayGoals: score[1], League: g.League, Home: g.Home, Away: g.Away}
			errs = append(errs, s.publish(ctx, outgoing{
				Key: key, Kind: KindNote, Public: true, Payload: g,
				Caption: func(source string) string { return CorrectionCaption(now, s.Config.SiteURL, source) },
			}))
		}
	}
	return errors.Join(errs...)
}
