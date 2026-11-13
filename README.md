# Prashna

*Prashna* (प्रश्न) is Sanskrit for "question". The app was called Radar before; if you used it then, your library, ratings and settings move over by themselves the first time Prashna runs.

A reading app that runs on your own computer. Each morning it puts together a short **edition** of papers, essays, discussions and books chosen for you, and it gets better at choosing as you read.

It's free: no accounts, no subscriptions, no paid services, no server to rent. Everything it learns about you stays in one folder on your machine.

## Install

Download the file for your computer from the [releases page](https://github.com/Sahas001/prashna/releases), unpack it, and run `prashna`:

| System | File | Then |
| --- | --- | --- |
| macOS (Apple silicon / Intel) | `prashna_*_darwin_arm64.tar.gz` / `_amd64` | `./prashna`. The first time, macOS may block it: right-click it and choose Open, or run `xattr -d com.apple.quarantine prashna`. |
| Windows | `prashna_*_windows_amd64.zip` | Double-click `prashna.exe`. |
| Linux | `prashna_*_linux_amd64.tar.gz` / `_arm64` | `./prashna` |

Or, with Go 1.27+: `go install github.com/Sahas001/prashna/cmd/prashna@latest`. (Releases from before the rename carry the old name, `radar_*`.)

Prashna opens in your browser at `http://127.0.0.1:7878`. To keep it handy:

- **Install it as an app**: in Chrome or Edge, use *Install Prashna* in the address bar; in Safari, *File > Add to Dock*.
- **Start it when you log in**: run `prashna autostart on`. It then runs quietly in the background, and opening the app (or running `prashna`) shows it instantly.

### The desktop app

Prashna can also be built as a desktop app that opens in its own window instead of a browser tab. It is the same program and the same data; the window is drawn by the web view your system already has (WebView2 on Windows, WebKit on macOS, WebKitGTK on Linux), so nothing extra is bundled.

- Closing the window quits Prashna. To keep it fetching in the background, run `prashna autostart on`: the window then just shows the copy that is already running.
- Links to other sites open in your normal browser.
- A window has no browser buttons, so the top bar gets a back arrow.
- Starting Prashna again brings the open window forward instead of opening a second one.
- On Linux, `prashna launcher on` adds Prashna to your applications menu (`launcher off` removes it).

The next release will be the first to include it, as `prashna-desktop_*` files on the releases page. Until then, see *Development* for how to build it.

## Using it

The first time, Prashna asks what you like to read. Pick a few topics, name authors you love, or import the feeds from your current reader (OPML). It then gathers your first edition, which takes about a minute.

- **Today**: the edition. It has a lead story, a few papers, essays and discussions, and on Sundays a book of the week plus new books from authors you follow. It stays the same all day, and it ends, so there's no endless scrolling. Tomorrow's edition is made from whatever arrives overnight.
- **Explore**: everything recent, best first, for when you want more.
- **Library**:
  - **To read**: things you saved.
  - **Favorites**: things you marked *More like this*, kept for rereading.
  - **Read**: everything you've opened, most recent first, with how many times you've opened each.
  - **Highlights**: every passage you've marked and every note you've written, newest first.

  Once in a while, Today also brings back something from your library that you haven't opened in a while.
- **Archive**: every past edition, plus the things you marked *Less*.
- **Search** covers everything Prashna has ever found you.
- **Interests**, **Feeds** and **Settings** are where you tell it more.

Each piece has three buttons: **Save**, **More like this** and **Less**. Opening a piece to read it counts too. That's all Prashna learns from.

### Reading in the app

Essays, discussions and arXiv papers open in Prashna's own **reader**: just the text, set for reading, without the site around it.

- **Kept on your computer.** The text is stored the first time you open a piece, and right away when you save one, so you can read it later without the internet.
- **Highlights and notes.** Select any passage and press *Highlight*. Each highlight can carry a note, and every piece has room for a note of its own. They all collect under *Library > Highlights*.
- **Papers.** arXiv papers are read from arXiv's own web version: equations, figures, tables and references, set like the rest of the app, with footnotes gathered into a list of notes at the end. A paper that exists only as a PDF shows its abstract and a button for the PDF.
- **When there's no text.** If a site won't give up its text (a sign-in wall, a PDF, a page that only works with JavaScript), the reader says so and links to the original.
- **Elsewhere.** Books, and papers from places other than arXiv, open on their own pages.

### Adding your own links

Prashna isn't limited to what its sources find. *Library > Add a link* saves any page from the web to your reading list, text and all; an arXiv address is saved as a paper. Links you add never show up in an edition, since they're already yours.

To save from whatever page you're looking at, open `http://127.0.0.1:7878/add` in your browser and drag the *Save to Prashna* button to your bookmarks bar. Clicking it on any page opens Prashna with the address filled in; saving takes one more click there, so no other site can add things for you. (The desktop app's window has no bookmarks bar; its *Add a link* page points you to that address.)

## Where reading comes from

Every source is free and needs no key:

- **Blogs and magazines** you follow by RSS/Atom. Paste a site's address and Prashna finds its feed.
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

- **Linux**: `~/.config/prashna`
- **macOS**: `~/Library/Application Support/Prashna`
- **Windows**: `%AppData%\Prashna`

Override it with `-data DIR` or `PRASHNA_HOME`. The folder holds the SQLite database, a log, and an optional `config.yaml` (see [prashna.example.yaml](prashna.example.yaml)). Back up by copying the folder; move to a new computer by copying it there.

Prashna goes online only to fetch reading: the sources above, the page of a piece you open or save (and, for papers, arXiv's web version of it), and the pictures in a piece, which come from its own site as you read. Nothing about you is sent anywhere. It refuses to fetch addresses on your own computer or home network, so a link in a feed can't be used to peek at them.

## Reading on your phone

Prashna listens only to your own computer by default. To read on your phone at home:

1. Set `addr: 0.0.0.0:7878` in `config.yaml`.
2. Set a password in the `PRASHNA_PASSWORD` environment variable. Prashna won't open to other devices without one.
3. Open `http://<your-computer>:7878` on the phone.

Without a password, Prashna answers only at its own local address (`127.0.0.1` or `localhost`), whatever name a request claims.

Away from home, [Tailscale](https://tailscale.com) (free for personal use) does the same without opening anything to the internet.

## Commands

```
prashna                 open Prashna (starts it if needed)
prashna run             run in the background, without opening anything
prashna fetch           check all sources now
prashna top             best unread items, in the terminal
prashna model           what Prashna has learned from your ratings
prashna autostart on    start at login (off, status)
prashna launcher on     desktop app on Linux: add to the applications menu (off)
prashna version
```

## Development

```
go test ./...                                           # unit and handler tests
PRASHNA_LIVE=1 go test -run Live ./internal/server/     # checks the starter feeds over the network
go run ./cmd/prashna -data /tmp/prashna-dev             # a throwaway data folder
```

The desktop app needs a C compiler and, on Linux, the WebKitGTK development package (`webkit2gtk-4.1`). Build it with the [Wails](https://wails.io) CLI (v2.17 or newer), which also makes the macOS `.app` and gives the Windows program its icon:

```
cd cmd/prashna
wails build -s -skipbindings -tags webkit2_41      # drop -tags on macOS and Windows
./build/bin/prashna
```

or with plain Go: `go build -tags desktop,production,webkit2_41 ./cmd/prashna`. Without the `desktop` tag nothing changes: the window code (`cmd/prashna/window_desktop.go`) is left out.

The rest of the code is one Go module with no CGO (pure-Go SQLite), so `CGO_ENABLED=0 GOOS=windows go build ./cmd/prashna` cross-compiles anywhere. Pushing a `v*` tag builds release archives for all platforms with GoReleaser on GitHub Actions, and the desktop app for Linux, macOS and Windows with a second workflow (`.github/workflows/desktop.yml`).

```
cmd/prashna/        the app: CLI, background fetching, autostart, the desktop window
internal/fetch/     arXiv, Hacker News, RSS/Atom, Open Library, Wikidata
internal/reader/    the reader: fetch a page or an arXiv paper, keep the text, strip everything unsafe
internal/pipeline/  fetch, deduplicate, save, rescore
internal/score/     interest matching, TF-IDF similarity, recency
internal/learn/     the ratings model: features, logistic regression, cross-validation
internal/edition/   choosing and keeping each day's edition
internal/store/     SQLite: items, editions, library, articles, highlights, notes, profile, migrations
internal/server/    pages (Go templates + htmx), reader, PWA, sign-in
```

## Credits

Set in [Literata](https://github.com/googlefonts/literata) (SIL Open Font License, included). Formulas in papers are set in [STIX Two Math](https://github.com/stipub/stixfonts) (SIL Open Font License, included). The name प्रश्न is set in [Noto Serif Devanagari](https://github.com/notofonts/devanagari) (SIL Open Font License, included). Interactions use [htmx](https://htmx.org) (Zero-Clause BSD). The desktop window is made with [Wails](https://wails.io) (MIT). The reader finds article text with [go-readability](https://codeberg.org/readeck/go-readability) (MIT), a port of Mozilla's Readability. Book data from Open Library and Wikidata (CC0).
