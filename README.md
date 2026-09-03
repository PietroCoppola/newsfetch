# newsfetch

A small CLI that prints one piece of bite-sized tech news every time you open a
terminal. Written in Go. Reads from Hacker News by default, with optional
Lobste.rs and your own RSS/Atom feeds; biased toward the topics you tell it you
care about. The default render is a one-line boxed panel that takes a few
hundred milliseconds and gets out of the way. No telemetry — outbound HTTP
requests go only to your configured news sources, never anywhere else.

## Install

### Easy install (macOS, Linux) — no Go required

```
curl -fsSL https://raw.githubusercontent.com/PietroCoppola/newsfetch/main/install.sh | sh
```

Detects your OS and architecture, downloads the matching binary from
the [latest release](https://github.com/PietroCoppola/newsfetch/releases/latest),
verifies its SHA-256 against the published checksum, and installs to
`/usr/local/bin`. Read the
[script](https://github.com/PietroCoppola/newsfetch/blob/main/install.sh)
before piping to `sh` if you'd rather know what it does.

### Homebrew (macOS, Linux)

```
brew install PietroCoppola/tap/newsfetch
```

### Manual binary download

Grab the appropriate archive from the
[latest release](https://github.com/PietroCoppola/newsfetch/releases/latest),
verify the checksum against `SHA256SUMS`, and move the extracted
binary to a directory on your `$PATH`.

### From source (requires Go 1.25+)

```
go install github.com/PietroCoppola/newsfetch/cmd/newsfetch@latest
```

The binary lands in `$GOBIN` (or `$HOME/go/bin` if `$GOBIN` is unset).
Verify that directory is on your `$PATH`; if not, add it to your shell
rc file.

## Quickstart

```
newsfetch --init
```

Walks you through picking topics and a display style, writes the config to
`~/.config/newsfetch/config.toml`, and patches your shell's rc file (zsh,
bash, or fish) so a story renders on each new terminal.

- `newsfetch --settings` — edit your config later (topics, style, pools, feeds, counts, ticker).
- `newsfetch --uninstall` — remove the shell hook, config, and caches (see [below](#uninstall)).

## Flags

```
Per-render overrides (apply to this invocation only; config is
untouched):
  --style=<mode>    display mode for this render: boxed (default) | minimal | json | statusline
  --topics=<list>   topic bias for this render, comma-separated; '--topics=' defeats config
  --count=<n>       number of stories this render from the news pool: 1..4 (default 1)
  --pin=<key>       statusline style: pin story selection to this key so
                    repeated renders stay stable; default reads prompt_id
                    (fallback session_id) from JSON on stdin
  --max-width=<n>   statusline style: truncate to n display columns
                    (default 80; detected terminal width when stdout is a TTY)

Subcommands:
  --init            interactive setup
  --settings        edit existing config (topics, style, pools, feeds, counts, ticker)
  --uninstall       remove the shell hook, config, and caches — see below

  --version
  --help
```

## Uninstall

`newsfetch --uninstall` always removes the shell rc block first. Safe to
re-run — a missing rc file or an already-removed block prints a one-line
"nothing to remove" and stops there.

What happens to the files it created next depends on whether stdin is a
terminal:

- **Interactive** (stdin is a real terminal): asks once per group, and
  only for a group that has something on disk to remove.
  - **config** — `config.toml`
  - **caches** — `feed.json`, `following.json`, `refresh.log`; all
    rebuildable by one fetch
  - **state** — `seen.json` (dedup history), `sessions.json` (statusline
    session pins), `feeds.json` (up to four weeks of following-feed
    cadence and HTTP-conditional-GET data) — offered only in this mode

  Declining a group leaves its files in place and prints their paths.
- **Piped** (stdin is not a TTY — a script, or any non-interactive
  invocation): removes config and caches **without asking**. It
  deliberately never removes, and never even asks about, state — that
  group is left off the roster entirely — and instead prints the
  directory it was kept in.

No `--uninstall` removes a `.lock` file, in either mode: `refresh.lock`,
`seen.lock`, `sessions.lock` and `feeds.lock` are how concurrent newsfetch
processes stay out of each other's way, and a lock file's path is its
identity — unlink one and a second process can create and lock a fresh
file at the same name while the first still holds the original. They are
zero bytes, and the last line of an uninstall names the ones it left.

The asymmetry is intentional: config is a minute of retyping and caches
rebuild themselves on the next fetch, so a piped run is allowed to clear
them on its own judgment. State can't be rebuilt — it has to be re-earned
in real time — so only a human answering a prompt can remove it.

## Notes

- **Local cache and dedup.** Repeat terminal opens render from a local
  cache; rendered stories are tracked so the same headline doesn't keep
  cycling. Both windows are tunable — see `cache_ttl_minutes` and
  `dedup_ttl_hours` in the [config reference](#config-reference).
- **No telemetry, ever.** The binary makes outbound HTTP requests only to
  the configured news sources. Nothing about you or your usage is
  collected, transmitted, or logged anywhere outside your machine.
- **Unix only.** macOS and Linux are supported; native Windows isn't
  planned but isn't ruled out. WSL works fine in the meantime.
- **Config** lives at `~/.config/newsfetch/config.toml` (or
  `$XDG_CONFIG_HOME/newsfetch/config.toml`).
- **MIT licensed** — see `LICENSE`.

## Power user

### Config reference

| Field | Type | Default | Description |
|---|---|---|---|
| `topics` | `[string]` | `[]` | Bias the ranker toward these topics. Empty means no bias; ranker uses points and recency only. |
| `style` | `string` | `"boxed"` | Render mode. One of `boxed`, `minimal`, `json`. |
| `pools` | `[string]` | `["news"]` | Which pools are enabled. A pool is an independent fetch→rank→render unit, stacked vertically — pools never rank against each other. Supported: `news`, `following`. Empty is clamped to the default with a one-line warning. |
| `pool_order` | `[string]` | `["news"]` | Vertical stacking order. Defaults to the enabled pools' own names, not the compile-time order, so validation stays a no-op on `Defaults()`; once two pools are enabled with no explicit `pool_order` set, the compile-time order `["following", "news"]` applies. Any enabled pool missing from the list is appended in default order. Only written to config when two or more pools are enabled. |
| `news.aggregators` | `[string]` | `["hackernews"]` | Where the news pool fetches from. Drawn from the supported list (`hackernews`, `lobsters`). Empty is allowed as long as another enabled pool still has content; if it would leave every enabled pool empty (e.g. `news` is the only enabled pool, the shipped default), validation resets both `pools` and this list to their defaults and warns, rather than rendering nothing. Lives under a `[news]` table. |
| `following.feeds` | `[table]` | `[]` | RSS/Atom feeds for the following pool, one `[[following.feeds]]` table each. Keys: `url` (required, `http`/`https`), `max_items` (`1..10`, default `3`), `weight` (`(0.0, 5.0]`, replaces the automatic cadence weight). Removing `following` from `pools` leaves these blocks untouched. A `url` listed more than once is dropped back to a single entry with a warning, so one feed is never fetched or rendered twice; the JSON surfaces reject the repeat outright. |
| `count` | `int` | `1` | Stories rendered per invocation from the **news** pool. Range `1..4`. Out-of-range values are clamped with a one-line warning at next render. |
| `following_count` | `int` | `1` | Same, for the **following** pool. Range `1..4`. No CLI flag — `--count` is the news pool's knob. |
| `ticker_marker` | `string` | `"dot"` | Symbol prefixing each non-hero story when more than one renders. One of `dot`, `arrow`, `branch`. Visible only when `style = "boxed"` and `count > 1`. |
| `ticker_boxed` | `bool` | `false` | `true` wraps hero plus ticker in one outer box; `false` gives the hero its own box with ticker lines beneath. Same visibility rule as `ticker_marker`. |
| `cache_ttl_minutes` | `int` | `30` | Stale-while-revalidate window for the story cache. Floor of 5 minutes. |
| `dedup_ttl_hours` | `int` | `6` | Window during which a rendered story is filtered out of the candidate pool. After the window passes, the story ages back in and can re-appear. Set to `0` to disable dedup entirely. |
| `min_points` | `int` | `50` | Source-advisory floor on candidate points. Honoured by sources that have a comparable signal (HN); ignored by others (Lobste.rs). |

`ticker_marker` and `ticker_boxed` are persisted unconditionally even
when currently inert — switching `style = boxed` → `minimal` and back
keeps prior tuning intact instead of reverting to defaults.

**Deprecated: the top-level `sources` key.** Configs written before v0.7.0
used a top-level `sources` list instead of pools. It is still read, and
aliased to `pools = ["news"]` plus the `[news] aggregators` list, so
existing configs keep working untouched — the read path that runs on every
render never rewrites your config file, alias included.

`newsfetch --settings` does rewrite it: saving regenerates the whole file
from scratch rather than patching it in place. Every *documented* setting
you don't touch in that edit survives unchanged — including the three
advanced, wizard-hidden keys (`cache_ttl_minutes`, `min_points`,
`dedup_ttl_hours`) and a feed's `max_items`/`weight`, which the wizard never
shows either. What a settings save does **not** preserve: comments,
blank-line grouping, key ordering, and any key newsfetch doesn't recognize
— those come from a machine round-trip through the TOML encoder, which has
no notion of any of the four. Run `newsfetch --settings` when you want the
new keys written out; that save is also what retires `sources` for good,
since the regenerated file speaks `pools` / `[news] aggregators` from then
on. If both spellings are present and `pools` is absent, `[news]
aggregators` wins and `sources` is ignored.

Note that the JSON wizard input below does **not** accept the old key —
unknown fields are rejected by name. The asymmetry is deliberate: the TOML
alias protects config files that exist on your disk right now, while a
JSON alias would only protect scripts that do not exist.

### Scripted install (--init via JSON)

`--init` skips the interactive wizard when stdin is not a TTY and reads JSON
instead. `topics` and `style` are required; everything else is optional and
falls back to the compile-time default.

```
echo '{"topics": ["rust", "ai"], "style": "boxed"}' | newsfetch --init
echo '{"topics": [], "style": "boxed", "news": {"aggregators": ["hackernews", "lobsters"]}}' | newsfetch --init
echo '{"topics": ["rust"], "style": "boxed", "count": 3, "ticker_marker": "branch"}' | newsfetch --init
echo '{"topics": [], "style": "boxed", "pools": ["news", "following"], "following": {"feeds": [{"url": "https://drewdevault.com/blog/index.xml"}]}}' | newsfetch --init
```

Field validation matches the [config reference](#config-reference).
Unknown JSON fields are rejected.

### Scripted edit (--settings via JSON)

`--settings` is the equivalent of `--init` for changing your existing
config. `topics`, `style`, `pools`, and `count` are required. Pool
internals (`news.aggregators`, `following.feeds`) are optional and fall
back to your current config when omitted, so a scripted edit that only
changes the style cannot silently wipe your feed list. `following_count`,
`pool_order`, `ticker_marker` and `ticker_boxed` are likewise optional and
preserve current values (matching the wizard's hide-when-inert behaviour,
so toggling `style = boxed` → `minimal` and back through scripted edits
doesn't lose ticker tuning).

```
echo '{"topics": ["rust"], "style": "minimal", "pools": ["news"], "count": 1}' | newsfetch --settings
echo '{"topics": ["rust"], "style": "boxed", "pools": ["news"], "count": 3, "ticker_marker": "branch", "ticker_boxed": false}' | newsfetch --settings
echo '{"topics": [], "style": "boxed", "pools": ["news", "following"], "pool_order": ["following", "news"], "count": 1, "following_count": 2, "following": {"feeds": [{"url": "https://blog.cloudflare.com/rss/", "max_items": 2}]}}' | newsfetch --settings
```

### JSON output

`--style=json` emits a top-level array, always, with one object per
rendered story and a `pool` field on each naming the pool it came from:

```
[{"pool":"news","title":"React 21 drops...","url":"https://...","source":"hackernews","age_seconds":7200,"tags":["frontend"]}]
```

**Changed in v0.7.0:** earlier versions emitted a bare object when exactly
one story rendered, and an array with no `pool` field above that. The
shape is uniform now. If you indexed the bare object, unwrap `[0]`.

### Claude Code statusline

`--style=statusline` emits one line: the story title, OSC 8-hyperlinked to
the story URL and underlined, then a dim `· host · age · by author` tail —
the `by` segment only when the story has an author. The tail is never
linked or underlined. No box.

The line truncates to `--max-width` display columns (default 80; detected
terminal width when stdout is a TTY, which a statusline invocation never
is). The tail is charged against the budget first and the title takes what
is left, so a squeeze shrinks the headline and keeps the metadata; at
widths too narrow for both, the tail drops.

Piping the Claude Code statusline JSON into it pins the story per user
message (`prompt_id`), so the headline doesn't flicker as the statusline
re-renders — it advances when you send a message.

Add it to a statusline script:

```
input=$(cat)
news=$(printf '%s' "$input" | newsfetch --style=statusline 2>/dev/null)
[ -n "$news" ] && printf '%s\n' "$news"
```

Cache miss renders nothing (never blocks on the network) and warms the
cache for the next render. Statusline renders share the regular history
dedup, so your terminal-open story and your statusline story don't repeat
each other within the dedup window.

## Status

Pre-1.0; the CLI surface is stable enough to use day-to-day but config schema
and source list may change. Issues are turned off for now — this is a personal
project.
