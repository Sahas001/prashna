package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

// ErrDuplicate is returned when adding something that already exists.
var ErrDuplicate = errors.New("already exists")

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ---- Interests ----

type Interest struct {
	ID     int64
	Term   string
	Weight float64
}

// Interests returns the profile, strongest first.
func (s *Store) Interests(ctx context.Context) ([]Interest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, term, weight FROM interests ORDER BY weight DESC, term`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interest
	for rows.Next() {
		var in Interest
		if err := rows.Scan(&in.ID, &in.Term, &in.Weight); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// SaveInterest adds term, or updates its weight if it exists (case-insensitively).
func (s *Store) SaveInterest(ctx context.Context, term string, weight float64) error {
	term = strings.TrimSpace(term)
	if term == "" {
		return errors.New("empty interest")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO interests (term, weight, created_at) VALUES (?, ?, ?)
		ON CONFLICT (term) DO UPDATE SET weight = excluded.weight`, term, weight, time.Now().Unix())
	return err
}

func (s *Store) SetInterestWeight(ctx context.Context, id int64, weight float64) error {
	return s.execOne(ctx, `UPDATE interests SET weight = ? WHERE id = ?`, weight, id)
}

func (s *Store) DeleteInterest(ctx context.Context, id int64) error {
	return s.execOne(ctx, `DELETE FROM interests WHERE id = ?`, id)
}

// execOne runs a statement that must affect exactly one row.
func (s *Store) execOne(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Feeds ----

type Feed struct {
	ID        int64
	URL       string
	Name      string
	Tags      []string
	Type      item.Type
	Enabled   bool
	CreatedAt time.Time
	State     FeedState // last fetch outcome; zero if never fetched
}

func (s *Store) Feeds(ctx context.Context) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.url, f.name, f.tags, f.type, f.enabled, f.created_at,
			COALESCE(st.title, ''), st.last_fetched_at, st.last_ok_at, COALESCE(st.last_error, ''), COALESCE(st.last_items, 0)
		FROM feeds f LEFT JOIN feed_state st ON st.url = f.url
		ORDER BY lower(CASE WHEN f.name != '' THEN f.name ELSE COALESCE(st.title, f.url) END)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feed
	for rows.Next() {
		var (
			f           Feed
			tags, typ   string
			created     int64
			fetched, ok sql.NullInt64
		)
		if err := rows.Scan(&f.ID, &f.URL, &f.Name, &tags, &typ, &f.Enabled, &created,
			&f.State.Title, &fetched, &ok, &f.State.LastError, &f.State.LastItems); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tags), &f.Tags); err != nil {
			return nil, fmt.Errorf("feed %d tags: %w", f.ID, err)
		}
		f.Type = item.Type(typ)
		f.CreatedAt = time.Unix(created, 0).UTC()
		f.State.URL = f.URL
		if fetched.Valid {
			f.State.LastFetchedAt = time.Unix(fetched.Int64, 0).UTC()
		}
		if ok.Valid {
			f.State.LastOKAt = time.Unix(ok.Int64, 0).UTC()
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddFeed stores a new feed (enabled). Returns ErrDuplicate for a known URL.
func (s *Store) AddFeed(ctx context.Context, f Feed) (int64, error) {
	if f.Type == "" {
		f.Type = item.TypeArticle
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO feeds (url, name, tags, type, enabled, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
		strings.TrimSpace(f.URL), strings.TrimSpace(f.Name), jsonOf(f.Tags), string(f.Type), time.Now().Unix())
	if isUnique(err) {
		return 0, fmt.Errorf("feed %s: %w", f.URL, ErrDuplicate)
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SetFeedEnabled(ctx context.Context, id int64, enabled bool) error {
	return s.execOne(ctx, `UPDATE feeds SET enabled = ? WHERE id = ?`, enabled, id)
}

func (s *Store) DeleteFeed(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var url string
	if err := tx.QueryRowContext(ctx, `DELETE FROM feeds WHERE id = ? RETURNING url`, id).Scan(&url); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feed_state WHERE url = ?`, url); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Book follows ----

const (
	FollowAuthor  = "author"
	FollowSubject = "subject"
)

type BookFollow struct {
	ID   int64
	Kind string
	Name string
}

func (s *Store) BookFollows(ctx context.Context, kind string) ([]BookFollow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, name FROM book_follows WHERE kind = ? ORDER BY name`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookFollow
	for rows.Next() {
		var b BookFollow
		if err := rows.Scan(&b.ID, &b.Kind, &b.Name); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BookFollowNames returns just the names of one kind.
func (s *Store) BookFollowNames(ctx context.Context, kind string) ([]string, error) {
	fs, err := s.BookFollows(ctx, kind)
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Name
	}
	return names, err
}

// AddBookFollow adds an author or subject; duplicates are ignored.
func (s *Store) AddBookFollow(ctx context.Context, kind, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("empty name")
	}
	if kind != FollowAuthor && kind != FollowSubject {
		return fmt.Errorf("unknown follow kind %q", kind)
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO book_follows (kind, name, created_at) VALUES (?, ?, ?)`,
		kind, name, time.Now().Unix())
	return err
}

func (s *Store) DeleteBookFollow(ctx context.Context, id int64) error {
	return s.execOne(ctx, `DELETE FROM book_follows WHERE id = ?`, id)
}

// ---- Seeding from config ----

const profileSeededKey = "profile_seeded"

// Profile is the editable profile, as imported from the config file.
type Profile struct {
	Interests []Interest
	Feeds     []Feed
	Authors   []string
	Subjects  []string
}

// SeedProfile imports p the first time it is called on a database and
// does nothing afterwards, so edits made in the web UI are never
// overwritten by the config file. It reports whether it imported.
func (s *Store) SeedProfile(ctx context.Context, p Profile) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var done string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, profileSeededKey).Scan(&done)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	now := time.Now().Unix()
	for _, in := range p.Interests {
		if _, err := tx.ExecContext(ctx, `INSERT INTO interests (term, weight, created_at) VALUES (?, ?, ?)
			ON CONFLICT (term) DO UPDATE SET weight = excluded.weight`, strings.TrimSpace(in.Term), in.Weight, now); err != nil {
			return false, fmt.Errorf("seed interest %q: %w", in.Term, err)
		}
	}
	for _, f := range p.Feeds {
		typ := f.Type
		if typ == "" {
			typ = item.TypeArticle
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO feeds (url, name, tags, type, enabled, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
			f.URL, f.Name, jsonOf(f.Tags), string(typ), now); err != nil {
			return false, fmt.Errorf("seed feed %s: %w", f.URL, err)
		}
	}
	for kind, names := range map[string][]string{FollowAuthor: p.Authors, FollowSubject: p.Subjects} {
		for _, n := range names {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO book_follows (kind, name, created_at) VALUES (?, ?, ?)`,
				kind, strings.TrimSpace(n), now); err != nil {
				return false, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)`, profileSeededKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ---- Reading list ----

// SetSaved adds the item to, or removes it from, the reading list.
func (s *Store) SetSaved(ctx context.Context, id string, saved bool) (item.Item, error) {
	var at any
	if saved {
		at = time.Now().Unix()
	}
	return s.updateItem(ctx, id, `UPDATE items SET saved_at = ?, read_at = CASE WHEN ? IS NULL THEN NULL ELSE read_at END WHERE id = ?`, at, at)
}

// SetRead marks a reading-list item read or unread.
func (s *Store) SetRead(ctx context.Context, id string, read bool) (item.Item, error) {
	var at any
	if read {
		at = time.Now().Unix()
	}
	return s.updateItem(ctx, id, `UPDATE items SET read_at = ? WHERE id = ?`, at)
}

// updateItem runs query with args followed by the item ID, then returns the fresh item.
func (s *Store) updateItem(ctx context.Context, id, query string, args ...any) (item.Item, error) {
	it, err := s.Get(ctx, id)
	if err != nil {
		return it, err
	}
	if _, err := s.db.ExecContext(ctx, query, append(args, it.ID)...); err != nil {
		return it, err
	}
	return s.Get(ctx, it.ID)
}

// SavedItems returns the reading list: unread first, newest saves first.
func (s *Store) SavedItems(ctx context.Context) ([]item.Item, error) {
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items WHERE saved_at IS NOT NULL
		ORDER BY read_at IS NOT NULL, saved_at DESC`)
}

func (s *Store) CountUnreadSaved(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE saved_at IS NOT NULL AND read_at IS NULL`).Scan(&n)
	return n, err
}

// prefixed qualifies a column list with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
