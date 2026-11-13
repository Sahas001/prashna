package server

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Sahas001/prashna/internal/edition"
	"github.com/Sahas001/prashna/internal/item"
	"github.com/Sahas001/prashna/internal/store"
)

const pageSize = 20

func (s *Server) explainer(ctx context.Context) func(item.Item) []string {
	if s.Scorer == nil {
		return nil
	}
	sc, err := s.Scorer(ctx)
	if err != nil {
		s.log().Error("load profile", "err", err)
		return nil
	}
	return sc.MatchedTerms
}

func (s *Server) cards(ctx context.Context, items []item.Item, variant string) []card {
	explain := s.explainer(ctx)
	out := make([]card, len(items))
	for i, it := range items {
		out[i] = newCard(it, explain, variant)
	}
	return out
}

func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	s.log().Error(what, "err", err)
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}

type tab struct {
	Href, Label string
	Count       int
	Active      bool
}

// ---- Today ----

const onboardedKey = "onboarded"

// needsWelcome is true on a fresh install: nothing to read and nothing to
// go on yet.
func (s *Server) needsWelcome(ctx context.Context) bool {
	if done, _ := s.Store.Setting(ctx, onboardedKey); done != "" {
		return false
	}
	ins, _ := s.Store.Interests(ctx)
	feeds, _ := s.Store.Feeds(ctx)
	authors, _ := s.Store.BookFollows(ctx, store.FollowAuthor)
	subjects, _ := s.Store.BookFollows(ctx, store.FollowSubject)
	return len(ins)+len(feeds)+len(authors)+len(subjects) == 0
}

type sectionView struct {
	Key, Title string
	Cards      []card
}

type editionView struct {
	store.EditionSummary
	Date     time.Time
	Lead     *card
	Sections []sectionView
	Read     int
}

func (s *Server) editionView(ctx context.Context, sum store.EditionSummary, items []store.EditionItem) *editionView {
	g := edition.Group(sum, items)
	explain := s.explainer(ctx)
	v := &editionView{EditionSummary: sum, Date: g.Date}
	count := func(it item.Item) {
		if !it.ReadAt.IsZero() {
			v.Read++
		}
	}
	if g.Lead != nil {
		c := newCard(*g.Lead, explain, variantLead)
		v.Lead = &c
		count(*g.Lead)
	}
	for _, sec := range g.Sections {
		sv := sectionView{Key: sec.Key, Title: sec.Title}
		for _, it := range sec.Items {
			variant := variantStory
			switch sec.Key {
			case edition.SectionBookPick:
				variant = variantPick
			case edition.SectionBookNew:
				variant = variantBook
			}
			sv.Cards = append(sv.Cards, newCard(it, explain, variant))
			count(it)
		}
		v.Sections = append(v.Sections, sv)
	}
	return v
}

type todayData struct {
	Today     time.Time
	Edition   *editionView
	Revisit   *card
	Preparing bool // fetching; the page polls until the edition exists
	LastFetch time.Time
	FetchErr  string
}

func (s *Server) today(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.needsWelcome(ctx) {
		http.Redirect(w, r, "/welcome", http.StatusSeeOther)
		return
	}
	now := time.Now()
	d := todayData{Today: now}
	sum, items, err := s.Store.EditionForDay(ctx, edition.Day(now))
	running, last, lastErr := s.fetchStatus()
	if errors.Is(err, store.ErrNotFound) && !running && s.EnsureEdition != nil {
		if _, err := s.EnsureEdition(ctx); err != nil {
			s.log().Error("make edition", "err", err)
		}
		sum, items, err = s.Store.EditionForDay(ctx, edition.Day(now))
	}
	switch {
	case err == nil:
		d.Edition = s.editionView(ctx, sum, items)
	case errors.Is(err, store.ErrNotFound):
		d.Preparing, d.LastFetch, d.FetchErr = running, last, lastErr
	default:
		s.serverError(w, "today", err)
		return
	}
	if rev, err := s.Store.Revisit(ctx, now.AddDate(0, 0, -14), 30); err == nil && len(rev) > 0 {
		// One per day, the same all day.
		h := fnv.New32a()
		h.Write([]byte(edition.Day(now)))
		c := newCard(rev[int(h.Sum32())%len(rev)], nil, variantStory)
		d.Revisit = &c
	}
	s.renderPage(w, r, http.StatusOK, "today", "Today", "today", d)
}

// ---- Explore ----

type exploreData struct {
	Type  string
	Days  int
	Tabs  []tab
	Cards []card
	Next  int // offset of the next page; 0 = no more
}

var exploreTabs = []struct {
	typ   item.Type
	label string
}{{"", "Everything"}, {item.TypePaper, "Papers"}, {item.TypeArticle, "Essays"}, {item.TypeNews, "Discussions"}, {item.TypeBook, "Books"}}

const exploreDays = 14

func (s *Server) exploreData(r *http.Request) (exploreData, error) {
	typ := item.Type(r.URL.Query().Get("type"))
	if typ != "" && !typ.Valid() {
		typ = ""
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	q := store.TopQuery{Limit: pageSize + 1, Offset: max(offset, 0), Since: time.Now().AddDate(0, 0, -exploreDays)}
	if typ != "" {
		q.Types = []item.Type{typ}
	}
	if typ == item.TypeBook {
		q.Since = time.Time{} // books stay relevant long after they were found
	}
	items, err := s.Store.Top(r.Context(), q)
	if err != nil {
		return exploreData{}, err
	}
	d := exploreData{Type: string(typ), Days: exploreDays}
	if len(items) > pageSize {
		items = items[:pageSize]
		d.Next = q.Offset + pageSize
	}
	d.Cards = s.cards(r.Context(), items, "")
	for _, t := range exploreTabs {
		href := "/explore"
		if t.typ != "" {
			href += "?type=" + string(t.typ)
		}
		d.Tabs = append(d.Tabs, tab{Href: href, Label: t.label, Active: t.typ == typ})
	}
	return d, nil
}

func (s *Server) explore(w http.ResponseWriter, r *http.Request) {
	d, err := s.exploreData(r)
	if err != nil {
		s.serverError(w, "explore", err)
		return
	}
	s.renderPage(w, r, http.StatusOK, "explore", "Explore", "explore", d)
}

func (s *Server) exploreMore(w http.ResponseWriter, r *http.Request) {
	d, err := s.exploreData(r)
	if err != nil {
		s.serverError(w, "explore", err)
		return
	}
	s.renderPartial(w, http.StatusOK, "explore-page", d)
}

// ---- Library ----

type libraryData struct {
	Shelf store.Shelf
	Tabs  []tab
	Cards []card
	Notes []annotated // the Highlights shelf
	Next  string
	Empty string
}

// shelfHighlights is the library tab listing highlights and notes; the
// other shelves are lists of pieces.
const shelfHighlights store.Shelf = "highlights"

var shelves = []struct {
	shelf        store.Shelf
	label, empty string
}{
	{store.ShelfToRead, "To read", "Nothing saved. Use Save on anything you want to come back to."},
	{store.ShelfFavorites, "Favorites", "Mark pieces you love with More like this, and they collect here for rereading."},
	{store.ShelfRead, "Read", "Everything you open from Prashna is kept here, most recent first."},
}

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	shelf := store.Shelf(r.URL.Query().Get("shelf"))
	counts, err := s.Store.ShelfCounts(ctx)
	if err != nil {
		s.serverError(w, "library", err)
		return
	}
	d := libraryData{Shelf: store.ShelfToRead}
	for _, sh := range shelves {
		if sh.shelf == shelf {
			d.Shelf = shelf
		}
	}
	for _, sh := range shelves {
		d.Tabs = append(d.Tabs, tab{Href: "/library?shelf=" + string(sh.shelf), Label: sh.label, Count: counts[sh.shelf], Active: sh.shelf == d.Shelf})
		if sh.shelf == d.Shelf {
			d.Empty = sh.empty
		}
	}
	marks, err := s.Store.CountHighlights(ctx)
	if err != nil {
		s.serverError(w, "library", err)
		return
	}
	d.Tabs = append(d.Tabs, tab{Href: "/library?shelf=" + string(shelfHighlights), Label: "Highlights", Count: marks, Active: shelf == shelfHighlights})
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	offset = max(offset, 0)
	if shelf == shelfHighlights {
		d.Shelf = shelf
		d.Empty = "Select a passage while reading to highlight it. Your highlights and notes collect here."
		d.Tabs[0].Active = false
		notes, err := s.Store.AnnotatedItems(ctx, pageSize+1, offset)
		if err != nil {
			s.serverError(w, "library", err)
			return
		}
		if len(notes) > pageSize {
			notes = notes[:pageSize]
			d.Next = fmt.Sprintf("/library?shelf=%s&offset=%d", d.Shelf, offset+pageSize)
		}
		for _, n := range notes {
			d.Notes = append(d.Notes, annotated{Card: newCard(n.Item, nil, variantStory), Note: n.Note, Highlights: n.Highlights})
		}
		s.renderPage(w, r, http.StatusOK, "library", "Library", "library", d)
		return
	}
	items, err := s.Store.Library(ctx, d.Shelf, pageSize+1, offset)
	if err != nil {
		s.serverError(w, "library", err)
		return
	}
	if len(items) > pageSize {
		items = items[:pageSize]
		d.Next = fmt.Sprintf("/library?shelf=%s&offset=%d", d.Shelf, offset+pageSize)
	}
	d.Cards = s.cards(ctx, items, variantStory)
	s.renderPage(w, r, http.StatusOK, "library", "Library", "library", d)
}

// ---- Archive ----

type archiveData struct {
	Tab      string
	Tabs     []tab
	Editions []store.EditionSummary
	Cards    []card
	Older    string
}

func (s *Server) archive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := archiveData{Tab: r.URL.Query().Get("tab")}
	if d.Tab != "skipped" {
		d.Tab = ""
	}
	d.Tabs = []tab{{Href: "/archive", Label: "Editions", Active: d.Tab == ""}, {Href: "/archive?tab=skipped", Label: "Not for me", Active: d.Tab == "skipped"}}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	offset = max(offset, 0)
	var err error
	if d.Tab == "skipped" {
		var items []item.Item
		items, err = s.Store.RatedItems(ctx, item.Disliked, pageSize+1, offset)
		if len(items) > pageSize {
			items = items[:pageSize]
			d.Older = fmt.Sprintf("/archive?tab=skipped&offset=%d", offset+pageSize)
		}
		d.Cards = s.cards(ctx, items, variantStory)
	} else {
		d.Editions, err = s.Store.Editions(ctx, 61, offset)
		if len(d.Editions) > 60 {
			d.Editions = d.Editions[:60]
			d.Older = fmt.Sprintf("/archive?offset=%d", offset+60)
		}
	}
	if err != nil {
		s.serverError(w, "archive", err)
		return
	}
	s.renderPage(w, r, http.StatusOK, "archive", "Archive", "archive", d)
}

func (s *Server) archiveEdition(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sum, items, err := s.Store.Edition(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "edition", err)
		return
	}
	v := s.editionView(r.Context(), sum, items)
	s.renderPage(w, r, http.StatusOK, "edition", sum.Title, "archive", v)
}

// ---- Search and More ----

type searchData struct {
	Query string
	Cards []card
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d := searchData{Query: q}
	if q != "" {
		items, err := s.Store.Search(r.Context(), q, 50)
		if err != nil {
			s.serverError(w, "search", err)
			return
		}
		d.Cards = s.cards(r.Context(), items, "")
	}
	if r.Header.Get("HX-Request") != "" && r.Header.Get("HX-Boosted") == "" {
		s.renderPartial(w, http.StatusOK, "search-results", d)
		return
	}
	s.renderPage(w, r, http.StatusOK, "search", "Search", "search", d)
}

func (s *Server) morePage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, http.StatusOK, "more", "More", "more", nil)
}

// ---- Item actions ----

// itemUpdate re-renders a card in place, plus the Library badge out of band.
type itemUpdate struct {
	Card   card
	ToRead int
}

func (s *Server) respondItem(w http.ResponseWriter, r *http.Request, it item.Item, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such item.", http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, "item action", err)
		return
	}
	variant := r.FormValue("v")
	switch variant {
	case variantStory, variantLead, variantBook, variantPick, variantReader:
	default:
		variant = ""
	}
	counts, _ := s.Store.ShelfCounts(r.Context())
	s.renderPartial(w, http.StatusOK, "item-update", itemUpdate{
		Card:   newCard(it, s.explainer(r.Context()), variant),
		ToRead: counts[store.ShelfToRead],
	})
}

var votes = map[string]item.Feedback{"up": item.Liked, "down": item.Disliked, "clear": item.Neutral}

func (s *Server) rateItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fb, ok := votes[r.PathValue("vote")]
	if !ok || !fullID(id) {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.SetFeedback(r.Context(), id, fb); err != nil {
		s.respondItem(w, r, item.Item{}, err)
		return
	}
	if s.Rescore != nil {
		if err := s.Rescore(r.Context()); err != nil {
			s.log().Error("rescore after rating", "err", err)
		}
	}
	it, err := s.Store.Get(r.Context(), id)
	s.respondItem(w, r, it, err)
}

// openedItem records that a link was opened (sent with sendBeacon, so the
// link itself stays the real URL).
func (s *Server) openedItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !fullID(id) {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.RecordOpen(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "record open", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) itemAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !fullID(id) {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	var (
		it  item.Item
		err error
	)
	switch r.PathValue("action") {
	case "save":
		it, err = s.Store.SetSaved(r.Context(), id, true)
		if err == nil {
			s.keepText(it)
		}
	case "unsave":
		it, err = s.Store.SetSaved(r.Context(), id, false)
	case "read":
		it, err = s.Store.SetRead(r.Context(), id, true)
	case "unread":
		it, err = s.Store.SetRead(r.Context(), id, false)
	default:
		http.NotFound(w, r)
		return
	}
	s.respondItem(w, r, it, err)
}

func fullID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
