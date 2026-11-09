package server

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Sahas001/radar/internal/config"
	"github.com/Sahas001/radar/internal/item"
	"github.com/Sahas001/radar/internal/learn"
	"github.com/Sahas001/radar/internal/store"
)

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

type modelView struct {
	learn.Report
	Progress int // percent of the way to the minimum number of ratings
}

type interestsPage struct {
	Interests interestsData
	Authors   followsData
	Subjects  followsData
	Model     *modelView
}

const (
	maxWeight      = 3
	minRatingsHint = 20
)

func parseWeight(s string) (float64, error) {
	w, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(w) || math.IsInf(w, 0) {
		return 0, errors.New("weight must be a number")
	}
	return math.Max(-maxWeight, math.Min(maxWeight, w)), nil
}

func (s *Server) loadFollows(ctx context.Context, kind string) (followsData, error) {
	d := followsData{Kind: kind}
	switch kind {
	case store.FollowAuthor:
		d.Title, d.Placeholder = "Authors you follow", "Ursula K. Le Guin"
		d.Hint = "Their new books appear in Sunday’s edition. Dates come from Wikidata and Open Library."
	case store.FollowSubject:
		d.Title, d.Placeholder = "Book subjects", "science fiction"
		d.Hint = "The most-read books on these subjects compete for book of the week."
	default:
		return d, fmt.Errorf("unknown kind %q", kind)
	}
	var err error
	d.Items, err = s.Store.BookFollows(ctx, kind)
	return d, err
}

func (s *Server) interestsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var d interestsPage
	ins, err := s.Store.Interests(ctx)
	d.Interests.Interests = ins
	if err == nil {
		if d.Authors, err = s.loadFollows(ctx, store.FollowAuthor); err == nil {
			d.Subjects, err = s.loadFollows(ctx, store.FollowSubject)
		}
	}
	if err != nil {
		s.serverError(w, "interests", err)
		return
	}
	var rep learn.Report
	if ok, err := s.Store.JSONSetting(ctx, learn.ReportKey, &rep); err != nil {
		s.log().Error("load model report", "err", err)
	} else if ok {
		d.Model = &modelView{Report: rep}
		if rep.ModelAUC == 0 && rep.Ratings < minRatingsHint {
			d.Model.Progress = rep.Ratings * 100 / minRatingsHint
		}
	}
	s.renderPage(w, r, http.StatusOK, "interests", "Interests", "interests", d)
}

// interestsChanged rescores and re-renders the list; formErr (a user
// mistake) is shown in the list instead of failing the request.
func (s *Server) interestsChanged(w http.ResponseWriter, r *http.Request, formErr error, term string) {
	if formErr == nil && s.Rescore != nil {
		if err := s.Rescore(r.Context()); err != nil {
			s.log().Error("rescore after interest change", "err", err)
		}
	}
	ins, err := s.Store.Interests(r.Context())
	if err != nil {
		s.serverError(w, "interests", err)
		return
	}
	d := interestsData{Interests: ins}
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
		err = errors.New("Enter a word, phrase, tag or author.")
	case len(term) > 100:
		err = errors.New("Keep it under 100 characters.")
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
		err = nil
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
		err = errors.New("Enter a name (up to 100 characters).")
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
	Feeds   []feedView
	Form    feedForm
	Error   string
	Message string
}

func (s *Server) loadFeeds(ctx context.Context) (feedsData, error) {
	feeds, err := s.Store.Feeds(ctx)
	d := feedsData{Form: feedForm{Type: string(item.TypeArticle)}}
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
			v.Status, v.StatusClass = "Not checked yet", "never"
		case f.State.LastError != "":
			v.Status, v.StatusClass = "Couldn’t read it "+ago(f.State.LastFetchedAt)+": "+shortError(f.State.LastError), "err"
		default:
			v.Status, v.StatusClass = fmt.Sprintf("Checked %s · %d recent %s", ago(f.State.LastOKAt), f.State.LastItems, plural(f.State.LastItems, "post", "posts")), "ok"
		}
		d.Feeds = append(d.Feeds, v)
	}
	return d, err
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
	fail := func(msg string) { s.feedsChanged(w, r, func(d *feedsData) { d.Error, d.Form = msg, form }) }
	if form.URL != "" && !strings.Contains(form.URL, "://") {
		form.URL = "https://" + form.URL // people paste "example.com/blog"
	}
	u, err := url.Parse(form.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fail("Paste the address of a blog or its feed, like example.com/blog.")
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
			fail("Couldn’t find a feed there: " + shortError(err.Error()) + ".")
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
	name := form.Name
	if name == "" {
		name = title
	}
	s.feedsChanged(w, r, func(d *feedsData) { d.Message = "Added " + name + ". Its posts arrive with the next check." })
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

// ---- OPML ----

type opml struct {
	XMLName xml.Name    `xml:"opml"`
	Version string      `xml:"version,attr"`
	Title   string      `xml:"head>title"`
	Body    []opmlEntry `xml:"body>outline"`
}

type opmlEntry struct {
	Text     string      `xml:"text,attr"`
	Title    string      `xml:"title,attr,omitempty"`
	Type     string      `xml:"type,attr,omitempty"`
	XMLURL   string      `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string      `xml:"htmlUrl,attr,omitempty"`
	Children []opmlEntry `xml:"outline"`
}

// parseOPML returns the feeds in an OPML file, flattening folders and
// using a folder's name as a tag for the feeds inside it.
func parseOPML(r io.Reader) ([]store.Feed, error) {
	var doc opml
	dec := xml.NewDecoder(io.LimitReader(r, 5<<20))
	dec.Strict = false
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("not an OPML file: %w", err)
	}
	var feeds []store.Feed
	var walk func([]opmlEntry, string)
	walk = func(entries []opmlEntry, folder string) {
		for _, e := range entries {
			if e.XMLURL != "" {
				f := store.Feed{URL: strings.TrimSpace(e.XMLURL), Name: strings.TrimSpace(cmpOr(e.Title, e.Text))}
				if folder != "" {
					f.Tags = []string{folder}
				}
				feeds = append(feeds, f)
			}
			if len(e.Children) > 0 {
				walk(e.Children, strings.TrimSpace(cmpOr(e.Title, e.Text)))
			}
		}
	}
	walk(doc.Body, "")
	return feeds, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// importFeeds adds feeds, skipping ones already followed or not http(s).
func (s *Server) importFeeds(ctx context.Context, feeds []store.Feed) (added int, err error) {
	for _, f := range feeds {
		u, perr := url.Parse(f.URL)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		if _, err := s.Store.AddFeed(ctx, f); err == nil {
			added++
		} else if !errors.Is(err, store.ErrDuplicate) {
			return added, err
		}
	}
	return added, nil
}

func (s *Server) importOPML(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("opml")
	if err != nil {
		s.feedsChanged(w, r, func(d *feedsData) { d.Error = "Choose an OPML file exported from your feed reader." })
		return
	}
	defer file.Close()
	feeds, err := parseOPML(file)
	if err != nil {
		s.feedsChanged(w, r, func(d *feedsData) { d.Error = "That file isn’t OPML. Feed readers export it under Export or Backup." })
		return
	}
	added, err := s.importFeeds(r.Context(), feeds)
	if err != nil {
		s.serverError(w, "import opml", err)
		return
	}
	s.feedsChanged(w, r, func(d *feedsData) {
		d.Message = fmt.Sprintf("Imported %d new %s from %d in the file.", added, plural(added, "feed", "feeds"), len(feeds))
	})
}

func (s *Server) exportOPML(w http.ResponseWriter, r *http.Request) {
	feeds, err := s.Store.Feeds(r.Context())
	if err != nil {
		s.serverError(w, "export opml", err)
		return
	}
	doc := opml{Version: "2.0", Title: "Radar feeds"}
	for _, f := range feeds {
		name := cmpOr(f.Name, cmpOr(f.State.Title, f.URL))
		doc.Body = append(doc.Body, opmlEntry{Text: name, Title: name, Type: "rss", XMLURL: f.URL})
	}
	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="radar-feeds.opml"`)
	io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(doc)
}

// ---- Settings, fetching ----

type statusView struct {
	Running bool
	Last    time.Time
	Error   string
}

type settingsData struct {
	config.Settings
	Categories string // comma-separated, as typed
	Saved      bool
	Error      string
	Status     statusView
	About      About
	Weekdays   []weekdayOption
}

type weekdayOption struct{ Value, Label string }

var weekdayOptions = func() []weekdayOption {
	var out []weekdayOption
	for d := time.Sunday; d <= time.Saturday; d++ {
		out = append(out, weekdayOption{Value: strings.ToLower(d.String()), Label: "On " + d.String() + "s"})
	}
	return out
}()

func (s *Server) statusView() statusView {
	running, last, lastErr := s.fetchStatus()
	return statusView{Running: running, Last: last, Error: shortError(lastErr)}
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	var st config.Settings
	if s.Settings != nil {
		st = s.Settings(r.Context())
	}
	s.renderPage(w, r, http.StatusOK, "settings", "Settings", "settings", s.settingsData(st))
}

func (s *Server) settingsData(st config.Settings) settingsData {
	return settingsData{
		Settings: st, Categories: strings.Join(st.ArxivCategories, ", "), Status: s.statusView(), About: s.About,
		Weekdays: weekdayOptions,
	}
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if s.SaveSettings == nil {
		http.Error(w, "Not available.", http.StatusNotImplemented)
		return
	}
	atoi := func(k string) int { n, _ := strconv.Atoi(r.FormValue(k)); return n }
	theme := "system"
	if s.Settings != nil {
		theme = s.Settings(r.Context()).Theme // set separately, on change
	}
	st := config.Settings{
		Theme:           theme,
		ArxivEnabled:    r.FormValue("arxiv_enabled") == "on",
		ArxivCategories: strings.Split(r.FormValue("arxiv_categories"), ","),
		HNEnabled:       r.FormValue("hn_enabled") == "on",
		HNMinPoints:     atoi("hn_min_points"),
		BooksEnabled:    r.FormValue("books_enabled") == "on",
		Papers:          atoi("papers"),
		Articles:        atoi("articles"),
		Discussions:     atoi("discussions"),
		BooksWeekday:    r.FormValue("books_weekday"),
		FetchEveryHours: atoi("fetch_every_hours"),
		LearningEnabled: r.FormValue("learning_enabled") == "on",
	}
	if st.BooksWeekday == "every" {
		st.BooksWeekday = ""
	}
	verr := st.Normalize()
	d := s.settingsData(st)
	d.Categories = r.FormValue("arxiv_categories")
	if verr != nil {
		d.Error = verr.Error()
		s.renderPartial(w, http.StatusOK, "settings-form", d)
		return
	}
	if err := s.SaveSettings(r.Context(), st); err != nil {
		s.serverError(w, "save settings", err)
		return
	}
	if s.Rescore != nil {
		s.Rescore(r.Context()) // learning on/off changes scores
	}
	d.Categories, d.Saved = strings.Join(st.ArxivCategories, ", "), true
	s.renderPartial(w, http.StatusOK, "settings-form", d)
}

// saveTheme stores the Appearance choice. The page has already switched
// (app.js applies it instantly); this makes it stick.
func (s *Server) saveTheme(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil || s.SaveSettings == nil {
		http.Error(w, "Not available.", http.StatusNotImplemented)
		return
	}
	st := s.Settings(r.Context())
	st.Theme = r.FormValue("theme")
	if !slices.Contains(config.Themes, st.Theme) {
		http.Error(w, "Unknown theme.", http.StatusBadRequest)
		return
	}
	if err := s.SaveSettings(r.Context(), st); err != nil {
		s.serverError(w, "save theme", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fetchNow starts a background fetch and returns the status line, which
// polls itself until the fetch is done.
func (s *Server) fetchNow(w http.ResponseWriter, r *http.Request) {
	if s.StartFetch != nil {
		s.StartFetch()
	}
	v := s.statusView()
	v.Running = true // even if a quick fetch already finished: the next poll shows the result
	s.renderPartial(w, http.StatusOK, "fetch-status", v)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, http.StatusOK, "fetch-status", s.statusView())
}
