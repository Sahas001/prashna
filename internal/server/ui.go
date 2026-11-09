package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"radar/internal/item"
	"radar/internal/learn"
	"radar/internal/links"
	"radar/internal/store"
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

func (s *Server) cards(ctx context.Context, items []item.Item) []card {
	explain := s.explainer(ctx)
	out := make([]card, len(items))
	for i, it := range items {
		out[i] = newCard(it, explain)
	}
	return out
}

func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	s.log().Error(what, "err", err)
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}

// ---- Today ----

type tab struct {
	Href, Label string
	Active      bool
}

type todayData struct {
	Type  string
	Days  int
	Tabs  []tab
	Cards []card
	Next  int // offset of the next page; 0 = no more
}

var todayTabs = []struct {
	typ   item.Type
	label string
}{{"", "All"}, {item.TypePaper, "Papers"}, {item.TypeArticle, "Articles"}, {item.TypeNews, "Discussions"}, {item.TypeBook, "Books"}}

func (s *Server) todayData(r *http.Request) (todayData, error) {
	days := s.TopDays
	if days <= 0 {
		days = 7
	}
	typ := item.Type(r.URL.Query().Get("type"))
	if typ != "" && !typ.Valid() {
		typ = ""
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	q := store.TopQuery{Limit: pageSize + 1, Offset: max(offset, 0), Since: time.Now().AddDate(0, 0, -days)}
	if typ != "" {
		q.Types = []item.Type{typ}
	}
	if typ == item.TypeBook {
		q.Since = time.Time{} // books stay relevant long after they were fetched
	}
	items, err := s.Store.Top(r.Context(), q)
	if err != nil {
		return todayData{}, err
	}
	d := todayData{Type: string(typ), Days: days}
	if len(items) > pageSize {
		items = items[:pageSize]
		d.Next = q.Offset + pageSize
	}
	d.Cards = s.cards(r.Context(), items)
	for _, t := range todayTabs {
		href := "/"
		if t.typ != "" {
			href += "?type=" + string(t.typ)
		}
		d.Tabs = append(d.Tabs, tab{Href: href, Label: t.label, Active: t.typ == typ})
	}
	return d, nil
}

func (s *Server) today(w http.ResponseWriter, r *http.Request) {
	d, err := s.todayData(r)
	if err != nil {
		s.serverError(w, "today", err)
		return
	}
	s.renderPage(w, r, http.StatusOK, "today", "Today", "today", d)
}

func (s *Server) more(w http.ResponseWriter, r *http.Request) {
	d, err := s.todayData(r)
	if err != nil {
		s.serverError(w, "more", err)
		return
	}
	s.renderPartial(w, http.StatusOK, "more-page", d)
}

// ---- Reading list, history ----

func (s *Server) saved(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.SavedItems(r.Context())
	if err != nil {
		s.serverError(w, "saved", err)
		return
	}
	s.renderPage(w, r, http.StatusOK, "saved", "Reading list", "saved", s.cards(r.Context(), items))
}

type historyData struct {
	Tab     string
	Tabs    []tab
	Digests []store.DigestSummary
	Cards   []card
	Older   string // link to the next page of rated items
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	d := historyData{Tab: r.URL.Query().Get("tab")}
	for _, t := range []struct{ v, l string }{{"", "Digests"}, {"liked", "Liked"}, {"disliked", "Disliked"}} {
		href := "/history"
		if t.v != "" {
			href += "?tab=" + t.v
		}
		d.Tabs = append(d.Tabs, tab{Href: href, Label: t.l, Active: d.Tab == t.v})
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	offset = max(offset, 0)
	var err error
	switch d.Tab {
	case "liked", "disliked":
		fb := item.Liked
		if d.Tab == "disliked" {
			fb = item.Disliked
		}
		var items []item.Item
		items, err = s.Store.RatedItems(r.Context(), fb, pageSize+1, offset)
		if len(items) > pageSize {
			items = items[:pageSize]
			d.Older = fmt.Sprintf("/history?tab=%s&offset=%d", d.Tab, offset+pageSize)
		}
		d.Cards = s.cards(r.Context(), items)
	default:
		d.Tab = ""
		d.Digests, err = s.Store.Digests(r.Context(), 100, 0)
	}
	if err != nil {
		s.serverError(w, "history", err)
		return
	}
	s.renderPage(w, r, http.StatusOK, "history", "History", "history", d)
}

type digestPageData struct {
	Digest store.DigestSummary
	Cards  []card
}

func (s *Server) digestPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, items, err := s.Store.Digest(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "digest", err)
		return
	}
	s.renderPage(w, r, http.StatusOK, "digest", d.Subject, "history", digestPageData{Digest: d, Cards: s.cards(r.Context(), items)})
}

// ---- Item actions (htmx) ----

// itemUpdate is the response to an item action: the re-rendered card plus
// an out-of-band update of the reading-list badge in the nav.
type itemUpdate struct {
	Card   card
	Unread int
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
	unread, _ := s.Store.CountUnreadSaved(r.Context())
	s.renderPartial(w, http.StatusOK, "item-update", itemUpdate{Card: newCard(it, s.explainer(r.Context())), Unread: unread})
}

func (s *Server) rateItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, fb, ok := links.ParseVote(r.PathValue("vote"))
	if !ok || !fullID.MatchString(id) {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.SetFeedback(r.Context(), id, fb); err != nil {
		s.respondItem(w, r, item.Item{}, err)
		return
	}
	if s.Rescore != nil {
		if err := s.Rescore(r.Context()); err != nil {
			s.log().Error("rescore after feedback", "err", err)
		}
	}
	it, err := s.Store.Get(r.Context(), id)
	s.respondItem(w, r, it, err)
}

func (s *Server) itemAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !fullID.MatchString(id) {
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

// ---- Interests and book follows ----

type interestsData struct {
	Interests []store.Interest
	Error     string
	Term      string // form value kept after an error
}

type followsData struct {
	Kind, Title, Hint, Placeholder string
	Items                          []store.BookFollow
	Error                          string
}

type interestsPage struct {
	Interests interestsData
	Authors   followsData
	Subjects  followsData
	Model     *modelView
}

// modelView is the learned-ranking panel.
type modelView struct {
	learn.Report
	Progress int // percent of the way to MinRatings, while collecting
}

const maxWeight = 3

// minRatingsHint scales the "ratings so far" bar; the real threshold is
// in the report's status text.
const minRatingsHint = 20

func parseWeight(s string) (float64, error) {
	w, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(w) || math.IsInf(w, 0) {
		return 0, errors.New("weight must be a number")
	}
	return math.Max(-maxWeight, math.Min(maxWeight, w)), nil
}

func (s *Server) loadInterests(ctx context.Context) (interestsData, error) {
	ins, err := s.Store.Interests(ctx)
	return interestsData{Interests: ins}, err
}

func (s *Server) loadFollows(ctx context.Context, kind string) (followsData, error) {
	d := followsData{Kind: kind}
	switch kind {
	case store.FollowAuthor:
		d.Title, d.Placeholder = "Followed authors", "Ursula K. Le Guin"
		d.Hint = "New books by these authors appear in the weekly books section."
	case store.FollowSubject:
		d.Title, d.Placeholder = "Book subjects", "science fiction"
		d.Hint = "Well-loved books on these subjects compete for book of the week."
	default:
		return d, fmt.Errorf("unknown kind %q", kind)
	}
	var err error
	d.Items, err = s.Store.BookFollows(ctx, kind)
	return d, err
}

func (s *Server) interestsPage(w http.ResponseWriter, r *http.Request) {
	var d interestsPage
	var err error
	if d.Interests, err = s.loadInterests(r.Context()); err == nil {
		if d.Authors, err = s.loadFollows(r.Context(), store.FollowAuthor); err == nil {
			d.Subjects, err = s.loadFollows(r.Context(), store.FollowSubject)
		}
	}
	if err != nil {
		s.serverError(w, "interests", err)
		return
	}
	var rep learn.Report
	if ok, err := s.Store.JSONSetting(r.Context(), learn.ReportKey, &rep); err != nil {
		s.log().Error("load model report", "err", err)
	} else if ok {
		d.Model = &modelView{Report: rep}
		if rep.ModelAUC == 0 && rep.Ratings < minRatingsHint {
			d.Model.Progress = rep.Ratings * 100 / minRatingsHint
		}
	}
	s.renderPage(w, r, http.StatusOK, "interests", "Interests", "interests", d)
}

// interestsChanged rescores and re-renders the interests list. formErr
// (a user error) is shown in the list instead of failing the request.
func (s *Server) interestsChanged(w http.ResponseWriter, r *http.Request, formErr error, term string) {
	if formErr == nil && s.Rescore != nil {
		if err := s.Rescore(r.Context()); err != nil {
			s.log().Error("rescore after interest change", "err", err)
		}
	}
	d, err := s.loadInterests(r.Context())
	if err != nil {
		s.serverError(w, "interests", err)
		return
	}
	if formErr != nil {
		d.Error, d.Term = formErr.Error(), term
	}
	s.renderPartial(w, http.StatusOK, "interests", d)
}

func (s *Server) addInterest(w http.ResponseWriter, r *http.Request) {
	term := strings.TrimSpace(r.FormValue("term"))
	weight, err := parseWeight(r.FormValue("weight"))
	switch {
	case term == "":
		err = errors.New("enter a word, phrase, tag or author")
	case len(term) > 100:
		err = errors.New("keep it under 100 characters")
	case err == nil:
		err = s.Store.SaveInterest(r.Context(), term, weight)
	}
	s.interestsChanged(w, r, err, term)
}

func (s *Server) setInterestWeight(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	weight, err := parseWeight(r.FormValue("weight"))
	if err == nil {
		err = s.Store.SetInterestWeight(r.Context(), id, weight)
	}
	s.interestsChanged(w, r, err, "")
}

func (s *Server) deleteInterest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	err = s.Store.DeleteInterest(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		err = nil // already gone: just re-render
	}
	s.interestsChanged(w, r, err, "")
}

func (s *Server) followsChanged(w http.ResponseWriter, r *http.Request, kind string, formErr error) {
	d, err := s.loadFollows(r.Context(), kind)
	if err != nil {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if formErr != nil {
		d.Error = formErr.Error()
	}
	s.renderPartial(w, http.StatusOK, "follows", d)
}

func (s *Server) addFollow(w http.ResponseWriter, r *http.Request) {
	kind, name := r.FormValue("kind"), strings.TrimSpace(r.FormValue("name"))
	var err error
	if name == "" || len(name) > 100 {
		err = errors.New("enter a name (up to 100 characters)")
	} else {
		err = s.Store.AddBookFollow(r.Context(), kind, name)
	}
	s.followsChanged(w, r, kind, err)
}

func (s *Server) deleteFollow(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.DeleteBookFollow(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "delete follow", err)
		return
	}
	s.followsChanged(w, r, r.FormValue("kind"), nil)
}

// ---- Feeds ----

type feedView struct {
	store.Feed
	Title       string // configured name, else the feed's own title, else the URL
	Status      string
	StatusClass string // ok, err, never
}

type feedForm struct {
	URL, Name, Tags, Type string
}

type feedsData struct {
	Feeds    []feedView
	Form     feedForm
	Error    string
	Message  string
	Fetching bool
	CanFetch bool
}

func (s *Server) loadFeeds(ctx context.Context) (feedsData, error) {
	feeds, err := s.Store.Feeds(ctx)
	d := feedsData{Fetching: s.fetching.Load(), CanFetch: s.FetchFeeds != nil, Form: feedForm{Type: string(item.TypeArticle)}}
	for _, f := range feeds {
		v := feedView{Feed: f, Title: f.Name}
		if v.Title == "" {
			v.Title = f.State.Title
		}
		if v.Title == "" {
			v.Title = f.URL
		}
		switch {
		case f.State.LastFetchedAt.IsZero():
			v.Status, v.StatusClass = "Not fetched yet", "never"
		case f.State.LastError != "":
			v.Status, v.StatusClass = "Failed "+ago(f.State.LastFetchedAt)+": "+shortError(f.State.LastError), "err"
		default:
			v.Status, v.StatusClass = fmt.Sprintf("OK %s · %d recent %s", ago(f.State.LastOKAt), f.State.LastItems, map[bool]string{true: "post", false: "posts"}[f.State.LastItems == 1]), "ok"
		}
		d.Feeds = append(d.Feeds, v)
	}
	return d, err
}

// shortError trims the URL-laden prefix Go puts on HTTP errors.
func shortError(e string) string {
	if i := strings.LastIndex(e, ": "); i >= 0 && i+2 < len(e) {
		e = e[i+2:]
	}
	return e
}

func (s *Server) feedsPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.loadFeeds(r.Context())
	if err != nil {
		s.serverError(w, "feeds", err)
		return
	}
	if r.URL.Query().Get("partial") == "1" { // polling while a fetch runs
		s.renderPartial(w, http.StatusOK, "feeds", d)
		return
	}
	s.renderPage(w, r, http.StatusOK, "feeds", "Feeds", "feeds", d)
}

func (s *Server) feedsChanged(w http.ResponseWriter, r *http.Request, mutate func(*feedsData)) {
	d, err := s.loadFeeds(r.Context())
	if err != nil {
		s.serverError(w, "feeds", err)
		return
	}
	if mutate != nil {
		mutate(&d)
	}
	s.renderPartial(w, http.StatusOK, "feeds", d)
}

const probeTimeout = 20 * time.Second

func (s *Server) addFeed(w http.ResponseWriter, r *http.Request) {
	form := feedForm{
		URL:  strings.TrimSpace(r.FormValue("url")),
		Name: strings.TrimSpace(r.FormValue("name")),
		Tags: strings.TrimSpace(r.FormValue("tags")),
		Type: r.FormValue("type"),
	}
	fail := func(msg string) {
		s.feedsChanged(w, r, func(d *feedsData) { d.Error, d.Form = msg, form })
	}
	u, err := url.Parse(form.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fail("Enter the feed's full URL, starting with https://")
		return
	}
	typ := item.Type(form.Type)
	if typ != item.TypeArticle && typ != item.TypeNews && typ != item.TypePaper {
		typ = item.TypeArticle
	}
	var tags []string
	for t := range strings.SplitSeq(form.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}

	title := ""
	if s.ProbeFeed != nil {
		ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
		defer cancel()
		var feedURL string
		if title, feedURL, err = s.ProbeFeed(ctx, form.URL); err != nil {
			fail("Couldn't read a feed at that URL: " + shortError(err.Error()) + ".")
			return
		}
		form.URL = feedURL
	}
	if _, err := s.Store.AddFeed(r.Context(), store.Feed{URL: form.URL, Name: form.Name, Tags: tags, Type: typ}); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			fail("You already follow that feed.")
			return
		}
		s.serverError(w, "add feed", err)
		return
	}
	if title != "" {
		st, _ := s.Store.FeedState(r.Context(), form.URL)
		st.Title = title
		s.Store.SaveFeedState(r.Context(), st)
	}
	s.feedsChanged(w, r, func(d *feedsData) { d.Message = "Added. New posts arrive with the next fetch." })
}

func (s *Server) toggleFeed(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.SetFeedEnabled(r.Context(), id, r.FormValue("enabled") == "true"); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "toggle feed", err)
		return
	}
	s.feedsChanged(w, r, nil)
}

func (s *Server) deleteFeed(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.DeleteFeed(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "delete feed", err)
		return
	}
	s.feedsChanged(w, r, nil)
}

const backgroundFetchTimeout = 5 * time.Minute

// fetchFeedsNow starts an RSS fetch in the background; the page polls
// itself while it runs.
func (s *Server) fetchFeedsNow(w http.ResponseWriter, r *http.Request) {
	if s.FetchFeeds == nil {
		http.Error(w, "Not available.", http.StatusNotImplemented)
		return
	}
	if s.fetching.CompareAndSwap(false, true) {
		go func() {
			defer s.fetching.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), backgroundFetchTimeout)
			defer cancel()
			if err := s.FetchFeeds(ctx); err != nil {
				s.log().Error("fetch feeds from UI", "err", err)
			}
		}()
	}
	// Always start polling, even if a quick fetch already finished: the
	// next poll shows the final state.
	s.feedsChanged(w, r, func(d *feedsData) { d.Fetching = true })
}
