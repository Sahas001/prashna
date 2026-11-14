package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Sahas001/prashna/internal/item"
)

// ---- Edition candidates ----

// CandidateQuery selects candidates for one edition section.
type CandidateQuery struct {
	Type           item.Type
	Since          time.Time // fetched_at lower bound; zero = any
	PublishedSince time.Time // published lower bound; zero = any
	MetaKeys       []string  // only items whose meta has all of these keys
	MinScore       float64
	Limit          int
}

// Candidates returns the best unrated items of one type that have not
// appeared in any edition yet.
func (s *Store) Candidates(ctx context.Context, q CandidateQuery) ([]item.Item, error) {
	if q.Limit <= 0 {
		return nil, nil
	}
	// Links the reader added themselves are already theirs: not news.
	where := "digested_at IS NULL AND feedback = 0 AND type = ? AND score >= ? AND source != '" + item.SourceSaved + "'"
	args := []any{string(q.Type), q.MinScore}
	if !q.Since.IsZero() {
		where += " AND fetched_at >= ?"
		args = append(args, q.Since.Unix())
	}
	if !q.PublishedSince.IsZero() {
		where += " AND published >= ?"
		args = append(args, q.PublishedSince.Unix())
	}
	for _, k := range q.MetaKeys {
		where += " AND json_extract(meta, '$.' || ?) IS NOT NULL"
		args = append(args, k)
	}
	args = append(args, q.Limit)
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items WHERE `+where+
		` ORDER BY score DESC, published DESC LIMIT ?`, args...)
}

// ---- Editions ----

// EditionEntry places one item in an edition section.
type EditionEntry struct {
	Section string
	ItemID  string
}

// EditionItem is an item as it appears in a stored edition.
type EditionItem struct {
	Section string
	Item    item.Item
}

// EditionSummary describes a stored edition.
type EditionSummary struct {
	ID       int64
	Number   int    // 1 for the first edition ever made
	Day      string // YYYY-MM-DD, local
	Title    string
	MadeAt   time.Time
	Items    int
	Liked    int
	Disliked int
	Opened   int
}

// SaveEdition stores the edition for day and marks its items as used so
// they never appear in another edition. A second edition for the same
// day returns ErrDuplicate.
func (s *Store) SaveEdition(ctx context.Context, day, title string, madeAt time.Time, entries []EditionEntry) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO digests (sent_at, subject, day) VALUES (?, ?, ?)`, madeAt.Unix(), title, day)
	if isUnique(err) {
		return 0, fmt.Errorf("edition %s: %w", day, ErrDuplicate)
	}
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for pos, e := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO digest_items (digest_id, item_id, position, section) VALUES (?, ?, ?, ?)`,
			id, e.ItemID, pos, e.Section); err != nil {
			return 0, fmt.Errorf("edition item %s: %w", e.ItemID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET digested_at = ? WHERE id = ?`, madeAt.Unix(), e.ItemID); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

const editionSummaryCols = `d.id, (SELECT count(*) FROM digests e WHERE e.id <= d.id), coalesce(d.day, ''), d.subject, d.sent_at,
	count(i.id), coalesce(sum(i.feedback = 1), 0), coalesce(sum(i.feedback = -1), 0), coalesce(sum(i.open_count > 0), 0)`

const editionSummaryFrom = ` FROM digests d LEFT JOIN digest_items di ON di.digest_id = d.id LEFT JOIN items i ON i.id = di.item_id`

func scanEditionSummary(row scanner) (EditionSummary, error) {
	var e EditionSummary
	var made int64
	err := row.Scan(&e.ID, &e.Number, &e.Day, &e.Title, &made, &e.Items, &e.Liked, &e.Disliked, &e.Opened)
	e.MadeAt = time.Unix(made, 0).UTC()
	return e, err
}

// Editions lists stored editions, newest first.
func (s *Store) Editions(ctx context.Context, limit, offset int) ([]EditionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+editionSummaryCols+editionSummaryFrom+
		` GROUP BY d.id ORDER BY d.sent_at DESC, d.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EditionSummary
	for rows.Next() {
		e, err := scanEditionSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EditionForDay returns the edition made on day, or ErrNotFound.
func (s *Store) EditionForDay(ctx context.Context, day string) (EditionSummary, []EditionItem, error) {
	return s.edition(ctx, `d.day = ?`, day)
}

// Edition returns the edition with id, or ErrNotFound.
func (s *Store) Edition(ctx context.Context, id int64) (EditionSummary, []EditionItem, error) {
	return s.edition(ctx, `d.id = ?`, id)
}

func (s *Store) edition(ctx context.Context, where string, arg any) (EditionSummary, []EditionItem, error) {
	e, err := scanEditionSummary(s.db.QueryRowContext(ctx, `SELECT `+editionSummaryCols+editionSummaryFrom+
		` WHERE `+where+` GROUP BY d.id`, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return e, nil, ErrNotFound
	}
	if err != nil {
		return e, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT di.section, `+prefixed("i", itemCols)+
		` FROM digest_items di JOIN items i ON i.id = di.item_id WHERE di.digest_id = ? ORDER BY di.position`, e.ID)
	if err != nil {
		return e, nil, err
	}
	defer rows.Close()
	var out []EditionItem
	for rows.Next() {
		var section string
		it, err := scanItem(sectionScanner{rows, &section})
		if err != nil {
			return e, nil, err
		}
		out = append(out, EditionItem{Section: section, Item: it})
	}
	return e, out, rows.Err()
}

// sectionScanner prepends the section column to scanItem's destinations.
type sectionScanner struct {
	rows    *sql.Rows
	section *string
}

func (s sectionScanner) Scan(dest ...any) error {
	return s.rows.Scan(append([]any{s.section}, dest...)...)
}

// ---- Reading: opens, library, search ----

// RecordOpen notes that the item was opened from Prashna: it counts the
// open, stamps the time, and marks the item read the first time.
func (s *Store) RecordOpen(ctx context.Context, id string) (item.Item, error) {
	now := time.Now().Unix()
	return s.updateItem(ctx, id, `UPDATE items SET opened_at = ?, open_count = open_count + 1, read_at = coalesce(read_at, ?) WHERE id = ?`, now, now)
}

// Shelf selects a library view.
type Shelf string

const (
	ShelfToRead    Shelf = "to-read"   // saved, not read yet
	ShelfFavorites Shelf = "favorites" // liked: worth reading again
	ShelfRead      Shelf = "read"      // opened or marked read, latest first
)

// Library returns one shelf of the reader's library.
func (s *Store) Library(ctx context.Context, shelf Shelf, limit, offset int) ([]item.Item, error) {
	var where, order string
	switch shelf {
	case ShelfToRead:
		where, order = "saved_at IS NOT NULL AND read_at IS NULL", "saved_at DESC"
	case ShelfFavorites:
		where, order = "feedback = 1", "coalesce(opened_at, feedback_at) DESC"
	case ShelfRead:
		where, order = "read_at IS NOT NULL", "coalesce(opened_at, read_at) DESC"
	default:
		return nil, fmt.Errorf("unknown shelf %q", shelf)
	}
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items WHERE `+where+` ORDER BY `+order+`, id LIMIT ? OFFSET ?`, limit, offset)
}

// ShelfCounts returns how many items each shelf holds.
func (s *Store) ShelfCounts(ctx context.Context) (map[Shelf]int, error) {
	var toRead, fav, read int
	err := s.db.QueryRowContext(ctx, `SELECT
		coalesce(sum(saved_at IS NOT NULL AND read_at IS NULL), 0),
		coalesce(sum(feedback = 1), 0),
		coalesce(sum(read_at IS NOT NULL), 0) FROM items`).Scan(&toRead, &fav, &read)
	return map[Shelf]int{ShelfToRead: toRead, ShelfFavorites: fav, ShelfRead: read}, err
}

// RatedItems returns liked or disliked items, most recently rated first.
func (s *Store) RatedItems(ctx context.Context, fb item.Feedback, limit, offset int) ([]item.Item, error) {
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items WHERE feedback = ?
		ORDER BY feedback_at DESC, id LIMIT ? OFFSET ?`, int(fb), limit, offset)
}

// Revisit returns favorites and saved items not opened since before, for
// the "from your library" slot in an edition. Older visits come first.
func (s *Store) Revisit(ctx context.Context, before time.Time, limit int) ([]item.Item, error) {
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items
		WHERE (feedback = 1 OR saved_at IS NOT NULL) AND coalesce(opened_at, saved_at, feedback_at, 0) < ?
		ORDER BY coalesce(opened_at, saved_at, feedback_at, 0), id LIMIT ?`, before.Unix(), limit)
}

// Search finds items whose title, summary, authors or tags contain every
// word of q (case-insensitive), best scores and recent opens first.
func (s *Store) Search(ctx context.Context, q string, limit int) ([]item.Item, error) {
	words := SearchWords(q)
	if len(words) == 0 {
		return nil, nil
	}
	esc := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	var where []string
	var args []any
	for _, w := range words[:min(len(words), 8)] {
		pattern := "%" + esc.Replace(w) + "%"
		// Each word must appear somewhere in the piece or in what the
		// reader wrote about it (highlights and notes).
		where = append(where, `(lower(title) LIKE ? ESCAPE '\' OR lower(summary) LIKE ? ESCAPE '\'
			OR lower(authors) LIKE ? ESCAPE '\' OR lower(tags) LIKE ? ESCAPE '\'
			OR id IN (SELECT item_id FROM highlights WHERE lower(quote) LIKE ? ESCAPE '\' OR lower(note) LIKE ? ESCAPE '\')
			OR id IN (SELECT item_id FROM item_notes WHERE lower(body) LIKE ? ESCAPE '\'))`)
		args = append(args, pattern, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	// Title matches on the first word rank first.
	args = append(args, "%"+esc.Replace(words[0])+"%", limit)
	return s.queryItems(ctx, `SELECT `+itemCols+` FROM items WHERE `+strings.Join(where, " AND ")+
		` ORDER BY (lower(title) LIKE ? ESCAPE '\') DESC, open_count DESC, score DESC LIMIT ?`, args...)
}

// SearchWords splits a query into lowercase words, without the
// punctuation around them: "roots," finds "roots".
func SearchWords(q string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(q)) {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// HasSummary reports whether the stored item with id has a description.
func (s *Store) HasSummary(ctx context.Context, id string) bool {
	var n int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE id = ? AND summary != ''`, id).Scan(&n)
	return n > 0
}

// FetchedAny reports whether anything has ever been fetched.
func (s *Store) FetchedAny(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM items LIMIT 1)`).Scan(&n)
	return n > 0, err
}

// SecretKey returns a random key stored under name, creating it on first
// use (e.g. for signing session cookies).
func (s *Store) SecretKey(ctx context.Context, name string) ([]byte, error) {
	key := "secret:" + name
	v, err := s.Setting(ctx, key)
	if err != nil {
		return nil, err
	}
	if v == "" {
		b := make([]byte, 32)
		rand.Read(b)
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`, key, hex.EncodeToString(b)); err != nil {
			return nil, err
		}
		if v, err = s.Setting(ctx, key); err != nil {
			return nil, err
		}
	}
	return hex.DecodeString(v)
}
