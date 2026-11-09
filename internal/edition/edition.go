// Package edition makes Radar's daily edition: once a day it picks a lead
// story and a few of the best unread papers, articles and discussions
// (plus a weekly books block), and keeps that selection fixed for the day
// so it can be read, left, and come back to.
package edition

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Sahas001/radar/internal/item"
	"github.com/Sahas001/radar/internal/store"
)

// Section keys, in the order sections appear.
const (
	SectionLead     = "lead"
	SectionPapers   = "paper"
	SectionArticles = "article"
	SectionNews     = "news"
	SectionBookPick = "book-pick"
	SectionBookNew  = "book-new"
)

var sectionOrder = []string{SectionLead, SectionPapers, SectionArticles, SectionNews, SectionBookPick, SectionBookNew}

var sectionTitles = map[string]string{
	SectionLead:     "Lead",
	SectionPapers:   "Papers",
	SectionArticles: "Essays & articles",
	SectionNews:     "Discussions",
	SectionBookPick: "Book of the week",
	SectionBookNew:  "New from authors you follow",
}

// SectionTitle returns the heading for a section key.
func SectionTitle(key string) string { return sectionTitles[key] }

// MetaFollowedAuthor matches fetch.MetaFollowedAuthor (not imported, to
// keep editions independent of the fetchers).
const MetaFollowedAuthor = "followed_author"

// Store is what making and reading editions needs; *store.Store has it all.
type Store interface {
	Candidates(ctx context.Context, q store.CandidateQuery) ([]item.Item, error)
	EditionForDay(ctx context.Context, day string) (store.EditionSummary, []store.EditionItem, error)
	SaveEdition(ctx context.Context, day, title string, madeAt time.Time, entries []store.EditionEntry) (int64, error)
	TimeSetting(ctx context.Context, key string) (time.Time, error)
	SetTimeSetting(ctx context.Context, key string, t time.Time) error
}

type Options struct {
	// Sections maps item type to how many items its section gets.
	Sections map[item.Type]int
	MinScore float64
	Lookback time.Duration // only items fetched this recently
	Books    BookOptions
}

// BookOptions controls the weekly books block.
type BookOptions struct {
	Weekly           bool         // false = books in every edition
	Weekday          time.Weekday // the day books appear when Weekly
	PickOfWeek       bool
	NewReleases      int
	NewReleaseWindow time.Duration
	include          bool // decided per edition by booksDue
}

// Plan is a selection, before it is saved.
type Plan struct {
	Sections []PlanSection
}

type PlanSection struct {
	Key   string
	Items []item.Item
}

// Count is the number of items in the plan.
func (p Plan) Count() int {
	n := 0
	for _, s := range p.Sections {
		n += len(s.Items)
	}
	return n
}

// HasBooks reports whether the plan includes a books section.
func (p Plan) HasBooks() bool {
	for _, s := range p.Sections {
		if s.Key == SectionBookPick || s.Key == SectionBookNew {
			return true
		}
	}
	return false
}

func (p Plan) entries() []store.EditionEntry {
	var out []store.EditionEntry
	for _, s := range p.Sections {
		for _, it := range s.Items {
			out = append(out, store.EditionEntry{Section: s.Key, ItemID: it.ID})
		}
	}
	return out
}

var contentTypes = []struct {
	typ item.Type
	key string
}{{item.TypePaper, SectionPapers}, {item.TypeArticle, SectionArticles}, {item.TypeNews, SectionNews}}

// Build selects an edition from the current scores. The lead is the
// single best item across papers, articles and discussions; each section
// then gets its share of the rest.
func Build(ctx context.Context, src Store, opts Options, now time.Time) (Plan, error) {
	lists := make([][]item.Item, len(contentTypes))
	lead := -1
	for i, ct := range contentTypes {
		limit := opts.Sections[ct.typ]
		if limit <= 0 {
			continue
		}
		items, err := src.Candidates(ctx, store.CandidateQuery{
			Type: ct.typ, Since: now.Add(-opts.Lookback), MinScore: opts.MinScore, Limit: limit + 1,
		})
		if err != nil {
			return Plan{}, fmt.Errorf("edition %s: %w", ct.typ, err)
		}
		lists[i] = items
		if len(items) > 0 && (lead < 0 || items[0].Score > lists[lead][0].Score) {
			lead = i
		}
	}

	var p Plan
	if lead >= 0 {
		p.Sections = append(p.Sections, PlanSection{Key: SectionLead, Items: lists[lead][:1]})
		lists[lead] = lists[lead][1:]
	}
	for i, ct := range contentTypes {
		items := lists[i]
		if n := opts.Sections[ct.typ]; len(items) > n {
			items = items[:n]
		}
		if len(items) > 0 {
			p.Sections = append(p.Sections, PlanSection{Key: ct.key, Items: items})
		}
	}
	if opts.Books.include {
		books, err := buildBooks(ctx, src, opts, now)
		if err != nil {
			return Plan{}, err
		}
		p.Sections = append(p.Sections, books...)
	}
	return p, nil
}

// buildBooks picks the book of the week (best unread book of any age) and
// recent books by followed authors (regardless of score: you follow them).
func buildBooks(ctx context.Context, src Store, opts Options, now time.Time) ([]PlanSection, error) {
	b := opts.Books
	var releases []item.Item
	if b.NewReleases > 0 {
		var err error
		releases, err = src.Candidates(ctx, store.CandidateQuery{
			Type: item.TypeBook, PublishedSince: now.Add(-b.NewReleaseWindow),
			// Require a known date: an undated book would otherwise look
			// published on the day it was fetched.
			MetaKeys: []string{MetaFollowedAuthor, item.MetaDatePrecision}, Limit: b.NewReleases,
		})
		if err != nil {
			return nil, fmt.Errorf("edition new releases: %w", err)
		}
	}
	var out []PlanSection
	if b.PickOfWeek {
		cands, err := src.Candidates(ctx, store.CandidateQuery{Type: item.TypeBook, MinScore: opts.MinScore, Limit: len(releases) + 1})
		if err != nil {
			return nil, fmt.Errorf("edition book pick: %w", err)
		}
	pick:
		for _, c := range cands {
			for _, r := range releases {
				if r.ID == c.ID {
					continue pick
				}
			}
			out = append(out, PlanSection{Key: SectionBookPick, Items: []item.Item{c}})
			break
		}
	}
	if len(releases) > 0 {
		out = append(out, PlanSection{Key: SectionBookNew, Items: releases})
	}
	return out, nil
}

const booksLastKey = "books_last_edition"

// booksDue reports whether the weekly books block belongs in an edition
// made at now: in the very first one (so a new reader sees it), on the
// chosen weekday, or on any day once a week has passed since the last one
// (so a skipped day doesn't skip a week).
func booksDue(ctx context.Context, st Store, b BookOptions, now time.Time) (bool, error) {
	if !b.Weekly || now.Weekday() == b.Weekday {
		return true, nil
	}
	last, err := st.TimeSetting(ctx, booksLastKey)
	if err != nil {
		return false, err
	}
	return last.IsZero() || now.Sub(last) > 7*24*time.Hour, nil
}

// Day is the edition key for a moment: its local date.
func Day(t time.Time) string { return t.Local().Format("2006-01-02") }

// Title is how an edition is named: "Monday, 9 November 2026".
func Title(t time.Time) string { return t.Local().Format("Monday, 2 January 2006") }

// Ensure makes today's edition if it doesn't exist yet and there is
// something to put in it. It is safe to call often and from several
// places at once: at most one edition per day is ever saved.
func Ensure(ctx context.Context, st Store, opts Options, now time.Time) (bool, error) {
	day := Day(now)
	if _, _, err := st.EditionForDay(ctx, day); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	include, err := booksDue(ctx, st, opts.Books, now)
	if err != nil {
		return false, err
	}
	opts.Books.include = include
	plan, err := Build(ctx, st, opts, now)
	if err != nil || plan.Count() == 0 {
		return false, err
	}
	if _, err := st.SaveEdition(ctx, day, Title(now), now, plan.entries()); errors.Is(err, store.ErrDuplicate) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if plan.HasBooks() {
		if err := st.SetTimeSetting(ctx, booksLastKey, now); err != nil {
			return true, err
		}
	}
	return true, nil
}

// View is a stored edition grouped for reading.
type View struct {
	store.EditionSummary
	Date     time.Time
	Lead     *item.Item
	Sections []ViewSection
}

type ViewSection struct {
	Key, Title string
	Items      []item.Item
}

// Group arranges a stored edition's items into sections, in reading order.
func Group(sum store.EditionSummary, items []store.EditionItem) View {
	v := View{EditionSummary: sum}
	if d, err := time.ParseInLocation("2006-01-02", sum.Day, time.Local); err == nil {
		v.Date = d
	} else {
		v.Date = sum.MadeAt.Local()
	}
	bySection := map[string][]item.Item{}
	for _, ei := range items {
		bySection[ei.Section] = append(bySection[ei.Section], ei.Item)
	}
	if lead := bySection[SectionLead]; len(lead) > 0 {
		v.Lead = &lead[0]
	}
	for _, key := range sectionOrder[1:] {
		if its := bySection[key]; len(its) > 0 {
			v.Sections = append(v.Sections, ViewSection{Key: key, Title: sectionTitles[key], Items: its})
		}
	}
	return v
}
