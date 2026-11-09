package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"radar/internal/digest"
	"radar/internal/links"
	"radar/internal/mail"
)

func (a *app) digest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("digest", flag.ContinueOnError)
	preview := fs.String("preview", "", "write the HTML email to this file instead of sending")
	text := fs.Bool("text", false, "print the plain-text email instead of sending")
	books := fs.Bool("books", false, "include the weekly books block even if it isn't due")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *preview == "" && !*text {
		return a.sendDigest(ctx, *books)
	}

	d, r, err := a.buildDigest(ctx, *books)
	if err != nil {
		return err
	}
	if d.Count() == 0 {
		fmt.Fprintln(a.out, "nothing new for a digest - run 'radar fetch' first")
		return nil
	}
	if *preview != "" {
		if err := os.WriteFile(*preview, []byte(r.HTML), 0o644); err != nil {
			return err
		}
		a.log.Info("wrote preview (nothing sent, nothing marked as sent)", "path", *preview, "subject", r.Subject, "items", d.Count())
	}
	if *text {
		fmt.Fprintf(a.out, "Subject: %s\n\n%s", r.Subject, r.Text)
	}
	return nil
}

const booksLastDigestKey = "books_last_digest"

// booksDue reports whether the weekly books block belongs in a digest sent
// at now: on the configured weekday, or on any day once a week has passed
// since the last one (so a missed Sunday doesn't skip a week).
func (a *app) booksDue(ctx context.Context, now time.Time) (bool, error) {
	day, weekly, _ := a.cfg.Digest.Books.ParseWeekday() // validated at load
	if !weekly || now.Weekday() == day {
		return true, nil
	}
	last, err := a.store.TimeSetting(ctx, booksLastDigestKey)
	if err != nil {
		return false, err
	}
	return !last.IsZero() && now.Sub(last) > 7*24*time.Hour, nil
}

// buildDigest rescores (so recency is current), then selects and renders.
func (a *app) buildDigest(ctx context.Context, forceBooks bool) (*digest.Digest, digest.Rendered, error) {
	if err := a.rescoreAll(ctx); err != nil {
		return nil, digest.Rendered{}, fmt.Errorf("rescore: %w", err)
	}
	key, err := a.store.FeedbackKey(ctx)
	if err != nil {
		return nil, digest.Rendered{}, err
	}
	_, _, loc, _, _ := a.cfg.Schedule.DigestClock() // validated at load
	now := time.Now().In(loc)
	includeBooks := forceBooks
	if !includeBooks {
		var err error
		if includeBooks, err = a.booksDue(ctx, now); err != nil {
			return nil, digest.Rendered{}, err
		}
	}
	sc, err := a.scorer(ctx)
	if err != nil {
		return nil, digest.Rendered{}, err
	}
	b := a.cfg.Digest.Books
	d, err := digest.Build(ctx, a.store, digest.Options{
		Sections: a.digestSections(),
		MinScore: a.cfg.Digest.MinScore,
		Lookback: a.cfg.Digest.Lookback,
		Books: digest.BookOptions{
			Include: includeBooks, PickOfWeek: b.PickOfWeek,
			NewReleases: b.NewReleases, NewReleaseWindow: b.NewReleaseWindow,
		},
		Explain: sc.MatchedTerms,
	}, now)
	if err != nil || d.Count() == 0 {
		return d, digest.Rendered{}, err
	}
	r, err := digest.Render(d, links.NewSigner(a.cfg.Server.BaseURL, key))
	return d, r, err
}

// sendDigest emails the digest and marks its items as sent. Items are only
// marked after the SMTP server accepts the message, so a failed send is
// retried with the same items next time.
func (a *app) sendDigest(ctx context.Context, forceBooks bool) error {
	ec := a.cfg.Email
	if err := ec.ValidateForSending(); err != nil {
		return fmt.Errorf("email config: %w", err)
	}
	if u, _ := url.Parse(a.cfg.Server.BaseURL); u != nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
		a.log.Warn("server.base_url is localhost: 👍/👎 links will only work on this machine", "base_url", a.cfg.Server.BaseURL)
	}

	d, r, err := a.buildDigest(ctx, forceBooks)
	if err != nil {
		return err
	}
	if d.Count() == 0 {
		a.log.Info("no new items above min_score; digest not sent")
		return nil
	}
	sender := &mail.SMTP{
		Host: ec.SMTPHost, Port: ec.SMTPPort, TLS: mail.TLSMode(ec.TLS),
		Username: ec.Username, Password: ec.Password(),
	}
	msg := mail.Message{From: ec.From, To: ec.To, Subject: r.Subject, Text: r.Text, HTML: r.HTML, Date: d.Date}
	if err := sender.Send(ctx, msg); err != nil {
		return fmt.Errorf("send digest: %w", err)
	}
	id, err := a.store.RecordDigest(ctx, r.Subject, d.IDs(), time.Now())
	if err != nil {
		// Sent but not recorded: these items may repeat in the next digest.
		return fmt.Errorf("digest sent but not recorded: %w", err)
	}
	if d.HasBooks() {
		if err := a.store.SetTimeSetting(ctx, booksLastDigestKey, time.Now()); err != nil {
			return err
		}
	}
	a.log.Info("digest sent", "id", id, "subject", r.Subject, "items", d.Count(), "to", ec.To)
	return nil
}
