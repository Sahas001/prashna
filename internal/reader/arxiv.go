package reader

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"

	"bytes"
)

// arXiv publishes most papers as web pages too (made from their LaTeX by
// LaTeXML), which read far better on a screen than a PDF. This file turns
// one into reader text: the paper's own structure, with the site's
// chrome removed and LaTeXML's markup reduced to what the reader styles.

var arxivAbsPath = regexp.MustCompile(`^/abs/((?:[a-z-]+(?:\.[A-Z]{2})?/\d{7}|\d{4}\.\d{4,5})(?:v\d+)?)$`)

// ArxivID returns the paper ID in an arXiv abstract address
// ("https://arxiv.org/abs/1706.03762" gives "1706.03762"), or "".
func ArxivID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."); host != "arxiv.org" {
		return ""
	}
	if m := arxivAbsPath.FindStringSubmatch(u.Path); m != nil {
		return m[1]
	}
	return ""
}

// ArxivPDF is the address of a paper's PDF.
func ArxivPDF(id string) string { return "https://arxiv.org/pdf/" + id }

var defaultArxivHTML = []string{"https://arxiv.org/html/", "https://ar5iv.labs.arxiv.org/html/"}

func (e *Extractor) extractArxiv(ctx context.Context, id string) (Article, error) {
	sources := e.ArxivHTML
	if len(sources) == 0 {
		sources = defaultArxivHTML
	}
	for _, src := range sources {
		body, ct, final, err := e.get(ctx, src+id)
		if se, ok := errors.AsType[*statusError](err); ok && se.code == http.StatusNotFound {
			continue // no web version here
		}
		if err != nil {
			return Article{}, err
		}
		if strings.HasPrefix(final.Path, "/abs/") {
			continue // ar5iv sends papers it doesn't have back to arXiv
		}
		art, err := FromLaTeXML(body, ct, final)
		if err == nil {
			return art, nil
		}
	}
	return Article{}, ErrNoHTML
}

// FromLaTeXML extracts a paper from a LaTeXML page (arXiv's web
// versions). base is the page's address.
func FromLaTeXML(page []byte, contentType string, base *url.URL) (Article, error) {
	r, err := charset.NewReader(bytes.NewReader(page), contentType)
	if err != nil {
		return Article{}, err
	}
	doc, err := html.Parse(r)
	if err != nil {
		return Article{}, err
	}
	paper := find(doc, func(n *html.Node) bool { return n.DataAtom == atom.Article && hasClass(n, "ltx_document") })
	if paper == nil {
		return Article{}, ErrNoText
	}
	art := Article{Site: "arXiv"}
	if h := find(paper, func(n *html.Node) bool { return hasClass(n, "ltx_title_document") }); h != nil {
		art.Title = strings.Join(strings.Fields(text(h)), " ")
	}
	p := &paperPass{}
	p.walk(paper)
	p.appendNotes(paper)
	art.HTML, art.Words = Sanitize(paper, base)
	if art.Words < minWords {
		return Article{}, ErrNoText
	}
	return art, nil
}

// paperPass rewrites a LaTeXML document in place.
type paperPass struct {
	notes []*html.Node // footnote contents, in order
}

// gone lists parts of the page that the reader shows itself (title,
// authors) or that are furniture.
var gone = []string{"ltx_title_document", "ltx_authors", "ltx_dates", "ltx_pagination", "ltx_page_footer",
	"ltx_page_header", "ltx_TOC", "ltx_ERROR", "ltx_rule", "ltx_note_type", "ltx_tag_note"}

func (p *paperPass) walk(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode {
			switch {
			case hasAnyClass(c, gone) || c.DataAtom == atom.Nav || c.DataAtom == atom.Footer || c.DataAtom == atom.Header:
				n.RemoveChild(c)
			case hasClass(c, "ltx_note"):
				p.footnote(n, c)
			case c.DataAtom == atom.Table && (hasClass(c, "ltx_eqn_table") || hasClass(c, "ltx_equationgroup")):
				p.equations(n, c)
			default:
				switch {
				case hasClass(c, "ltx_abstract"):
					setClass(c, "p-abstract")
				case hasClass(c, "ltx_biblist"):
					setClass(c, "p-refs")
				}
				p.walk(c)
			}
		}
		c = next
	}
}

// footnote replaces an inline note (LaTeXML puts the whole text of a
// footnote inside the sentence) with its number, and keeps the text for
// the list of notes at the end.
func (p *paperPass) footnote(parent, note *html.Node) {
	content := find(note, func(n *html.Node) bool { return hasClass(n, "ltx_note_content") })
	if content == nil || strings.TrimSpace(text(content)) == "" {
		parent.RemoveChild(note)
		return
	}
	// Its own copy of the mark, and LaTeXML's labels.
	for {
		junk := find(content, func(n *html.Node) bool {
			return hasClass(n, "ltx_note_mark") || hasClass(n, "ltx_note_type") || hasClass(n, "ltx_tag_note")
		})
		if junk == nil {
			break
		}
		junk.Parent.RemoveChild(junk)
	}
	p.walk(content) // notes can hold maths and (rarely) notes
	if strings.TrimSpace(text(content)) == "" {
		parent.RemoveChild(note)
		return
	}
	li := element(atom.Li)
	for c := content.FirstChild; c != nil; c = content.FirstChild {
		content.RemoveChild(c)
		li.AppendChild(c)
	}
	p.notes = append(p.notes, li)
	sup := element(atom.Sup)
	sup.AppendChild(&html.Node{Type: html.TextNode, Data: strconv.Itoa(len(p.notes))})
	parent.InsertBefore(sup, note)
	parent.RemoveChild(note)
}

func (p *paperPass) appendNotes(paper *html.Node) {
	if len(p.notes) == 0 {
		return
	}
	box := element(atom.Div)
	setClass(box, "p-notes")
	h := element(atom.H2)
	h.AppendChild(&html.Node{Type: html.TextNode, Data: "Notes"})
	ol := element(atom.Ol)
	for _, li := range p.notes {
		ol.AppendChild(li)
	}
	box.AppendChild(h)
	box.AppendChild(ol)
	// Before the references, if the paper has them.
	if refs := find(paper, func(n *html.Node) bool { return hasClass(n, "ltx_bibliography") }); refs != nil {
		refs.Parent.InsertBefore(box, refs)
		return
	}
	paper.AppendChild(box)
}

// equations turns LaTeXML's layout table for displayed equations (cells
// for padding, the formula, and its number) into one block per line:
// the formula, then its number.
func (p *paperPass) equations(parent, table *html.Node) {
	var rows []*html.Node
	var collect func(n *html.Node)
	collect = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.DataAtom == atom.Tr {
				rows = append(rows, c)
			} else if c.Type == html.ElementNode {
				collect(c)
			}
		}
	}
	collect(table)
	for _, tr := range rows {
		line := element(atom.Div)
		setClass(line, "p-eq")
		var number string
		for td := tr.FirstChild; td != nil; td = td.NextSibling {
			if td.Type != html.ElementNode {
				continue
			}
			if hasClass(td, "ltx_eqn_eqno") {
				number = strings.Join(strings.Fields(text(td)), " ")
				continue
			}
			for c := td.FirstChild; c != nil; c = td.FirstChild {
				td.RemoveChild(c)
				if c.Type == html.TextNode && strings.TrimSpace(c.Data) == "" {
					continue
				}
				line.AppendChild(c)
			}
		}
		if line.FirstChild == nil {
			continue
		}
		if number != "" {
			no := element(atom.Small)
			setClass(no, "p-eqno")
			no.AppendChild(&html.Node{Type: html.TextNode, Data: number})
			line.AppendChild(no)
		}
		parent.InsertBefore(line, table)
	}
	parent.RemoveChild(table)
}

// ---- small DOM helpers ----

func element(a atom.Atom) *html.Node {
	return &html.Node{Type: html.ElementNode, DataAtom: a, Data: a.String()}
}

func hasClass(n *html.Node, class string) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func hasAnyClass(n *html.Node, classes []string) bool {
	for _, c := range classes {
		if hasClass(n, c) {
			return true
		}
	}
	return false
}

func setClass(n *html.Node, class string) {
	for i, a := range n.Attr {
		if a.Namespace == "" && a.Key == "class" {
			n.Attr[i].Val = class
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: "class", Val: class})
}

// find returns the first node under n (n included) that matches.
func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := find(c, match); got != nil {
			return got
		}
	}
	return nil
}

func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
