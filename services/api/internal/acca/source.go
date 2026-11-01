package acca

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Query struct {
	Tab, Period, Country, League, Selection string
	Legs                                    *int
	MinFairOdds, MaxFairOdds                *float64
	Limit                                   int
	Cursor, Seed                            string
	// Generate lets a caller fall back to request-time composition when the
	// precomputed inventory holds nothing for these filters. Browse pages leave
	// it off; the generator page turns it on.
	Generate bool
}

type Leg struct {
	Position     int       `json:"position"`
	Source       string    `json:"source"`
	PredictionID string    `json:"prediction_id"`
	MatchID      int64     `json:"match_id"`
	Market       string    `json:"market"`
	Selection    string    `json:"selection"`
	FairOdds     float64   `json:"fair_odds"`
	KickoffAt    time.Time `json:"kickoff_at"`
	HomeTeam     string    `json:"home_team"`
	AwayTeam     string    `json:"away_team"`
	HomeTeamLogo string    `json:"home_team_logo"`
	AwayTeamLogo string    `json:"away_team_logo"`
	League       string    `json:"league"`
	Country      string    `json:"country"`

	// Slugs for the /teams/{slug} and /leagues/{slug} routes.
	//
	// FLAT, beside the names and logos they belong to, rather than nested team
	// and league objects. Nesting would read better on a blank page, but these
	// legs are already on the wire and the web app already consumes them, so
	// restructuring is a breaking change to buy tidiness. Additive fields next
	// to their siblings cost nothing and match what is here.
	//
	// Empty rather than absent when a slug is missing, because the clients
	// already handle that: validCatalogSlug falls back to a search link. An
	// omitted key would make them handle the same gap a second way.
	HomeTeamSlug string `json:"home_team_slug"`
	AwayTeamSlug string `json:"away_team_slug"`
	LeagueSlug   string `json:"league_slug"`
	// Status is how this leg finished: pending, won, lost or void.
	//
	// It is graded by the SAME expression the match page uses, inside the legs
	// subquery, rather than re-derived here. Two mappings of
	// prediction_results onto a word is two definitions of "won", and the first
	// time they drifted an ACCA would call a leg won on a match page that
	// called it void.
	//
	// Omitted on the list endpoint, where it is always "pending" by
	// construction: that endpoint only serves slips whose earliest kickoff is
	// still in the future.
	Status string `json:"status,omitempty"`
}
type Slip struct {
	ID               string    `json:"id"`
	MarketTab        string    `json:"market_tab"`
	Period           string    `json:"period"`
	LegCount         int       `json:"leg_count"`
	CombinedFairOdds float64   `json:"combined_fair_odds"`
	RankingScore     float64   `json:"ranking_score"`
	OddsBand         string    `json:"odds_band"`
	EarliestKickoff  time.Time `json:"earliest_kickoff"`
	LatestKickoff    time.Time `json:"latest_kickoff"`
	Legs             []Leg     `json:"legs"`
	// Origin is empty for inventory rows and GeneratedOrigin for a slip composed
	// for this request, which is never stored and has no durable id.
	Origin string `json:"origin,omitempty"`
}
type Result struct {
	Items      []Slip `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	Limit      int    `json:"limit"`
	// Total is the whole active catalogue, not this page. Without it the web
	// layer falls back to len(Items) and tells visitors there are 10 ACCAs
	// when there are thousands.
	Total  int    `json:"total"`
	Facets Facets `json:"facets"`
}
type Facets struct {
	// Total rides along on the same aggregate round trip; it is lifted onto
	// Result and not exposed as part of the facets payload.
	Total           int                 `json:"total"`
	Markets         []string            `json:"markets"`
	Countries       []string            `json:"countries"`
	Leagues         []string            `json:"leagues"`
	LegCounts       []int               `json:"leg_counts"`
	LeagueCountries map[string][]string `json:"league_countries"`
}
type Source struct {
	db     *sql.DB
	schema string
}

var schemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func NewSource(db *sql.DB, schema string) *Source {
	if strings.TrimSpace(schema) == "" {
		schema = "public"
	}
	if !schemaPattern.MatchString(schema) {
		schema = "public"
	}
	return &Source{db: db, schema: schema}
}

// legsQuery builds the per-slip leg aggregate.
//
// The team and league slugs come from the ht/at/lg joins this query already
// makes for the names, so a leg can link to /teams/{slug} and /leagues/{slug}
// without a second round trip. The MATCH link needs no slug: the clients build
// it from the match id plus the league and team names, all of which are
// already here.
//
// The team logos come from the ht/at joins this query already makes for the
// names. They were missing until 2026-09-04, so every leg fell back to the
// generated initials crest -- which reads as a rendering bug but was a
// missing column. COALESCE to ” rather than NULL: TeamIdentity keys on the
// empty string to decide between an <img> and a drawn crest, and a JSON null
// would reach it as the string "null".
func legsQuery(schema string) string {
	return fmt.Sprintf(`COALESCE((SELECT jsonb_agg(jsonb_build_object('position',l.position,'source',l.prediction_source,'prediction_id',l.prediction_id,'match_id',l.match_id,'market',l.market_key,'selection',l.selection,'fair_odds',l.fair_odds_at_generation,'kickoff_at',m.kickoff_at,'home_team',ht.name,'away_team',at.name,'home_team_logo',COALESCE(ht.logo_url,''),'away_team_logo',COALESCE(at.logo_url,''),'league',lg.name,'country',lg.country_code,'home_team_slug',COALESCE(ht.slug,''),'away_team_slug',COALESCE(at.slug,''),'league_slug',COALESCE(lg.slug,''),'status',%s) ORDER BY l.position)
 FROM %s.acca_slip_legs l JOIN matches m ON m.id=l.match_id JOIN teams ht ON ht.id=m.home_team_id JOIN teams at ON at.id=m.away_team_id JOIN leagues lg ON lg.id=m.league_id LEFT JOIN prediction_results pr ON pr.prediction_id=CASE WHEN l.prediction_source='engine' AND l.prediction_id ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' THEN l.prediction_id::uuid END WHERE l.slip_id=s.id),'[]'::jsonb)`, legStatusExpression(), schema)
}

// legStatusExpression grades one leg: pending, won, lost or void.
//
// THE MAPPING IS COPIED FROM THE MATCH PAGE ON PURPOSE AND MUST STAY EQUAL TO
// IT (services/api/internal/matches/source.go, matchPredictions). A leg and a
// match page describing the same prediction with different words is the defect
// this shape exists to avoid, and it is the kind a reader finds before we do:
// they open the ACCA, see a leg marked won, tap through to the match, and read
// void.
//
// No result row means pending, which is also the answer for every leg of every
// slip the list endpoint serves -- it only serves slips that have not kicked
// off. "Send pending until settlement exists" therefore needs no special case;
// it is what the absence of a row already says.
//
// Guarded on prediction_source: prediction_id is engine_predictions.id today
// and `engine` is the only source the candidate view produces, but a second
// source would carry ids from somewhere else entirely, and joining those
// against prediction_results would grade a leg from an unrelated row. An
// unrecognised source reads pending, which is honest -- we have no settlement
// for it -- rather than wrong.
//
// THE JOIN NEEDS A CAST, and the cast needs a CASE. acca_slip_legs.prediction_id
// is TEXT while prediction_results.prediction_id is UUID, so `pr.prediction_id
// = l.prediction_id` does not compare -- PostgreSQL rejects it outright with
// "operator does not exist: uuid ~~ unknown", which is a 503 on every request,
// not a wrong answer. Found by running the query against the real schema; no
// amount of stubbing would have shown it.
//
// Casting the UUID side to text would compare fine and silently stop the join
// using the index on prediction_results.prediction_id, turning ~20 indexed
// lookups per slip into ~20 scans of a table that grows with every settled
// prediction. So the TEXT side is cast instead, inside a CASE: CASE evaluates
// only the branch it needs, so a future non-engine source carrying a
// non-UUID id yields NULL and matches nothing rather than raising
// "invalid input syntax for type uuid" mid-response. The regex is belt and
// braces on the same point.
func legStatusExpression() string {
	return `CASE
    WHEN l.prediction_source <> 'engine' THEN 'pending'
    WHEN pr.prediction_id IS NULL THEN 'pending'
    WHEN pr.settlement_status IN ('void', 'unsettled_data_unavailable') THEN 'void'
    WHEN pr.is_correct THEN 'won'
    ELSE 'lost'
  END`
}

// ErrSlipNotFound is returned when no servable slip carries that id.
//
// One error for every reason a slip is not servable -- unknown id, malformed
// id, inventory marked invalid, inventory below the publication standard --
// because the caller turns all of them into the same 404 and distinguishing
// them on the wire would tell a stranger which ids exist.
var ErrSlipNotFound = errors.New("acca slip not found")

// uuidPattern is what acca_slips.id is: a UUID, as 000057 declares it.
//
// Checked in Go BEFORE the query, not left to PostgreSQL. `WHERE s.id = $1`
// against a non-UUID string raises `invalid input syntax for type uuid`, which
// is an error, which the handler would turn into a 503 "inventory unavailable".
// A typo in a URL is a 404, not an outage, and it must not read as one on the
// status page either.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Get returns one slip by id, in the same shape as an item from List.
//
// IDENTICAL SHAPE BY CONSTRUCTION, not by agreement: the same column list and
// the same legsQuery feed the same Slip struct. A hand-built projection here
// would be a second definition of an ACCA that drifts the first time a field is
// added to one and not the other.
//
// TWO FILTERS FROM List ARE KEPT AND ONE IS DROPPED, and the difference is the
// point of the endpoint:
//
//   - status: an `invalid` slip is bad inventory awaiting the reconciliation
//     worker and must not be served; `active` and `expired` both are. Expired
//     is the state the browse list exists to hide and this endpoint exists to
//     show -- a slip whose matches have been played is exactly when a reader
//     wants it.
//   - the publication standard is kept, because "tier rules match the list
//     endpoint" and the standard IS that rule. A slip below the odds floor is
//     hidden from the list and 404s here, identically.
//   - `earliest_kickoff > now()` is DROPPED. It is a browse-freshness filter,
//     not an eligibility rule. Keeping it would 404 every slip the moment its
//     first match kicked off, which would make leg statuses unreachable: they
//     only become interesting after kickoff, so an endpoint that reports them
//     and expires at kickoff reports nothing but `pending` for ever.
func (s *Source) Get(ctx context.Context, id string) (Slip, error) {
	if s == nil || s.db == nil {
		return Slip{}, errors.New("acca source unavailable")
	}
	if !uuidPattern.MatchString(strings.TrimSpace(id)) {
		return Slip{}, ErrSlipNotFound
	}

	query := fmt.Sprintf(`SELECT s.id,s.market_tab,s.period,s.leg_count,s.combined_fair_odds,s.ranking_score,s.odds_band,s.earliest_kickoff,s.latest_kickoff,
%s
		FROM %s.acca_slips s WHERE s.id=$1 AND s.status IN ('active','expired') AND %s`,
		legsQuery(s.schema), s.schema, SystemStandardSQLPredicate(s.schema, "s"))

	var slip Slip
	var raw []byte
	err := s.db.QueryRowContext(ctx, query, strings.TrimSpace(id)).Scan(
		&slip.ID, &slip.MarketTab, &slip.Period, &slip.LegCount, &slip.CombinedFairOdds,
		&slip.RankingScore, &slip.OddsBand, &slip.EarliestKickoff, &slip.LatestKickoff, &raw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Slip{}, ErrSlipNotFound
	}
	if err != nil {
		return Slip{}, err
	}
	if err := json.Unmarshal(raw, &slip.Legs); err != nil {
		return Slip{}, err
	}
	// The same guard List applies per row, and for the same reason: Go's JSON
	// encoder rejects NaN and +Inf, so a bad inventory row would not become a
	// wrong price, it would become a 500 halfway through a response body. A
	// slip that fails it is not servable, which is a 404.
	if !validSlip(slip) {
		return Slip{}, ErrSlipNotFound
	}
	return slip, nil
}

func (s *Source) List(ctx context.Context, q Query) (Result, error) {
	if s == nil || s.db == nil {
		return Result{}, errors.New("acca source unavailable")
	}
	limit := q.Limit
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	seed := q.Seed
	if seed == "" {
		seed = "default"
	}
	where := []string{"s.status='active'", "s.earliest_kickoff > now()", SystemStandardSQLPredicate(s.schema, "s")}
	args := []any{seed}
	n := 1
	if q.Tab != "" && q.Tab != "all" {
		n++
		where = append(where, fmt.Sprintf("s.market_tab=$%d", n))
		args = append(args, q.Tab)
	}
	if q.Period != "" && q.Period != "all" {
		n++
		where = append(where, fmt.Sprintf("s.period=$%d", n))
		args = append(args, q.Period)
	}
	if q.Legs != nil {
		n++
		where = append(where, fmt.Sprintf("s.leg_count=$%d", n))
		args = append(args, *q.Legs)
	}
	if q.MinFairOdds != nil {
		n++
		where = append(where, fmt.Sprintf("s.combined_fair_odds >= $%d", n))
		args = append(args, *q.MinFairOdds)
	}
	if q.MaxFairOdds != nil {
		n++
		where = append(where, fmt.Sprintf("s.combined_fair_odds <= $%d", n))
		args = append(args, *q.MaxFairOdds)
	}
	if predicate, values := geographicPredicate(s.schema, q.Country, q.League, n+1); len(values) > 0 {
		where = append(where, predicate)
		args = append(args, values...)
		n += len(values)
	}
	if q.Selection != "" {
		n++
		where = append(where, selectionPredicate(s.schema, n))
		args = append(args, q.Selection)
	}
	if q.Cursor != "" {
		c, err := decodeCursor(q.Cursor)
		if err != nil {
			return Result{}, err
		}
		n++
		where = append(where, fmt.Sprintf("md5(s.composition_hash || $1) > $%d", n))
		args = append(args, c)
	}
	query := fmt.Sprintf(`SELECT md5(s.composition_hash || $1),s.id,s.market_tab,s.period,s.leg_count,s.combined_fair_odds,s.ranking_score,s.odds_band,s.earliest_kickoff,s.latest_kickoff,
%s
		FROM %s.acca_slips s WHERE %s ORDER BY md5(s.composition_hash || $1), s.id LIMIT $%d`, legsQuery(s.schema), s.schema, strings.Join(where, " AND "), n+1)
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	out := Result{Items: make([]Slip, 0, limit), Limit: limit}
	var last string
	for rows.Next() {
		var x Slip
		var raw []byte
		var sortKey string
		if err := rows.Scan(&sortKey, &x.ID, &x.MarketTab, &x.Period, &x.LegCount, &x.CombinedFairOdds, &x.RankingScore, &x.OddsBand, &x.EarliestKickoff, &x.LatestKickoff, &raw); err != nil {
			return Result{}, err
		}
		if err := json.Unmarshal(raw, &x.Legs); err != nil {
			return Result{}, err
		}
		// Bad inventory rows must never escape as JSON (Go's encoder rejects
		// NaN/+Inf) or be presented as zero-priced selections. They are skipped
		// here and can be repaired by the background reconciliation worker.
		if !validSlip(x) {
			continue
		}
		if len(out.Items) < limit {
			out.Items = append(out.Items, x)
			last = sortKey
		}
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	// The inventory is a bounded sample of the possible combinations, so a
	// filter corner it never sampled would otherwise read as "no such ACCA
	// exists". Compose one from the same vetted pool instead, and only report
	// an empty result when the pool really cannot satisfy the filters.
	if len(out.Items) == 0 && q.Generate {
		if slip, ok, generateErr := s.generate(ctx, q); generateErr == nil && ok && validSlip(slip) {
			out.Items = append(out.Items, slip)
		} else {
			// Composing failed or ran out of budget. Record what was asked for
			// so the background composer stocks it, and the next reader to ask
			// this is served from inventory instead of waiting on a compose
			// that already failed once.
			s.recordDemand(q)
		}
	}
	out.Facets, err = s.facets(ctx)
	out.Total = out.Facets.Total
	if err != nil {
		return Result{}, err
	}
	if len(out.Items) == limit && last != "" {
		out.NextCursor = encodeCursor(last)
	}
	return out, nil
}

func validSlip(s Slip) bool {
	if s.ID == "" || s.LegCount < 2 || s.LegCount > 20 || len(s.Legs) != s.LegCount ||
		!finitePositive(s.CombinedFairOdds) || s.CombinedFairOdds > 1000 || !finite(s.RankingScore) ||
		s.EarliestKickoff.IsZero() || !s.LatestKickoff.IsZero() && s.LatestKickoff.Before(s.EarliestKickoff) {
		return false
	}
	if s.Origin != GeneratedOrigin && s.CombinedFairOdds < MinimumSystemCombinedFairOdds(s.LegCount) {
		return false
	}
	for _, l := range s.Legs {
		if l.MatchID <= 0 || l.Market == "" || l.Selection == "" || !finitePositive(l.FairOdds) || l.FairOdds > 1000 || l.KickoffAt.IsZero() {
			return false
		}
		if s.Origin != GeneratedOrigin && l.FairOdds < MinimumSystemLegFairOdds {
			return false
		}
	}
	return true
}

func finite(value float64) bool         { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finitePositive(value float64) bool { return finite(value) && value > 0 }

// facetsQuery is split out so the count's predicate can be asserted without a
// database. It must match the list query's predicate or the total will not
// describe what paging can actually reach.
func facetsQuery(schema string) string {
	standard := SystemStandardSQLPredicate(schema, "s")
	return fmt.Sprintf(`SELECT jsonb_build_object(
 'total', (SELECT count(*) FROM %[1]s.acca_slips s WHERE s.status='active' AND s.earliest_kickoff > now() AND %[2]s),
 'markets', COALESCE((SELECT jsonb_agg(x.market ORDER BY x.market) FROM (SELECT DISTINCT s.market_tab AS market FROM %[1]s.acca_slips s WHERE s.status='active' AND s.earliest_kickoff > now() AND %[2]s) x), '[]'::jsonb),
 'countries', COALESCE((SELECT jsonb_agg(x.country ORDER BY x.country) FROM (SELECT DISTINCT lg.country_code AS country FROM %[1]s.acca_slip_legs l JOIN %[1]s.acca_slips s ON s.id=l.slip_id JOIN matches m ON m.id=l.match_id JOIN leagues lg ON lg.id=m.league_id WHERE s.status='active' AND s.earliest_kickoff > now() AND %[2]s AND lg.country_code IS NOT NULL AND lg.country_code <> '') x), '[]'::jsonb),
 'leagues', COALESCE((SELECT jsonb_agg(x.league ORDER BY x.league) FROM (SELECT DISTINCT lg.name AS league FROM %[1]s.acca_slip_legs l JOIN %[1]s.acca_slips s ON s.id=l.slip_id JOIN matches m ON m.id=l.match_id JOIN leagues lg ON lg.id=m.league_id WHERE s.status='active' AND s.earliest_kickoff > now() AND %[2]s AND lg.name IS NOT NULL AND lg.name <> '') x), '[]'::jsonb),
 'leg_counts', COALESCE((SELECT jsonb_agg(x.legs ORDER BY x.legs) FROM (SELECT DISTINCT s.leg_count AS legs FROM %[1]s.acca_slips s WHERE s.status='active' AND s.earliest_kickoff > now() AND %[2]s) x), '[]'::jsonb),
 'league_countries', COALESCE((SELECT jsonb_object_agg(x.country, x.leagues) FROM (SELECT lg.country_code AS country, jsonb_agg(DISTINCT lg.name ORDER BY lg.name) AS leagues FROM %[1]s.acca_slip_legs l JOIN %[1]s.acca_slips s ON s.id=l.slip_id JOIN matches m ON m.id=l.match_id JOIN leagues lg ON lg.id=m.league_id WHERE s.status='active' AND s.earliest_kickoff > now() AND %[2]s AND lg.country_code IS NOT NULL AND lg.name IS NOT NULL GROUP BY lg.country_code) x), '{}'::jsonb))`, schema, standard)
}

func (s *Source) facets(ctx context.Context) (Facets, error) {
	query := facetsQuery(s.schema)
	var raw []byte
	if err := s.db.QueryRowContext(ctx, query).Scan(&raw); err != nil {
		return Facets{}, err
	}
	var out Facets
	if err := json.Unmarshal(raw, &out); err != nil {
		return Facets{}, err
	}
	return out, nil
}

// Both geographic constraints apply to one leg, not unrelated legs of a mixed slip.
func geographicPredicate(schema, country, league string, parameter int) (string, []any) {
	conditions := []string{"l.slip_id=s.id"}
	args := []any{}
	if country != "" && country != "all" {
		conditions = append(conditions, fmt.Sprintf("lower(lg.country_code)=lower($%d)", parameter))
		args = append(args, country)
		parameter++
	}
	if league != "" && league != "all" {
		conditions = append(conditions, fmt.Sprintf("(lower(lg.name)=lower($%d) OR cast(lg.id as text)=$%d)", parameter, parameter+1))
		args = append(args, league, league)
	}
	return fmt.Sprintf("EXISTS (SELECT 1 FROM %s.acca_slip_legs l JOIN matches m ON m.id=l.match_id JOIN leagues lg ON lg.id=m.league_id WHERE %s)", schema, strings.Join(conditions, " AND ")), args
}

func selectionPredicate(schema string, parameter int) string {
	return fmt.Sprintf("NOT EXISTS (SELECT 1 FROM %s.acca_slip_legs l WHERE l.slip_id=s.id AND (l.selection IS NULL OR l.selection <> $%d))", schema, parameter)
}

func ValidateCursor(v string) error {
	if v == "" {
		return nil
	}
	_, err := decodeCursor(v)
	return err
}

var cursorPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func encodeCursor(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
func decodeCursor(v string) (string, error) {
	if len(v) != 43 { // Raw URL-base64 of the 32-character MD5 sort key.
		return "", fmt.Errorf("invalid cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil {
		return "", fmt.Errorf("invalid cursor")
	}
	if !cursorPattern.Match(b) {
		return "", fmt.Errorf("invalid cursor")
	}
	return string(b), nil
}
func ParseInt(v string) (*int, error) {
	if v == "" || v == "all" {
		return nil, nil
	}
	x, e := strconv.Atoi(v)
	if e != nil || x < 2 || x > 20 {
		return nil, fmt.Errorf("legs must be between 2 and 20")
	}
	return &x, nil
}
func ParseOdds(v string, max float64) (*float64, error) {
	if v == "" {
		return nil, nil
	}
	x, e := strconv.ParseFloat(v, 64)
	if e != nil || !finite(x) || x < 0 || x > max {
		return nil, fmt.Errorf("invalid odds")
	}
	return &x, nil
}
