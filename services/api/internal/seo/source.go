// Package seo serves the crawlable URL set to the sitemap builders.
//
// It exists because the sitemaps were assembled from reader-facing API calls,
// and those carry pagination limits that have nothing to do with whether a
// page should rank. Measured on 2026-09-21:
//
//	              advertised   actually indexable
//	fixtures           3,152                7,626
//	teams              1,194               12,906
//	leagues               82                  845
//	                   -----               ------
//	                   4,462               21,377
//
// The league loader asked the API for 100 leagues and got 82 with content, of
// 845 that qualify. The team loader fanned out over those same 100 leagues and
// inherited the ceiling. The fixture loader filtered to the 1x2 market, so a
// fixture predicted in any other market was invisible to it.
//
// None of those numbers encode a decision about what should be indexed. They
// are artefacts of borrowing a reader's endpoint to build a crawler's
// document, and the result was telling Google about a fifth of the property
// while Googlebot crawls roughly 275 URLs a day.
//
// seo_urls already holds the right answer, maintained by the worker's sweep.
// This reads it, and has no cap of its own for the same reason.
package seo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxPerPage bounds one response.
//
// The sitemap protocol allows 50,000 URLs per file, and a single page of that
// size is a large response to build and hold in memory. 10,000 keeps each
// response modest while needing only two pages for the largest set (teams, at
// 12,906), so the loaders stay simple.
const MaxPerPage = 10000

// Fixture sitemap window.
//
// A fixture stays indexable for fourteen days after kickoff, but that is the
// page's promise to a reader who already has the link, not a reason to keep
// asking Google to crawl it. On 2026-09-24 fixtures.xml carried 7,869 URLs,
// 1,300 of them re-dated that day, and the fixture used as the example had
// never been crawled at all: Google was spending a young domain's small crawl
// allowance re-reading finished matches.
//
// Search demand for a prediction peaks in the days before kickoff and a
// "result" search lingers a day or two after, so the sitemap advertises that
// band and nothing else. This narrows what is advertised, never what is
// indexable: every row it returns is still an indexable row, so the invariant
// the deploy checks -- the sitemap never lists a noindex page -- is untouched.
const (
	FixtureSitemapLookback  = 2 * 24 * time.Hour
	FixtureSitemapLookahead = 5 * 24 * time.Hour
)

// crawlableWhere is the predicate List and Count share, so the sitemap index
// and its chunks can never disagree about how many URLs exist. $1 is always the
// entity type; fixtures add the kickoff band as $2 and $3.
func crawlableWhere(entityType string, now time.Time) (string, []any) {
	if entityType == "team" {
		return `entity_type = $1 AND indexable
  AND NOT EXISTS (SELECT 1 FROM team_identity_aliases tia WHERE tia.old_id = seo_urls.entity_id AND tia.revoked_at IS NULL)`, []any{entityType}
	}
	if entityType != "fixture" {
		return "entity_type = $1 AND indexable", []any{entityType}
	}
	return `entity_type = $1 AND indexable
  AND EXISTS (
    SELECT 1 FROM matches m
    WHERE m.id = seo_urls.entity_id
      AND m.kickoff_at >= $2
      AND m.kickoff_at < $3
      AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = m.id AND fia.revoked_at IS NULL)
  )`, []any{
			entityType,
			// RFC 3339 text rather than time.Time, as the matches source does, so
			// the same statement runs on the SQLite test database.
			now.Add(-FixtureSitemapLookback).UTC().Format(time.RFC3339),
			now.Add(FixtureSitemapLookahead).UTC().Format(time.RFC3339),
		}
}

// URL is one crawlable path.
type URL struct {
	Path         string
	LastModified *time.Time
}

// Source reads the crawlable set.
type Source struct {
	db  *sql.DB
	now func() time.Time
}

func NewSource(db *sql.DB) *Source { return &Source{db: db, now: time.Now} }

// allowedTypes is the set a caller may ask for.
//
// A whitelist rather than passing the parameter through, because entity_type
// reaches a SQL predicate: an unrecognised value should be a 400, not a
// question for the database.
var allowedTypes = map[string]bool{
	"fixture": true, "team": true, "league": true,
	"country": true, "market": true, "date_hub": true, "static": true,
}

// ValidType reports whether an entity type may be requested.
func ValidType(entityType string) bool {
	return allowedTypes[strings.ToLower(strings.TrimSpace(entityType))]
}

// List returns indexable paths of one entity type.
//
// Only indexable rows. A sitemap that lists a noindexed URL is a contradiction
// the deploy gate fails the build over -- correctly, because it is also how a
// property teaches Google to distrust the file. The filter lives here rather
// than in the caller so no caller can forget it.
//
// Ordered by path, not by last_modified. Paging needs a total order that does
// not move between requests, and last_modified changes whenever a prediction
// republishes -- which would shuffle rows across page boundaries mid-crawl and
// silently drop some from the sitemap.
func (s *Source) List(ctx context.Context, entityType string, offset, limit int) ([]URL, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("seo url source has no database")
	}
	entityType = strings.ToLower(strings.TrimSpace(entityType))
	if !ValidType(entityType) {
		return nil, fmt.Errorf("unknown seo entity type %q", entityType)
	}
	if limit <= 0 || limit > MaxPerPage {
		limit = MaxPerPage
	}
	if offset < 0 {
		offset = 0
	}

	where, args := crawlableWhere(entityType, s.now())
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT path, last_modified
FROM seo_urls
WHERE %s
ORDER BY path
LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list seo urls: %w", err)
	}
	defer rows.Close()

	out := make([]URL, 0, limit)
	for rows.Next() {
		var (
			path         string
			lastModified sql.NullTime
		)
		if err := rows.Scan(&path, &lastModified); err != nil {
			return nil, fmt.Errorf("scan seo url: %w", err)
		}
		url := URL{Path: path}
		if lastModified.Valid {
			at := lastModified.Time.UTC()
			url.LastModified = &at
		}
		out = append(out, url)
	}
	return out, rows.Err()
}

// Count reports how many indexable paths exist for a type, so a caller can
// page without discovering the end by receiving a short response.
func (s *Source) Count(ctx context.Context, entityType string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("seo url source has no database")
	}
	entityType = strings.ToLower(strings.TrimSpace(entityType))
	if !ValidType(entityType) {
		return 0, fmt.Errorf("unknown seo entity type %q", entityType)
	}
	var total int
	where, args := crawlableWhere(entityType, s.now())
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM seo_urls WHERE "+where, args...).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count seo urls: %w", err)
	}
	return total, nil
}
