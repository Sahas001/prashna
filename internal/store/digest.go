package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"radar/internal/item"
)

// DigestQuery selects candidates for one digest section.
type DigestQuery struct {
	Type           item.Type
	Since          time.Time // fetched_at lower bound; zero = any
	PublishedSince time.Time // published lower bound; zero = any
	MetaKeys       []string  // only items whose meta has all of these keys
	MinScore       float64
	Limit          int
}

// DigestCandidates returns the best unrated items of one type that have not
// appeared in any previous digest.
func (s *Store) DigestCandidates(ctx context.Context, q DigestQuery) ([]item.Item, error) {
	if q.Limit <= 0 {
		return nil, nil
	}
	where := "digested_at IS NULL AND feedback = 0 AND type = ? AND score >= ?"
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

// RecordDigest stores a sent digest and marks its items as digested so
// they are not sent again. ids must be in display order.
func (s *Store) RecordDigest(ctx context.Context, subject string, ids []string, sentAt time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO digests (sent_at, subject) VALUES (?, ?)`, sentAt.Unix(), subject)
	if err != nil {
		return 0, err
	}
	digestID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for pos, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO digest_items (digest_id, item_id, position) VALUES (?, ?, ?)`, digestID, id, pos); err != nil {
			return 0, fmt.Errorf("record digest item %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET digested_at = ? WHERE id = ?`, sentAt.Unix(), id); err != nil {
			return 0, err
		}
	}
	return digestID, tx.Commit()
}

const feedbackKeySetting = "feedback_signing_key"

// FeedbackKey returns the HMAC key for signing feedback links, generating
// and persisting a random one on first use. Deleting the setting
// invalidates every link already sent.
func (s *Store) FeedbackKey(ctx context.Context) ([]byte, error) {
	var hexKey string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, feedbackKeySetting).Scan(&hexKey)
	if errors.Is(err, sql.ErrNoRows) {
		key := make([]byte, 32)
		rand.Read(key)
		// INSERT OR IGNORE + re-read keeps this correct if two processes race.
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`,
			feedbackKeySetting, hex.EncodeToString(key)); err != nil {
			return nil, err
		}
		err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, feedbackKeySetting).Scan(&hexKey)
	}
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(hexKey)
}
