# Radar

A self-hosted reading recommender. It builds a curated digest of papers (arXiv), articles (your RSS/Atom feeds), HN posts and books (Open Library + Google Books), ranked against your interests with classical methods (no LLM APIs). It runs as a single Go binary backed by SQLite.

## Quick start

```bash
cp radar.example.yaml radar.yaml   # edit interests
go build -o radar ./cmd/radar
./radar fetch                      # all sources -> SQLite, then score
./radar top -explain               # best unrated items, with score breakdown
./radar feedback 27615b26 up       # 👍 (id prefix ok); rescoring happens automatically
./radar digest -preview out.html   # render the email without sending it (-books adds the weekly books block)
```

To run it for real, fill in the `email` and `server` sections, then:

```bash
export RADAR_SMTP_PASSWORD=...     # app password for your mail provider
export RADAR_GOOGLE_BOOKS_KEY=...  # optional but recommended, see Books below
export RADAR_UI_PASSWORD=...       # web UI sign-in
./radar serve                      # web UI + feedback links + fetch every 6h + digest daily at 07:00
```

Open `server.base_url` in a browser for the web UI. On a phone, use "Add to Home Screen" or "Install app" to get Radar as a PWA.

`radar serve` handles the 👍/👎 links and runs the schedule, so you don't need cron. You can still run `radar fetch` and `radar digest` from cron and use `radar serve -no-schedule` for the links only.

The binary is CGO-free (`modernc.org/sqlite`), so `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/radar` cross-compiles for a Pi or VPS.

## Layout

```
cmd/radar/            CLI: serve, fetch, digest, top, feedback, rescore
internal/item/        Item model, URL canonicalization, ID, merge rules
internal/fetch/       Fetcher interface + HTTP client (UA, retries, backoff)
  arxiv.go            arXiv Atom API: one sorted query across categories
  hackernews.go       Algolia HN search: stories >= min_points in lookback window
  rss.go              RSS 2.0 / RSS 1.0 / Atom, conditional GET, any charset
  books.go            Google Books + Open Library: followed authors, subjects
internal/pipeline/    fetch (concurrent) -> normalize -> dedupe -> save -> rescore
internal/score/       keyword, TF-IDF similarity, recency
internal/store/       SQLite store + numbered migrations (PRAGMA user_version)
internal/digest/      section selection + HTML/text email templates
internal/mail/        MIME multipart/alternative + SMTP (STARTTLS / TLS / none)
internal/links/       HMAC-signed feedback URLs
internal/server/      HTTP: web UI (templates + htmx + PWA), auth, feedback endpoints
  templates/          layout, pages, htmx partials
  static/             CSS, vendored htmx 2.0.4, icons
internal/schedule/    in-process jobs: fetch every N, digest daily at HH:MM
internal/config/      YAML config overlaid on defaults
```

## How it works

**Identity and dedupe.** Each URL is canonicalized: https, no `www.`, no tracking params or fragment, sorted query string. arXiv `abs`/`pdf`/`vN` links all collapse to `arxiv.org/abs/<id>`. `ID = sha1(canonical URL)[:16]`. Items with the same ID, or the same title (normalized, 4 or more words), are merged. The richer type wins (paper > book > article > news), tags and meta are combined, and your feedback is kept. So an HN post that links an arXiv paper becomes a single paper item that also carries `hn_points` and `hn_url`.

**Scoring.** Each signal is in [0, 1]:

- **keyword**: interest terms are matched as whole-word phrases. A title hit counts 1.0, a tag hit 0.8 and a summary hit 0.5, each multiplied by the term's weight. The sum is squashed with `1 - e^-x`. Negative weights subtract.
- **similarity**: the highest TF-IDF cosine similarity to any 👍 item. The vocabulary is built from the last `scoring.window` of items plus all liked items.
- **recency**: `0.5^(age / half_life)`, with a half-life set per type.

`score = 0.5·keyword + 0.3·similarity + 0.2·recency`. Every item in the window is rescored after each fetch and after each piece of feedback. Signals are stored per item so a digest can explain its picks.

**Digest.** Each section (papers, articles, discussions) gets the highest-scoring items that haven't been sent or rated yet. Items are marked as sent only after the SMTP server accepts the message, so a failed send retries the same items next time. Every entry says why it was picked, for example "matches large language models", "similar to items you liked" or "popular on HN".

**Feedback links.** Each link is `/f/{id}/{up|down}?t=<HMAC>`. The signing key is generated on first use and stored in the database, so there's no login and no secret to configure. Opening a link shows a page that sends a POST to record the vote. Mail scanners and link prefetchers only make GET requests and don't run scripts, so they can't vote for you by accident. The confirmation page also lets you change or clear the vote.

**Sources and schedule.** `radar serve` runs a fetch job every `schedule.fetch_every`. Each source can also set `every` as a minimum gap between its own fetches. Books default to `every: 24h`, so they're fetched daily while everything else runs every 6 hours. `radar fetch -force` ignores the gaps, and naming sources with `-source` implies it. If one feed or query fails, the rest are still saved, and the failure is reported.

**RSS.** Feeds are listed in `sources.rss.feeds`. Radar remembers each feed's ETag and Last-Modified headers, so unchanged feeds cost one cheap 304 response. Posts older than `max_age` are skipped, so adding a blog doesn't import its whole archive. Feed `tags` are added to every post and match interests exactly.

**Books.** `authors` are followed for new releases. `subjects` supply candidates for the book of the week, using Open Library's most-read works and Google's most relevant books for each subject. The same book from both APIs is merged on title plus the author's surname. The books block goes out once a week (`digest.books.weekday`). If that day's digest is missed, it goes in the next one. It contains:
- **Book of the week**: the best-scoring unsent book of any age. Book recency has a 3-year half-life, so classics can still win.
- **New from authors you follow**: books by followed authors published within `new_release_window`. Only books with a known publication date count.

Google Books is the main source of exact release dates and descriptions. Without an API key, requests go to a quota shared by every anonymous caller, and that quota is usually used up. Radar then logs a hint and continues with Open Library alone, which only knows publication years, so the new-releases section stays mostly empty. A free key from Google Cloud Console fixes this; set `RADAR_GOOGLE_BOOKS_KEY`.

An interest that names an author ("Ted Chiang") matches that author's books and papers.

**Web UI.** Server-rendered Go templates. Buttons use htmx, so rating, saving or editing swaps in just the changed fragment.
- **Today**: the best unrated items, with type tabs and "Load more". Each card has 👍/👎, save to the reading list, and a "Why" line.
- **Reading list**: saved items, unread first, with a mark-read toggle.
- **History**: past digests with how many items you liked or disliked, plus everything you've liked or disliked.
- **Interests**: weighted terms (changes re-rank everything immediately), followed authors and book subjects.
- **Feeds**: add a feed or just a blog's home page (Radar finds the feed link), set tags and type, pause, remove, see each feed's health, and "Fetch now".

Interests, feeds and book follows live in SQLite. The config file only seeds them on first run.

**PWA.** The app has a manifest, icons including a maskable one, and a service worker. Today and the reading list are cached network-first, so they open offline once you've visited them. Static assets carry a content hash in their URL, so a new build is picked up straight away.

**Security.** The UI has a single password, read from `RADAR_UI_PASSWORD`. Sign-in uses an HMAC-signed, HttpOnly, SameSite=Lax session cookie that lasts 30 days, and changing the password signs out every session. State-changing requests go through Go's `http.CrossOriginProtection`. The content security policy allows only same-origin scripts and styles, with no inline code, which is why htmx is vendored rather than loaded from a CDN. Set `server.ui_auth: none` only on a network you already control.

## Roadmap

1. ~~arXiv + HN fetchers, scoring, storage~~
2. ~~RSS/Atom fetcher, Open Library + Google Books fetchers, weekly book pick and followed-author releases~~
3. ~~Email digest (SMTP) with signed 👍/👎 links, `radar serve` feedback endpoint, in-process scheduler~~
4. ~~Web UI (Go templates + htmx, PWA): history, reading list, manage interests/feeds; profile moves into SQLite~~
5. Learned ranking: Naive Bayes / logistic regression on feedback, replacing the fixed weights
6. TUI client
