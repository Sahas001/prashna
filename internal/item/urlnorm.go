package item

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// trackingParams are query parameters that never change what a URL points
// to. utm_* is handled separately by prefix.
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "msclkid": true,
	"mc_cid": true, "mc_eid": true, "igshid": true,
	"ref": true, "ref_src": true, "ref_url": true,
}

// arxivPath matches abs and pdf URLs for both new-style (2401.12345) and
// old-style (hep-th/9901001, math.GT/0309136) identifiers, with optional
// version and .pdf suffix.
var arxivPath = regexp.MustCompile(`^/(?:abs|pdf)/((?:[a-z\-]+(?:\.[A-Z]{2})?/\d{7})|(?:\d{4}\.\d{4,5}))(?:v\d+)?(?:\.pdf)?/?$`)

// CanonicalURL normalizes a URL so that the same resource fetched from
// different sources produces the same string (and therefore the same ID):
// https scheme, lowercase host without "www.", no fragment, no tracking
// params, sorted query, no trailing slash, and arXiv abs/pdf/version
// variants collapsed to https://arxiv.org/abs/<id>.
func CanonicalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("unsupported url %q", raw)
	}

	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "arxiv.org" || host == "export.arxiv.org" {
		if m := arxivPath.FindStringSubmatch(u.Path); m != nil {
			return "https://arxiv.org/abs/" + m[1], nil
		}
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		host = net.JoinHostPort(host, port)
	}

	u.Scheme = "https"
	u.Host = host
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")

	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || trackingParams[lk] {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode() // Encode sorts by key
	u.ForceQuery = false
	return u.String(), nil
}
