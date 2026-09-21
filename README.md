# enginedj5multitool

[![CI](https://github.com/prune998/enginedj5multitool/actions/workflows/ci.yml/badge.svg)](https://github.com/prune998/enginedj5multitool/actions/workflows/ci.yml)

A CLI + GUI tool to inspect and fix the performance data (hot cues and loops)
stored in an **Engine DJ v5** database (`m.db`), used by Denon DJ SC/LC/Prime
gear and Engine DJ Desktop. Built with the pure-Go UI toolkit
[shirei](https://github.com/hasenj/go-shirei).

It can:

- **display** the QuickCues and Loop slots of tracks found by a text filter:
  timestamps, labels, colors, counts, and whether the cues/loops were created
  in chronological order or not
- **fix** them: reorder chronologically, place the latest cue/loop in slot 8,
  apply the standard slot colors, and normalize labels (`intro` / `outro`)
- **edit MP3 tags** manually (ID3v2): title, artist, album, album artist,
  genre, year, track/disc, composer, BPM and comment — with optional sync back
  into the Engine DJ database
- run the fix in **dry-run mode** that never touches the database

## GUI

```sh
./enginedj5multitool          # opens the window, loads ./m.db
./enginedj5multitool -db /path/to/m.db
```

The window has a top bar (library path, music root override, theme selector,
reload), a sidebar with the tools, and a shared track browser (search box —
**Return** runs the search — plus a sortable, virtualized table; click a row
to select it, or navigate with the **↑/↓ arrow keys** — the detail panel and
the list scroll follow along):

- **Cues & Loops** — shows the 8 cue and 8 loop slots of the selected track
  with color swatches, timestamps and order status. `Dry run` previews the fix
  inline (per-slot `slot N ← M` changes); `Fix selected track` and
  `Fix all filtered (N)` apply it (writes only happen when dry run is off).
- **MP3 Tags** — shows the embedded cover art above the form and loads the
  ID3v2 tags of the selected MP3 from disk into an editable form (title,
  artist, album, album artist, genre, year, track #, disc #, composer, BPM,
  comment). The **5-star rating** above the form is stored in the Engine DJ
  database (click a star to set it, click the single filled star again to
  clear) **and mirrored into the file's POPM (popularimeter) ID3 frame** —
  shown as a column in the track list. `Save changes` rewrites the
  ID3v2 tag in place (preserving the tag version, album art and all untouched
  frames; tagless files get a new tag). With *Also update Engine DJ database*
  checked, the matching `Track` row is updated so the library metadata stays
  in sync.
- **Artwork download** — when a track has no embedded cover (or to replace
  it), fetch one from **MusicBrainz** (via the Cover Art Archive, no key
  needed) or **Discogs** (requires a personal access token from
  discogs.com → Settings → Developers). The search uses the **artist name and
  song title** (falling back to the album), and the downloaded art is
  previewed and embedded into the file when you press *Save changes*.
- **Global Edit** — bulk comment maintenance for every track matching the
  current filter: re-order the comment `#tags` alphabetically and add
  `#cued` / `#looped` when a track has more than one cue or loop. Writes the
  ID3 comment (preserving artwork/POPM via the padding-aware writer), can
  sync the Engine DJ `Track.comment`, supports a dry run with a per-track
  report, and skips files it doesn't need to touch.
- **Settings** — edits `config.yaml` (library path, music root, Discogs
  token, theme, browser width) with explicit Save/Reload.

Other UI features:

- **Dark mode** — the top bar has an `Auto | Light | Dark` selector; `Auto`
  (the default) follows the OS appearance.
- **Splitter** — the divider between the track list and the tool panel can be
  dragged to resize them (min/max clamped).
- **#tag bubbles** — `#tags` in the comment are rendered as colored bubbles
  below the comment field: click one to remove it from the comment, or type a
  new one and press `+ Add`. They are stored as plain `#tag` words in the
  comment (the convention for genre/scene tagging).
- **⌘Q / Ctrl-Q** quits the app.

Files are located via the path stored in the `Track` table: absolute paths,
paths relative to the database directory, `../`-chains resolved against the
DB location, or relative to the *music root* from the top bar.

Useful flags:

| Flag        | Description                                                        |
|-------------|--------------------------------------------------------------------|
| `-ui`       | Force the GUI (default when no CLI action flags are given)          |
| `-tool N`   | Open tool tab N (0 = Cues & Loops, 1 = MP3 Tags)                    |
| `-theme T`  | `auto` (OS default), `light` or `dark`                              |
| `-snapshot png` | Render one frame of the UI headlessly to a PNG and exit (used for testing) |

## CLI

```sh
# Display mode (read-only)
./enginedj5multitool -db m.db                       # whole library
./enginedj5multitool -db m.db -filter "Emotion"     # substring on title/artist/album/filename

# Fix mode
./enginedj5multitool -db m.db -filter "Emotion" -fix -dry-run   # preview only
./enginedj5multitool -db m.db -filter "Emotion" -fix            # apply
```

### Flags

| Flag        | Default | Description                                                                 |
|-------------|---------|-----------------------------------------------------------------------------|
| `-db`       | `m.db`  | Path to the Engine DJ database                                              |
| `-filter`   | *(empty)* | Case-insensitive substring matched against `title`, `artist`, `album`, `filename` and `comment` (so `#tags` are searchable). Empty = all tracks. |
| `-fix`      | off     | Reorder cues/loops and apply standard slot colors (see semantics below)     |
| `-dry-run`  | off     | With `-fix`: show exactly what would change without writing to the DB       |

Display mode always opens the DB read-only. `-fix` without `-dry-run` opens it
read-write and updates the `PerformanceData` blobs inside a single transaction.

### Display output

```
#5  Purple Disco Machine — Emotion  [6:10]
──────────────────────────────────────────────
   Cue 1  Cue 1           0:00.067  #1DAFD7  blue (default cue)
   Cue 6  Cue 6           3:24.657  #1DAFD7  blue (default cue)
  → 2 cue(s), in order: NO

   Loop 2  Loop 2          0:47.280 → 0:55.149  #1CC608  green (default loop)  (7.87s ≈ 16.0 beats)
  → 1 loop(s), in order: yes
```

- timestamps are computed from the sample positions stored in the blobs and the
  track's sample rate (read from the `trackData` blob, fallback 44100 Hz)
- colors are shown as hex plus the nearest known Engine DJ palette name, with a
  color swatch when the output is a terminal
- loop duration is shown in seconds and beats (using `bpmAnalyzed`)
- `in order` is `NO` when any later slot holds an earlier timestamp

### Fix semantics

Per track, applied independently to cues and loops (only set slots participate):

1. **Reorder** — set items are sorted chronologically and fill slots
   `1, 2, 3, …`; the **latest** item is always placed in **slot 8** (so with
   5 set items you get slots `1, 2, 3, 4, 8`). A **single** set item is the
   exception: it goes to **slot 1** — it keeps a custom name or becomes
   `Cue 1`/`Loop 1` (never `intro`).
2. **Colors** — every set item gets the standard slot color (see table below),
   alpha 255. Empty slots keep no color.
3. **Labels** — with multiple items, slot 1 is always named `intro` and slot 8
   always `outro` (position-bound, overriding anything else). In middle slots,
   default labels (`Cue 3`, `Loop 2`) are renamed to match the new slot, and
   `intro`/`outro` labels that drift into a middle slot are normalized back to
   `Cue N` / `Loop N`. Genuinely custom labels (`Breakdown`, …) are preserved.
4. **`activeOnLoadLoops`** — this column is treated as a slot bitmask
   (bit *i* = slot *i+1*); when loops move, the bits are remapped accordingly.
   Values outside a byte range are left untouched.

Standard cue/loop colors (from the
[Engine Library format wiki](https://github.com/mixxxdj/mixxx/wiki/Engine%20Library%20format#standard-cueloop-colours)):

| Slot | Color   |        | Slot | Color   |
|------|---------|--------|------|---------|
| 1    | EAC532  | yellow | 5    | 86C64B  |
| 2    | EA8F32  | orange | 6    | 20C67C  |
| 3    | B855BF  | purple | 7    | 00A8B1  |
| 4    | BA2A41  | red    | 8    | 158EE2  |

The fix summary reports how many items were moved / recolored / relabeled and
how many tracks were already clean. Re-running `-fix` on a fixed library is a
no-op.

> **Warning**: `-fix` writes to your library database. Keep a backup of
> `m.db` (and let Engine DJ re-sync afterwards). The DB's own
> `trigger_PerformanceData_after_update_Track_timestamp` bumps
> `Track.lastEditTime`, which is how Engine DJ detects the edits.

---

## Architecture & extensibility

The code is split so new tools are easy to add:

| File           | Responsibility                                                                 |
|----------------|--------------------------------------------------------------------------------|
| `db/`          | **sqlc-generated data access**: `schema.sql` + `queries.sql` are the source of truth — run `make sqlc` (or `sqlc generate`) after editing them; never edit `models.go`/`queries.sql.go` by hand |
| `perfdata.go`  | Engine DJ v5 blob parsing/serialization (`quickCues`, `loops`, `trackData`) and the cue/loop fix computation |
| `library.go`   | Database access (`Library`, `TrackRecord`), path resolution, fix persistence, DB metadata sync |
| `mp3tags.go`   | ID3v2 read/write (`MediaTags`) via [bogem/id3v2/v2](https://pkg.go.dev/github.com/bogem/id3v2/v2) |
| `artwork.go`   | Cover-art download from MusicBrainz / Cover Art Archive / Discogs |
| `main.go`      | CLI entry: display mode and fix mode (same code paths as the GUI)               |
| `ui.go`        | shirei app shell: top bar, sidebar, shared track browser, **tool registry**      |
| `ui_cues.go`   | The *Cues & Loops* tool                                                          |
| `ui_tags.go`   | The *MP3 Tags* tool                                                              |
| `ui_config.go` | The *Settings* tool (config.yaml editor)                                         |

To add a tool, implement the `AppTool` interface (`Name`, `Icon`, `View(a *App)`)
in a new file and register it:

```go
func init() {
	RegisterTool(func() AppTool { return &MyTool{} })
}
```

It appears in the sidebar automatically and receives the shared `App` state
(library handle, filtered track list, selected track). `App.BrowserPanel`
provides the standard track list, so tools only implement their own panel.

---

## Engine DJ v5 binary format

The `PerformanceData` table (keyed by `trackId`, joining `Track.id`) stores
`BLOB` columns. Format verified against a real Engine DJ v5 database — it
**differs from the classic Engine Prime description** in the
[Mixxx wiki](https://github.com/mixxxdj/mixxx/wiki/Engine%20Library%20format):

| Blob         | Compression                         | Endianness of floats |
|--------------|-------------------------------------|----------------------|
| `quickCues`  | Qt `qCompress`: `u32 BE length` + zlib | **big-endian**    |
| `loops`      | none (raw)                          | little-endian        |
| `trackData`  | Qt `qCompress`                      | big-endian           |

### `quickCues`

```
u32 BE  uncompressed payload length        (qCompress header, before zlib data)
--- zlib-compressed payload ---
u64 BE  number of cues (always 8)
repeat 8 times:                             one frame per hot cue slot
  u8     label length (0 = empty slot)
  char[] label, no terminator (e.g. "Cue 1", "intro")
  f64 BE position in samples (-1 = no cue)
  u8[4]  RGBA (alpha first; empty slots are 00 00 00 00)
f64 BE  main cue position
u8      main cue overridden (0/1)
f64 BE  default auto-detected cue position
```

### `loops`

```
u8      number of loops (always 8)
u8[7]   padding (zeroes)
repeat 8 times:                             one frame per loop slot
  u8     label length (0 = empty slot)
  char[] label (e.g. "Loop 2", "intro")
  f64 LE start position in samples (-1 = not set)
  f64 LE end position in samples (-1 = not set)
  u8     start point set (0/1)
  u8     end point set (0/1)
  u8[4]  RGBA (alpha first)
```

No trailer. A loop counts as "set" when the start flag is 1 and
`start >= 0` (in practice `startSet == endSet` always holds).

### `trackData`

```
f64 BE  sample rate in Hz (44100 or 48000; NaN in some rows → fallback)
u64 BE  track length in samples
f64 BE  average loudness (often NaN in v5)
u32 BE  analysed key
```

### Timestamps

All positions are in **samples**; divide by the track's sample rate to get
seconds (the tool does this for display).

---

## Build

Requires Go (see `go.mod` for the minimum version). No cgo needed — the SQLite
driver is pure Go ([modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite))
and shirei uses purego for its macOS/Windows backends.

```sh
make build        # current platform -> ./enginedj5multitool
make check        # gofmt check + go vet + tests
make release      # cross-compile + package all platforms into dist/
make clean
```

`make release` produces, for `darwin/amd64`, `darwin/arm64`, `linux/amd64`,
`linux/arm64` and `windows/amd64`, an archive named
`enginedj5multitool-<version>-<os>-<arch>.tar.gz` (`.zip` for Windows)
containing the binary and this README. The version is injected from
`git describe` (override with `make release VERSION=v1.2.3`).

## Configuration

On first run the app creates `config.yaml` in the per-OS user config
directory (`~/Library/Application Support/enginedj5multitool/` on macOS,
`~/.config/enginedj5multitool/` on Linux, `%AppData%\enginedj5multitool` on
Windows — override with the `ENGINDJ5_CONFIG_DIR` env var):

```yaml
library: m.db            # default Engine DJ database path
music_root: ""           # optional root used to resolve relative track paths
theme: auto              # auto | light | dark
tool: 0                  # tool tab opened at startup
browser_width: 560       # track list width
discogs_token: ""        # personal access token for artwork search
window_width: 0          # main window width (saved on quit; 0 = default)
window_height: 0         # main window height (saved on quit; 0 = default)
```

Command-line flags override the file's values. The file is edited via the
**Settings** tool in the sidebar: edit the values and press *Save to
config.yaml* — they are applied to the running session and written to disk
(*Reload from file* re-reads it). Changes made elsewhere in the UI (theme
selector, splitter, library path, tag edits) are session-only and never
written to the config file — with one exception: the **window size** is saved
back to the config when the app quits.

## CI / Releases

- **CI** (`.github/workflows/ci.yml`): on every push to `main` and every PR —
  tests run on Ubuntu, macOS and Windows runners; gofmt is checked; all
  platforms are cross-compiled and uploaded as artifacts.
- **Release** (`.github/workflows/release.yml`): pushing a tag `v*` builds all
  platforms and creates a GitHub release with the archives. It can also be
  triggered manually via *workflow_dispatch* (artifacts only).

## Development

```sh
go test ./...   # unit tests: blob parsers, serializers, fix logic, tag round-trips, path resolution
go vet ./...
```

The tests embed real blobs extracted from an Engine DJ v5 database as fixtures
and verify parse → fix → serialize round-trips byte-exactly, plus ID3v2 tag
round-trips on temp files.
