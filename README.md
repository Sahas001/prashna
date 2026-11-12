# Radar

A reading app that runs on your own computer. Each morning it puts together a short **edition** of papers, essays, discussions and books chosen for you, and it gets better at choosing as you read.

It's free: no accounts, no subscriptions, no paid services, no server to rent. Everything it learns about you stays in one folder on your machine.

## Install

Download the file for your computer from the [releases page](https://github.com/Sahas001/radar/releases), unpack it, and run `radar`:

| System | File | Then |
| --- | --- | --- |
| macOS (Apple silicon / Intel) | `radar_*_darwin_arm64.tar.gz` / `_amd64` | `./radar`. The first time, macOS may block it: right-click it and choose Open, or run `xattr -d com.apple.quarantine radar`. |
| Windows | `radar_*_windows_amd64.zip` | Double-click `radar.exe`. |
| Linux | `radar_*_linux_amd64.tar.gz` / `_arm64` | `./radar` |

Radar opens in your browser at `http://127.0.0.1:7878`. To keep it handy:

- **Install it as an app**: in Chrome or Edge, use *Install Radar* in the address bar; in Safari, *File > Add to Dock*.
- **Start it when you log in**: run `radar autostart on`. It then runs quietly in the background, and opening the app (or running `radar`) shows it instantly.

Or, with Go 1.27+: `go install github.com/Sahas001/radar/cmd/radar@latest`.

## Using it

The first time, Radar asks what you like to read. Pick a few topics, name authors you love, or import the feeds from your current reader (OPML). It then gathers your first edition, which takes about a minute.

- **Today**: the edition. It has a lead story, a few papers, essays and discussions, and on Sundays a book of the week plus new books from authors you follow. It stays the same all day, and it ends, so there's no endless scrolling. Tomorrow's edition is made from whatever arrives overnight.
- **Explore**: everything recent, best first, for when you want more.
- **Library**:
  - **To read**: things you saved.
  - **Favorites**: things you marked *More like this*, kept for rereading.
  - **Read**: everything you've opened, most recent first, with how many times you've opened each.

  - **Highlights**: every passage you've marked and every note you've written, newest first.

  Once in a while, Today also brings back something from your library that you haven't opened in a while.
- **Archive**: every past edition, plus the things you marked *Less*.
- **Search** covers everything Radar has ever found you.
- **Interests**, **Feeds** and **Settings** are where you tell it more.

Essays and discussions open in Radar's own **reader**: just the text, set for reading, without the site around it. The text is kept on your computer (saving a piece fetches it right away), so you can read it later without the internet. Select any passage to **highlight** it, add a note to a highlight, or write a note on the whole piece. If a site won't give up its text (a sign-in wall, a PDF), the reader says so and links to the original. Papers and books open on their own pages.

Each piece has three buttons: **Save**, **More like this** and **Less**. Clicking through to read it counts too. That's all Radar learns from.

## Where reading comes from

Every source is free and needs no key:

- **Blogs and magazines** you follow by RSS/Atom. Paste a site's address and Radar finds its feed.
- **Hacker News** stories above a points threshold (on if you pick a tech topic).
- **arXiv** papers in the categories you choose.
- **Books** from [Open Library](https://openlibrary.org) (most-read books on your subjects, descriptions and covers) and [Wikidata](https://www.wikidata.org) (exact release dates for authors you follow).

## How it chooses

No AI service is involved; it's all classical techniques running locally:

- **Interest matching**: weighted words and phrases in titles, summaries and tags, plus exact author names and categories.
- **Similarity**: TF-IDF comparison with the pieces you've liked.
- **Recency**: newer pieces get a boost that fades with time.
- **A model of your taste**: logistic regression trained on your ratings. It only takes part once it ranks your past ratings better than your interest list does, checked by cross-validation. Its share then grows as you rate more. *Interests* shows what it has learned ("you tend to like cs.DC, Aeon").

Each piece in an edition says why it was picked ("matches consensus", "similar to things you liked").

## Your data

Everything lives in one folder:

- **Linux**: `~/.config/radar`
- **macOS**: `~/Library/Application Support/Radar`
- **Windows**: `%AppData%\Radar`

Override it with `-data DIR` or `RADAR_HOME`. The folder holds the SQLite database, a log, and an optional `config.yaml` (see [radar.example.yaml](radar.example.yaml)). Back up by copying the folder; move to a new computer by copying it there. Radar only contacts the sources above, and only to fetch reading.

## Reading on your phone

Radar listens only to your own computer by default. To read on your phone at home:

1. Set `addr: 0.0.0.0:7878` in `config.yaml`.
2. Set a password in the `RADAR_PASSWORD` environment variable. Radar won't open to other devices without one.
3. Open `http://<your-computer>:7878` on the phone.

Away from home, [Tailscale](https://tailscale.com) (free for personal use) does the same without opening anything to the internet.

## Commands

```
radar                 open Radar (starts it if needed)
radar run             run without opening a browser
radar fetch           check all sources now
radar top             best unread items, in the terminal
radar model           what Radar has learned from your ratings
radar autostart on    start at login (off, status)
radar version
```

## Development

```
go test ./...                                         # unit and handler tests
RADAR_LIVE=1 go test -run Live ./internal/server/     # checks the starter feeds over the network
go run ./cmd/radar -data /tmp/radar-dev               # a throwaway data folder
```

The code is one Go module with no CGO (pure-Go SQLite), so `CGO_ENABLED=0 GOOS=windows go build ./cmd/radar` cross-compiles anywhere. Pushing a `v*` tag builds release archives for all platforms with GoReleaser on GitHub Actions.

```
cmd/radar/          the app: CLI, background fetching, autostart
internal/fetch/     arXiv, Hacker News, RSS/Atom, Open Library, Wikidata
internal/reader/    the reader: fetch a page, keep the article, strip everything unsafe
internal/pipeline/  fetch, deduplicate, save, rescore
internal/score/     interest matching, TF-IDF similarity, recency
internal/learn/     the ratings model: features, logistic regression, cross-validation
internal/edition/   choosing and keeping each day's edition
internal/store/     SQLite: items, editions, library, articles, highlights, profile, migrations
internal/server/    pages (Go templates + htmx), PWA, sign-in
```

## Credits

Set in [Literata](https://github.com/googlefonts/literata) (SIL Open Font License, included). Interactions use [htmx](https://htmx.org) (Zero-Clause BSD). The reader finds article text with [go-readability](https://codeberg.org/readeck/go-readability) (MIT), a port of Mozilla's Readability. Book data from Open Library and Wikidata (CC0).
