package links

import (
	"net/url"
	"testing"
)

func TestSigner(t *testing.T) {
	s := NewSigner("https://radar.example.com/", []byte("key-one"))
	u, err := url.Parse(s.URL("abcd1234abcd1234", Up))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "radar.example.com" || u.Path != "/f/abcd1234abcd1234/up" {
		t.Errorf("url = %s", u)
	}
	tok := u.Query().Get("t")
	if !s.Valid("abcd1234abcd1234", Up, tok) {
		t.Error("own token rejected")
	}
	if s.Valid("abcd1234abcd1234", Down, tok) {
		t.Error("token accepted for a different vote")
	}
	if s.Valid("abcd1234abcd1235", Up, tok) {
		t.Error("token accepted for a different item")
	}
	if NewSigner("https://x", []byte("key-two")).Valid("abcd1234abcd1234", Up, tok) {
		t.Error("token accepted under a different key")
	}
	if s.Valid("abcd1234abcd1234", Up, "") {
		t.Error("empty token accepted")
	}
}
