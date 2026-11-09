package server

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Sahas001/radar/internal/item"
	"github.com/Sahas001/radar/internal/store"
)

// topic is a starting point offered on the welcome screen: a few weighted
// interests, sources and book subjects that work well together. Every
// feed here was checked to be live and parseable.
type topic struct {
	Key, Name, Blurb string
	Tech             bool // turns on Hacker News
	Interests        []store.Interest
	Arxiv            []string
	Feeds            []store.Feed
	Subjects         []string
}

func feed(url, name string, tags ...string) store.Feed {
	return store.Feed{URL: url, Name: name, Tags: tags, Type: item.TypeArticle}
}

func in(term string, w float64) store.Interest { return store.Interest{Term: term, Weight: w} }

var topics = []topic{
	{
		Key: "systems", Name: "Distributed systems", Blurb: "Consensus, databases, and how real systems fail.", Tech: true,
		Interests: []store.Interest{in("distributed systems", 1), in("consensus", 1), in("databases", 1), in("storage", 0.8), in("cs.DC", 0.5), in("cs.DB", 0.5)},
		Arxiv:     []string{"cs.DC", "cs.DB"},
		Feeds: []store.Feed{
			feed("https://muratbuffalo.blogspot.com/feeds/posts/default", "Metadata (Murat Demirbas)", "distributed systems"),
			feed("https://jack-vanlightly.com/blog?format=rss", "Jack Vanlightly", "distributed systems"),
			feed("https://aphyr.com/posts.atom", "Aphyr", "distributed systems"),
			feed("https://www.brendangregg.com/blog/rss.xml", "Brendan Gregg"),
		},
		Subjects: []string{"distributed systems"},
	},
	{
		Key: "ai", Name: "Machine learning & AI", Blurb: "Research, models, and the people building them.", Tech: true,
		Interests: []store.Interest{in("machine learning", 1), in("large language models", 1), in("deep learning", 0.8), in("cs.LG", 0.5), in("cs.CL", 0.5)},
		Arxiv:     []string{"cs.LG", "cs.CL"},
		Feeds: []store.Feed{
			feed("https://lilianweng.github.io/index.xml", "Lil'Log"),
			feed("https://karpathy.github.io/feed.xml", "Andrej Karpathy"),
			feed("https://simonwillison.net/atom/everything/", "Simon Willison"),
			feed("https://www.interconnects.ai/feed", "Interconnects"),
		},
		Subjects: []string{"artificial intelligence"},
	},
	{
		Key: "programming", Name: "Programming", Blurb: "Languages, tools, and the craft of building software.", Tech: true,
		Interests: []store.Interest{in("programming languages", 1), in("compilers", 1), in("software engineering", 0.8), in("debugging", 0.8), in("cs.PL", 0.5)},
		Arxiv:     []string{"cs.PL", "cs.SE"},
		Feeds: []store.Feed{
			feed("https://jvns.ca/atom.xml", "Julia Evans"),
			feed("https://danluu.com/atom.xml", "Dan Luu"),
			feed("https://matklad.github.io/feed.xml", "matklad"),
			feed("https://computer.rip/rss.xml", "Computers Are Bad"),
			{URL: "https://lobste.rs/rss", Name: "Lobsters", Type: item.TypeNews},
		},
		Subjects: []string{"computer programming"},
	},
	{
		Key: "security", Name: "Security & privacy", Blurb: "Breaches, cryptography, and the people who find the holes.", Tech: true,
		Interests: []store.Interest{in("security", 1), in("cryptography", 1), in("vulnerability", 0.8), in("privacy", 0.8), in("cs.CR", 0.5)},
		Arxiv:     []string{"cs.CR"},
		Feeds: []store.Feed{
			feed("https://krebsonsecurity.com/feed/", "Krebs on Security"),
			feed("https://www.schneier.com/feed/atom/", "Schneier on Security"),
		},
		Subjects: []string{"computer security"},
	},
	{
		Key: "science", Name: "Science writing", Blurb: "Physics, biology and mathematics, written for curious readers.",
		Interests: []store.Interest{in("physics", 1), in("mathematics", 1), in("biology", 0.8), in("astronomy", 0.8)},
		Feeds: []store.Feed{
			feed("https://www.quantamagazine.org/feed/", "Quanta Magazine"),
			feed("https://nautil.us/feed/", "Nautilus"),
		},
		Subjects: []string{"science"},
	},
	{
		Key: "ideas", Name: "Ideas & philosophy", Blurb: "Long essays on minds, meaning, and how to live.",
		Interests: []store.Interest{in("philosophy", 1), in("ethics", 0.8), in("consciousness", 0.8)},
		Feeds: []store.Feed{
			feed("https://aeon.co/feed.rss", "Aeon"),
			feed("https://www.themarginalian.org/feed/", "The Marginalian"),
		},
		Subjects: []string{"philosophy"},
	},
	{
		Key: "fiction", Name: "Literary fiction", Blurb: "Novels, stories, and the writers behind them.",
		Interests: []store.Interest{in("fiction", 1), in("novel", 0.8), in("writers", 0.6)},
		Feeds: []store.Feed{
			feed("https://lithub.com/feed/", "Literary Hub"),
			feed("https://www.theparisreview.org/blog/feed/", "The Paris Review"),
			feed("https://www.lrb.co.uk/feeds/rss", "London Review of Books"),
		},
		Subjects: []string{"literary fiction"},
	},
	{
		Key: "sff", Name: "Science fiction & fantasy", Blurb: "Other worlds, from the classics to this year’s releases.",
		Interests: []store.Interest{in("science fiction", 1), in("fantasy", 1)},
		Subjects:  []string{"science fiction", "fantasy"},
	},
	{
		Key: "history", Name: "History", Blurb: "The ancient world to the archives, told well.",
		Interests: []store.Interest{in("history", 1), in("archaeology", 0.6)},
		Feeds: []store.Feed{
			feed("https://acoup.blog/feed/", "A Collection of Unmitigated Pedantry"),
			feed("https://publicdomainreview.org/rss.xml", "The Public Domain Review"),
			feed("https://www.smithsonianmag.com/rss/history/", "Smithsonian: History"),
		},
		Subjects: []string{"history"},
	},
	{
		Key: "business", Name: "Technology & business", Blurb: "Strategy, startups, and how the industry works.", Tech: true,
		Interests: []store.Interest{in("startups", 1), in("strategy", 0.8), in("technology", 0.5)},
		Feeds: []store.Feed{
			feed("http://www.aaronsw.com/2002/feeds/pgessays.rss", "Paul Graham"),
			feed("https://stratechery.com/feed/", "Stratechery"),
		},
		Subjects: []string{"business"},
	},
}

type welcomeData struct {
	Topics   []topic
	Selected map[string]bool
	Authors  string
	Error    string
}

func (s *Server) welcomePage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, http.StatusOK, "welcome", "Welcome", "", welcomeData{Topics: topics, Selected: map[string]bool{}})
}

func splitNames(s string) []string {
	var out []string
	for _, line := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' || r == ';' }) {
		if line = strings.TrimSpace(line); line != "" && len(line) <= 100 && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out
}

func (s *Server) welcome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseMultipartForm(5 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if r.FormValue("skip") != "" {
		s.Store.SetSetting(ctx, onboardedKey, "skipped")
		http.Redirect(w, r, "/interests", http.StatusSeeOther)
		return
	}

	selected := map[string]bool{}
	for _, k := range r.Form["topic"] {
		selected[k] = true
	}
	authors := splitNames(r.FormValue("authors"))
	var imported []store.Feed
	if file, _, err := r.FormFile("opml"); err == nil {
		defer file.Close()
		feeds, perr := parseOPML(file)
		if perr != nil {
			s.renderPage(w, r, http.StatusOK, "welcome", "Welcome", "", welcomeData{Topics: topics, Selected: selected,
				Authors: r.FormValue("authors"), Error: "That file isn’t OPML. Feed readers export it under Export or Backup."})
			return
		}
		imported = feeds
	}
	if len(selected) == 0 && len(authors) == 0 && len(imported) == 0 {
		s.renderPage(w, r, http.StatusOK, "welcome", "Welcome", "", welcomeData{Topics: topics, Selected: selected,
			Authors: r.FormValue("authors"), Error: "Pick at least one topic, add an author you love, or import your feeds."})
		return
	}

	var cats []string
	tech := false
	var feeds []store.Feed
	for _, t := range topics {
		if !selected[t.Key] {
			continue
		}
		tech = tech || t.Tech
		for _, i := range t.Interests {
			if err := s.Store.SaveInterest(ctx, i.Term, i.Weight); err != nil {
				s.serverError(w, "welcome", err)
				return
			}
		}
		for _, c := range t.Arxiv {
			if !slices.Contains(cats, c) {
				cats = append(cats, c)
			}
		}
		feeds = append(feeds, t.Feeds...)
		for _, sub := range t.Subjects {
			s.Store.AddBookFollow(ctx, store.FollowSubject, sub)
		}
	}
	for _, a := range authors {
		s.Store.AddBookFollow(ctx, store.FollowAuthor, a)
	}
	if _, err := s.importFeeds(ctx, append(feeds, imported...)); err != nil {
		s.serverError(w, "welcome", err)
		return
	}
	if s.Settings != nil && s.SaveSettings != nil {
		st := s.Settings(ctx)
		st.ArxivEnabled, st.ArxivCategories, st.HNEnabled = len(cats) > 0, cats, tech
		if err := st.Normalize(); err == nil {
			s.SaveSettings(ctx, st)
		}
	}
	s.Store.SetSetting(ctx, onboardedKey, "yes")
	if s.Rescore != nil {
		s.Rescore(ctx)
	}
	if s.StartFetch != nil {
		s.StartFetch()
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
