package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

// Article is the stored reader text of an item.
type Article struct {
	ItemID    string
	HTML      string // sanitized; safe to render as is
	Byline    string
	Site      string
	Words     int
	FetchedAt time.Time
}

// Article returns the stored text for an item, or ErrNotFound.
func (s *Store) Article(ctx context.Context, itemID string) (Article, error) {
	a := Article{ItemID: itemID}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT html, byline, site, words, fetched_at FROM articles WHERE item_id = ?`, itemID).
		Scan(&a.HTML, &a.Byline, &a.Site, &a.Words, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.FetchedAt = time.Unix(at, 0).UTC()
	return a, err
}

// HasArticle reports whether the item's text is stored.
func (s *Store) HasArticle(ctx context.Context, itemID string) bool {
	var n int
	return s.db.QueryRowContext(ctx, `SELECT 1 FROM articles WHERE item_id = ?`, itemID).Scan(&n) == nil
}

// SaveArticle stores (or replaces) an item's reader text.
func (s *Store) SaveArticle(ctx context.Context, a Article) error {
	if a.FetchedAt.IsZero() {
		a.FetchedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO articles (item_id, html, byline, site, words, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (item_id) DO UPDATE SET html = excluded.html, byline = excluded.byline,
			site = excluded.site, words = excluded.words, fetched_at = excluded.fetched_at`,
		a.ItemID, a.HTML, a.Byline, a.Site, a.Words, a.FetchedAt.Unix())
	return err
}

// Highlight is a passage the reader marked, with an optional note.
// Start and End are character offsets (UTF-16 units, as browsers count)
// into the text of the stored article.
type Highlight struct {
	ID         int64
	ItemID     string
	Start, End int
	Quote      string
	Note       string
	CreatedAt  time.Time
}

const highlightCols = `id, item_id, start_pos, end_pos, quote, note, created_at`

func scanHighlight(row scanner, extra ...any) (Highlight, error) {
	var (
		h  Highlight
		at int64
	)
	err := row.Scan(append([]any{&h.ID, &h.ItemID, &h.Start, &h.End, &h.Quote, &h.Note, &at}, extra...)...)
	h.CreatedAt = time.Unix(at, 0).UTC()
	return h, err
}

// AddHighlight stores a highlight on an existing item. Marking the same
// span twice returns the first highlight.
func (s *Store) AddHighlight(ctx context.Context, h Highlight) (Highlight, error) {
	if _, err := s.Get(ctx, h.ItemID); err != nil {
		return h, err
	}
	prev, err := scanHighlight(s.db.QueryRowContext(ctx,
		`SELECT `+highlightCols+` FROM highlights WHERE item_id = ? AND start_pos = ? AND end_pos = ?`, h.ItemID, h.Start, h.End))
	if err == nil {
		return prev, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return h, err
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO highlights (item_id, start_pos, end_pos, quote, note, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		h.ItemID, h.Start, h.End, h.Quote, h.Note, h.CreatedAt.Unix())
	if err != nil {
		return h, err
	}
	h.ID, err = res.LastInsertId()
	return h, err
}

// Highlights returns an item's highlights in reading order.
func (s *Store) Highlights(ctx context.Context, itemID string) ([]Highlight, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+highlightCols+` FROM highlights WHERE item_id = ? ORDER BY start_pos, id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Highlight
	for rows.Next() {
		h, err := scanHighlight(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetHighlightNote replaces a highlight's note and returns the highlight.
func (s *Store) SetHighlightNote(ctx context.Context, id int64, note string) (Highlight, error) {
	h, err := scanHighlight(s.db.QueryRowContext(ctx,
		`UPDATE highlights SET note = ? WHERE id = ? RETURNING `+highlightCols, strings.TrimSpace(note), id))
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// DeleteHighlight removes a highlight and returns the item it was on.
func (s *Store) DeleteHighlight(ctx context.Context, id int64) (itemID string, err error) {
	err = s.db.QueryRowContext(ctx, `DELETE FROM highlights WHERE id = ? RETURNING item_id`, id).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return itemID, err
}

// ItemHighlights is one piece with the passages marked in it.
type ItemHighlights struct {
	Item       item.Item
	Note       string
	Highlights []Highlight
}

// AnnotatedItems returns the pieces that have highlights or a note, the
// most recently annotated first.
func (s *Store) AnnotatedItems(ctx context.Context, limit, offset int) ([]ItemHighlights, error) {
	items, err := s.queryItems(ctx, `SELECT `+prefixed("i", itemCols)+` FROM items i
		JOIN (SELECT item_id, max(at) AS at FROM (
			SELECT item_id, created_at AS at FROM highlights
			UNION ALL SELECT item_id, updated_at FROM item_notes) GROUP BY item_id) a ON a.item_id = i.id
		ORDER BY a.at DESC, i.id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]ItemHighlights, len(items))
	for i, it := range items {
		out[i].Item = it
		if out[i].Highlights, err = s.Highlights(ctx, it.ID); err != nil {
			return nil, err
		}
		if out[i].Note, err = s.ItemNote(ctx, it.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CountHighlights is the number of highlights across the library.
func (s *Store) CountHighlights(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM highlights`).Scan(&n)
	return n, err
}

// ItemNote returns the reader's note on an item ("" if none).
func (s *Store) ItemNote(ctx context.Context, itemID string) (string, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM item_notes WHERE item_id = ?`, itemID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, err
}

// SetItemNote stores the note on an item; an empty note removes it.
func (s *Store) SetItemNote(ctx context.Context, itemID, body string) error {
	if _, err := s.Get(ctx, itemID); err != nil {
		return err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM item_notes WHERE item_id = ?`, itemID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO item_notes (item_id, body, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (item_id) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		itemID, body, time.Now().Unix())
	return err
}

// SetTitle replaces an item's title and summary (an empty summary keeps
// the old one).
func (s *Store) SetTitle(ctx context.Context, itemID, title, summary string) error {
	title, summary = item.CleanText(title), item.CleanText(summary)
	if title == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE items SET title = ?, title_key = '',
		summary = CASE WHEN ? != '' THEN ? ELSE summary END WHERE id = ?`, title, summary, summary, itemID)
	return err
}
