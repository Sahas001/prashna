package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"radar/internal/item"
)

// Setting returns a value from the settings table ("" if unset).
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// JSONSetting decodes a setting stored with SetJSONSetting into v. It
// reports false (leaving v alone) if the setting is unset.
func (s *Store) JSONSetting(ctx context.Context, key string, v any) (bool, error) {
	raw, err := s.Setting(ctx, key)
	if err != nil || raw == "" {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

func (s *Store) SetJSONSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, key, string(b))
}

// TimeSetting reads a setting stored with SetTimeSetting (zero if unset).
func (s *Store) TimeSetting(ctx context.Context, key string) (time.Time, error) {
	v, err := s.Setting(ctx, key)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, nil // corrupt value: treat as unset
	}
	return time.Unix(n, 0).UTC(), nil
}

func (s *Store) SetTimeSetting(ctx context.Context, key string, t time.Time) error {
	return s.SetSetting(ctx, key, strconv.FormatInt(t.Unix(), 10))
}

// FeedState is what Radar remembers about one RSS/Atom feed.
type FeedState struct {
	URL           string
	Title         string
	ETag          string
	LastModified  string
	LastFetchedAt time.Time
	LastOKAt      time.Time
	LastError     string
	LastItems     int
}

// FeedState returns the stored state for url (zero value with URL set if unseen).
func (s *Store) FeedState(ctx context.Context, url string) (FeedState, error) {
	fs := FeedState{URL: url}
	var fetched, ok sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT title, etag, last_modified, last_fetched_at, last_ok_at, last_error, last_items FROM feed_state WHERE url = ?`, url).
		Scan(&fs.Title, &fs.ETag, &fs.LastModified, &fetched, &ok, &fs.LastError, &fs.LastItems)
	if errors.Is(err, sql.ErrNoRows) {
		return fs, nil
	}
	if fetched.Valid {
		fs.LastFetchedAt = time.Unix(fetched.Int64, 0).UTC()
	}
	if ok.Valid {
		fs.LastOKAt = time.Unix(ok.Int64, 0).UTC()
	}
	return fs, err
}

func (s *Store) SaveFeedState(ctx context.Context, fs FeedState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO feed_state
		(url, title, etag, last_modified, last_fetched_at, last_ok_at, last_error, last_items)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (url) DO UPDATE SET title = excluded.title, etag = excluded.etag,
			last_modified = excluded.last_modified, last_fetched_at = excluded.last_fetched_at,
			last_ok_at = excluded.last_ok_at, last_error = excluded.last_error, last_items = excluded.last_items`,
		fs.URL, fs.Title, fs.ETag, fs.LastModified, nullTime(fs.LastFetchedAt), nullTime(fs.LastOKAt), fs.LastError, fs.LastItems)
	return err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

// ItemsForScoring returns items fetched at or after since, plus every
// unsent, unrated book regardless of age: books stay eligible for "pick of
// the week" long after they were first fetched, so their scores must keep
// tracking the profile and likes.
func (s *Store) ItemsForScoring(ctx context.Context, since time.Time) ([]item.Item, error) {
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items
		WHERE fetched_at >= ? OR (type = 'book' AND digested_at IS NULL AND feedback = 0)`, since.Unix())
}
