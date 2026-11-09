// Package store persists Items in SQLite (pure-Go driver, so the binary
// stays CGO-free and statically linkable).
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"radar/internal/item"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var (
	ErrNotFound  = errors.New("item not found")
	ErrAmbiguous = errors.New("id prefix matches more than one item")
)

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: a personal app never needs write concurrency, and this
	// rules out SQLITE_BUSY between the scheduler and web handlers.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate applies migrations/NNNN_*.sql files whose number exceeds
// PRAGMA user_version, each in its own transaction.
func (s *Store) migrate(ctx context.Context) error {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	for i, name := range names {
		version := i + 1
		if version <= current {
			continue
		}
		src, err := migrationFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(src)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

const itemCols = `id, type, title, url, source, summary, authors, tags, meta,
	published, fetched_at, score, sig_keyword, sig_similarity, sig_recency, feedback, saved_at, read_at, sig_learned, learned_why`

type scanner interface{ Scan(dest ...any) error }

func scanItem(row scanner) (item.Item, error) {
	var (
		it                   item.Item
		authors, tags, meta  string
		published, fetchedAt int64
		typ                  string
		feedback             int
		savedAt, readAt      sql.NullInt64
		learnedWhy           string
	)
	err := row.Scan(&it.ID, &typ, &it.Title, &it.URL, &it.Source, &it.Summary, &authors, &tags, &meta,
		&published, &fetchedAt, &it.Score, &it.Signals.Keyword, &it.Signals.Similarity, &it.Signals.Recency, &feedback,
		&savedAt, &readAt, &it.Signals.Learned, &learnedWhy)
	if err != nil {
		return it, err
	}
	if savedAt.Valid {
		it.SavedAt = time.Unix(savedAt.Int64, 0).UTC()
	}
	if readAt.Valid {
		it.ReadAt = time.Unix(readAt.Int64, 0).UTC()
	}
	it.Type = item.Type(typ)
	it.Feedback = item.Feedback(feedback)
	it.Published = time.Unix(published, 0).UTC()
	it.FetchedAt = time.Unix(fetchedAt, 0).UTC()
	for _, f := range []struct {
		src string
		dst any
	}{{authors, &it.Authors}, {tags, &it.Tags}, {meta, &it.Meta}, {learnedWhy, &it.Signals.LearnedWhy}} {
		if err := json.Unmarshal([]byte(f.src), f.dst); err != nil {
			return it, fmt.Errorf("item %s: decode json column: %w", it.ID, err)
		}
	}
	return it, nil
}

func (s *Store) queryItems(ctx context.Context, query string, args ...any) ([]item.Item, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item.Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v) // []string and map[string]string cannot fail
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func jsonMap(m map[string]string) string {
	if m == nil {
		return "{}"
	}
	return jsonOf(m)
}

// SaveStats reports what SaveItems did.
type SaveStats struct {
	Inserted int // new items
	Merged   int // duplicates (same URL, or same long title) folded into existing rows
}

// SaveItems inserts new items and merges duplicates into existing rows
// (matched by ID, then by dedupe title key) in a single transaction.
// Items must already be Normalized. Existing feedback and scores are kept.
func (s *Store) SaveItems(ctx context.Context, items []item.Item) (SaveStats, error) {
	var st SaveStats
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()

	for _, it := range items {
		key := item.DedupeKey(it)
		existing, err := scanItem(tx.QueryRowContext(ctx,
			`SELECT `+itemCols+` FROM items
			 WHERE id = ? OR (? != '' AND title_key = ?)
			 ORDER BY id = ? DESC LIMIT 1`,
			it.ID, key, key, it.ID))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx,
				`INSERT INTO items (id, type, title, title_key, url, source, summary, authors, tags, meta, published, fetched_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				it.ID, it.Type, it.Title, key, it.URL, it.Source, it.Summary,
				jsonOf(it.Authors), jsonOf(it.Tags), jsonMap(it.Meta), it.Published.Unix(), it.FetchedAt.Unix())
			if err != nil {
				return st, fmt.Errorf("insert %s: %w", it.ID, err)
			}
			st.Inserted++
		case err != nil:
			return st, fmt.Errorf("lookup %s: %w", it.ID, err)
		default:
			m := item.Merge(existing, it)
			_, err = tx.ExecContext(ctx,
				`UPDATE items SET type = ?, title = ?, title_key = ?, source = ?, summary = ?,
				 authors = ?, tags = ?, meta = ?, published = ? WHERE id = ?`,
				m.Type, m.Title, item.DedupeKey(m), m.Source, m.Summary,
				jsonOf(m.Authors), jsonOf(m.Tags), jsonMap(m.Meta), m.Published.Unix(), m.ID)
			if err != nil {
				return st, fmt.Errorf("merge into %s: %w", m.ID, err)
			}
			st.Merged++
		}
	}
	return st, tx.Commit()
}

// ItemsWithFeedback returns all items with the given rating, regardless of age.
func (s *Store) ItemsWithFeedback(ctx context.Context, fb item.Feedback) ([]item.Item, error) {
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items WHERE feedback = ?`, int(fb))
}

// UpdateScores writes Score and Signals for each item.
func (s *Store) UpdateScores(ctx context.Context, items []item.Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE items SET score = ?, sig_keyword = ?, sig_similarity = ?, sig_recency = ?, sig_learned = ?, learned_why = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, it := range items {
		if _, err := stmt.ExecContext(ctx, it.Score, it.Signals.Keyword, it.Signals.Similarity, it.Signals.Recency,
			it.Signals.Learned, jsonOf(it.Signals.LearnedWhy), it.ID); err != nil {
			return fmt.Errorf("update score %s: %w", it.ID, err)
		}
	}
	return tx.Commit()
}

// TopQuery selects the best-scoring items.
type TopQuery struct {
	Limit        int
	Since        time.Time   // fetched_at lower bound
	Types        []item.Type // empty = all
	IncludeRated bool        // include items already liked/disliked
	Offset       int
}

func (s *Store) Top(ctx context.Context, q TopQuery) ([]item.Item, error) {
	where := []string{"fetched_at >= ?"}
	args := []any{q.Since.Unix()}
	if len(q.Types) > 0 {
		where = append(where, "type IN (?"+strings.Repeat(", ?", len(q.Types)-1)+")")
		for _, t := range q.Types {
			args = append(args, string(t))
		}
	}
	if !q.IncludeRated {
		where = append(where, "feedback = 0")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	args = append(args, limit, max(q.Offset, 0))
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items WHERE `+strings.Join(where, " AND ")+
		` ORDER BY score DESC, published DESC, id LIMIT ? OFFSET ?`, args...)
}

var hexPrefix = regexp.MustCompile(`^[0-9a-f]{4,16}$`)

// Get returns the item whose ID is, or uniquely starts with, idPrefix
// (at least 4 hex chars).
func (s *Store) Get(ctx context.Context, idPrefix string) (item.Item, error) {
	idPrefix = strings.ToLower(strings.TrimSpace(idPrefix))
	if !hexPrefix.MatchString(idPrefix) {
		return item.Item{}, fmt.Errorf("invalid id %q: want 4-16 hex chars", idPrefix)
	}
	items, err := s.queryItems(ctx, `SELECT `+itemCols+` FROM items WHERE id LIKE ? LIMIT 2`, idPrefix+"%")
	if err != nil {
		return item.Item{}, err
	}
	switch len(items) {
	case 0:
		return item.Item{}, fmt.Errorf("%w: %s", ErrNotFound, idPrefix)
	case 1:
		return items[0], nil
	default:
		return item.Item{}, fmt.Errorf("%w: %s", ErrAmbiguous, idPrefix)
	}
}

// SetFeedback records a rating for the item matching idPrefix and returns it.
func (s *Store) SetFeedback(ctx context.Context, idPrefix string, fb item.Feedback) (item.Item, error) {
	it, err := s.Get(ctx, idPrefix)
	if err != nil {
		return it, err
	}
	var at any
	if fb != item.Neutral {
		at = time.Now().Unix()
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE items SET feedback = ?, feedback_at = ? WHERE id = ?`, int(fb), at, it.ID); err != nil {
		return it, err
	}
	it.Feedback = fb
	return it, nil
}
