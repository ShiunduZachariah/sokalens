package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// TypesenseClient is a minimal Typesense REST client for the entities
// collection used by search and autocomplete.
type TypesenseClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewTypesenseClient(baseURL, apiKey string) *TypesenseClient {
	return &TypesenseClient{
		baseURL:    trimSlash(baseURL),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

type typesenseField struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Sort bool   `json:"sort,omitempty"`
}

type typesenseSchema struct {
	Name   string           `json:"name"`
	Fields []typesenseField `json:"fields"`
}

// EnsureCollection creates the entities collection if it does not exist.
func (c *TypesenseClient) EnsureCollection(ctx context.Context) error {
	schema := typesenseSchema{
		Name: "entities",
		Fields: []typesenseField{
			{Name: "type", Type: "string"},
			{Name: "entity_id", Type: "int64"},
			{Name: "name", Type: "string", Sort: true},
			{Name: "subtitle", Type: "string"},
			{Name: "slug", Type: "string"},
			{Name: "kickoff_at", Type: "int64"},
			{Name: "status", Type: "string"},
			{Name: "logo_url", Type: "string"},
		},
	}
	body, _ := json.Marshal(schema)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/collections", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 201 = created, 409 = already exists; both are fine.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("ensure collection: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// TypesenseDocument is one indexed search entity.
type TypesenseDocument struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	EntityID  int64  `json:"entity_id"`
	Name      string `json:"name"`
	Subtitle  string `json:"subtitle"`
	Slug      string `json:"slug"`
	KickoffAt int64  `json:"kickoff_at"`
	Status    string `json:"status"`
	LogoURL   string `json:"logo_url"`
}

// Index upserts a batch of documents into the entities collection.
func (c *TypesenseClient) Index(ctx context.Context, documents []TypesenseDocument) error {
	if len(documents) == 0 {
		return nil
	}
	payload := make([]byte, 0, 1024*len(documents))
	for _, document := range documents {
		encoded, err := json.Marshal(document)
		if err != nil {
			return err
		}
		payload = append(payload, encoded...)
		payload = append(payload, '\n')
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/collections/entities/documents/import?action=upsert", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("index documents: unexpected status %d", resp.StatusCode)
	}
	return nil
}

type typesenseHit struct {
	Document TypesenseDocument `json:"document"`
}

type typesenseSearchResponse struct {
	Found int            `json:"found"`
	Hits  []typesenseHit `json:"hits"`
}

// CountDocuments returns the number of documents in the entities collection
// (used for search-health/coverage reporting in the admin plane). Some
// Typesense versions do not implement the count=true endpoint, so this uses a
// wildcard search and reads the found total.
func (c *TypesenseClient) CountDocuments(ctx context.Context) (int, error) {
	params := url.Values{}
	params.Set("q", "*")
	params.Set("per_page", "1")
	params.Set("query_by", "name")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/collections/entities/documents/search?"+params.Encode(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("count documents: unexpected status %d", resp.StatusCode)
	}
	var parsed struct {
		Found int `json:"found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, err
	}
	return parsed.Found, nil
}

// Search queries the entities collection.
func (c *TypesenseClient) Search(ctx context.Context, query string, limit int) ([]TypesenseDocument, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("per_page", fmt.Sprintf("%d", limit))
	params.Set("query_by", "name,subtitle,slug")
	// The name is what the user typed; the subtitle and slug are supporting
	// context and should not outrank it.
	params.Set("query_by_weights", "6,2,1")
	// An autocomplete is typed one character at a time, so every token is a
	// prefix in progress, not a finished word.
	params.Set("prefix", "true,true,true")
	params.Set("num_typos", "2")
	// "sunderland vs mancity" is three tokens and no document contains all
	// three. Dropping the tokens that match nothing is what turns that into
	// the Sunderland/Manchester City fixture instead of an empty dropdown.
	params.Set("drop_tokens_threshold", "1")
	params.Set("typo_tokens_threshold", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/collections/entities/documents/search?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search documents: unexpected status %d", resp.StatusCode)
	}
	var parsed typesenseSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	documents := make([]TypesenseDocument, 0, len(parsed.Hits))
	for _, hit := range parsed.Hits {
		documents = append(documents, hit.Document)
	}
	return documents, nil
}

func trimSlash(value string) string {
	for len(value) > 0 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	return value
}
