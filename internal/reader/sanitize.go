package reader

import (
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// kept elements are written out (without their attributes, except the few
// handled in writeOpen). Everything else is either dropped with its
// contents or replaced by its contents.
var kept = map[atom.Atom]bool{
	atom.P: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Blockquote: true, atom.Ul: true, atom.Ol: true, atom.Li: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
	atom.Pre: true, atom.Code: true, atom.Kbd: true, atom.Samp: true,
	atom.Em: true, atom.Strong: true, atom.B: true, atom.I: true, atom.U: true, atom.S: true, atom.Del: true, atom.Ins: true,
	atom.Small: true, atom.Cite: true, atom.Q: true, atom.Abbr: true, atom.Sup: true, atom.Sub: true,
	atom.A: true, atom.Img: true, atom.Figure: true, atom.Figcaption: true, atom.Br: true, atom.Hr: true,
	atom.Table: true, atom.Thead: true, atom.Tbody: true, atom.Tfoot: true, atom.Tr: true, atom.Th: true, atom.Td: true, atom.Caption: true,
	atom.Div: true,
}

// dropped elements disappear together with everything inside them.
var dropped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Iframe: true, atom.Frame: true, atom.Frameset: true,
	atom.Form: true, atom.Input: true, atom.Button: true, atom.Select: true, atom.Textarea: true, atom.Option: true,
	atom.Svg: true, atom.Math: true, atom.Object: true, atom.Embed: true, atom.Applet: true, atom.Template: true,
	atom.Video: true, atom.Audio: true, atom.Canvas: true, atom.Link: true, atom.Meta: true, atom.Head: true,
	atom.Title: true, atom.Dialog: true, atom.Base: true, atom.Source: true, atom.Track: true, atom.Map: true, atom.Area: true,
}

var void = map[atom.Atom]bool{atom.Img: true, atom.Br: true, atom.Hr: true}

// Sanitize renders n as safe HTML and counts its words. The output has
// no scripts, styles, event handlers, forms or embeds, and only http(s)
// and mailto links, so it can be put on a page as is.
func Sanitize(n *html.Node, base *url.URL) (string, int) {
	s := &sanitizer{base: base}
	s.node(n)
	return strings.TrimSpace(s.b.String()), s.words
}

type sanitizer struct {
	b     strings.Builder
	base  *url.URL
	words int
}

func (s *sanitizer) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s.node(c)
	}
}

func (s *sanitizer) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		s.words += len(strings.Fields(n.Data))
		s.b.WriteString(html.EscapeString(n.Data))
	case html.DocumentNode:
		s.children(n)
	case html.ElementNode:
		s.element(n)
	}
}

func (s *sanitizer) element(n *html.Node) {
	a := n.DataAtom
	if a == atom.H1 {
		a = atom.H2 // the page has its own h1: the piece's title
	}
	// Foreign content (svg, math) and unknown tags have no atom or a
	// namespace: never written out.
	if n.Namespace != "" || dropped[a] {
		return
	}
	if !kept[a] {
		s.children(n)
		return
	}
	name := a.String()
	switch a {
	case atom.A:
		href := s.link(attr(n, "href"), "http", "https", "mailto")
		if href == "" {
			s.children(n)
			return
		}
		s.b.WriteString(`<a href="` + html.EscapeString(href) + `" target="_blank" rel="noopener noreferrer">`)
	case atom.Img:
		src := s.link(attr(n, "src"), "http", "https")
		if src == "" {
			return
		}
		// The app's content policy only loads images over https.
		src = "https://" + strings.TrimPrefix(strings.TrimPrefix(src, "http://"), "https://")
		s.b.WriteString(`<img src="` + html.EscapeString(src) + `" alt="` + html.EscapeString(attr(n, "alt")) +
			`" loading="lazy" referrerpolicy="no-referrer">`)
		return
	case atom.Td, atom.Th:
		s.b.WriteString("<" + name)
		for _, k := range []string{"colspan", "rowspan"} {
			if v, err := strconv.Atoi(attr(n, k)); err == nil && v > 1 && v < 100 {
				s.b.WriteString(" " + k + `="` + strconv.Itoa(v) + `"`)
			}
		}
		s.b.WriteString(">")
	case atom.Ol:
		s.b.WriteString("<ol")
		if v, err := strconv.Atoi(attr(n, "start")); err == nil && v != 1 {
			s.b.WriteString(` start="` + strconv.Itoa(v) + `"`)
		}
		s.b.WriteString(">")
	default:
		s.b.WriteString("<" + name + ">")
	}
	if void[a] {
		return
	}
	s.children(n)
	s.b.WriteString("</" + name + ">")
}

// link resolves raw against the page and returns it if its scheme is
// allowed, "" otherwise.
func (s *sanitizer) link(raw string, schemes ...string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if s.base != nil {
		u = s.base.ResolveReference(u)
	}
	for _, sc := range schemes {
		if strings.EqualFold(u.Scheme, sc) {
			return u.String()
		}
	}
	return ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val
		}
	}
	return ""
}
