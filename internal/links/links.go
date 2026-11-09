// Package links builds and verifies the signed 👍/👎 URLs embedded in
// digest emails. A link carries an HMAC over (item ID, vote), so only
// links Radar generated can record feedback, without any login.
package links

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"

	"radar/internal/item"
)

// Vote is the action a feedback link performs.
type Vote string

const (
	Up    Vote = "up"
	Down  Vote = "down"
	Clear Vote = "clear"
)

// ParseVote maps a URL path segment to a Vote and its Feedback value.
func ParseVote(s string) (Vote, item.Feedback, bool) {
	switch Vote(s) {
	case Up:
		return Up, item.Liked, true
	case Down:
		return Down, item.Disliked, true
	case Clear:
		return Clear, item.Neutral, true
	}
	return "", 0, false
}

type Signer struct {
	base string
	key  []byte
}

// NewSigner returns a Signer producing links under baseURL (the public URL
// of `radar serve`).
func NewSigner(baseURL string, key []byte) *Signer {
	return &Signer{base: strings.TrimRight(baseURL, "/"), key: key}
}

// Token returns the signature for (id, vote): 128 bits of HMAC-SHA256,
// base64url without padding.
func (s *Signer) Token(id string, v Vote) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("radar-feedback\x00" + id + "\x00" + string(v)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// Valid reports whether token is the signature for (id, vote), in constant time.
func (s *Signer) Valid(id string, v Vote, token string) bool {
	return hmac.Equal([]byte(token), []byte(s.Token(id, v)))
}

// Path returns the server-relative feedback path for (id, vote), including the token.
func (s *Signer) Path(id string, v Vote) string {
	return "/f/" + url.PathEscape(id) + "/" + string(v) + "?t=" + s.Token(id, v)
}

// URL returns the absolute feedback URL for (id, vote).
func (s *Signer) URL(id string, v Vote) string {
	return s.base + s.Path(id, v)
}
