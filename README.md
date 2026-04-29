# newsfetch

A small CLI that prints one piece of bite-sized tech news every time you open a
terminal. Written in Go. Reads from Hacker News by default, with optional
Lobste.rs and more sources planned; biased toward the topics you tell it you
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

- `newsfetch --settings` — edit your config later (topics, style, sources, count, ticker).
- `newsfetch --uninstall` — remove the shell hook.

## Flags

```
Per-render overrides (apply to this invocation only; config is
untouched):
  --style=<mode>    display mode for this render: boxed (default) | minimal | json
  --topics=<list>   topic bias for this render, comma-separated; '--topics=' defeats config
  --count=<n>       number of stories this render: 1..4 (default 1)

Subcommands:
  --init            interactive setup
  --settings        edit existing config (topics, style, sources, count, ticker)
  --uninstall       remove the shell hook

  --version
  --help
```

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
| `sources` | `[string]` | `["hackernews"]` | Where to fetch from. Drawn from the supported list (`hackernews`, `lobsters`). When set, must be non-empty. |
| `count` | `int` | `1` | Stories rendered per invocation. Range `1..4`. Out-of-range values are clamped with a one-line warning at next render. |
| `ticker_marker` | `string` | `"dot"` | Symbol prefixing each non-hero story when more than one renders. One of `dot`, `arrow`, `branch`. Visible only when `style = "boxed"` and `count > 1`. |
| `ticker_boxed` | `bool` | `false` | `true` wraps hero plus ticker in one outer box; `false` gives the hero its own box with ticker lines beneath. Same visibility rule as `ticker_marker`. |
| `cache_ttl_minutes` | `int` | `30` | Stale-while-revalidate window for the story cache. Floor of 5 minutes. |
| `dedup_ttl_hours` | `int` | `6` | Window during which a rendered story is filtered out of the candidate pool. After the window passes, the story ages back in and can re-appear. Set to `0` to disable dedup entirely. |
| `min_points` | `int` | `50` | Source-advisory floor on candidate points. Honoured by sources that have a comparable signal (HN); ignored by others (Lobste.rs). |

`ticker_marker` and `ticker_boxed` are persisted unconditionally even
when currently inert — switching `style = boxed` → `minimal` and back
keeps prior tuning intact instead of reverting to defaults.

### Scripted install (--init via JSON)

`--init` skips the interactive wizard when stdin is not a TTY and reads JSON
instead. `topics` and `style` are required; everything else is optional and
falls back to the compile-time default.

```
echo '{"topics": ["rust", "ai"], "style": "boxed"}' | newsfetch --init
echo '{"topics": [], "style": "boxed", "sources": ["hackernews", "lobsters"]}' | newsfetch --init
echo '{"topics": ["rust"], "style": "boxed", "count": 3, "ticker_marker": "branch"}' | newsfetch --init
```

Field validation matches the [config reference](#config-reference).
Unknown JSON fields are rejected.

### Scripted edit (--settings via JSON)

`--settings` is the equivalent of `--init` for changing your existing
config. `topics`, `style`, `sources`, and `count` are required;
`ticker_marker` and `ticker_boxed` are optional and preserve current
config values when omitted (matches the wizard's hide-when-inert
behaviour, so toggling `style = boxed` → `minimal` and back through
scripted edits doesn't silently lose ticker tuning).

```
echo '{"topics": ["rust"], "style": "minimal", "sources": ["hackernews"], "count": 1}' | newsfetch --settings
echo '{"topics": ["rust"], "style": "boxed", "sources": ["hackernews"], "count": 3, "ticker_marker": "branch", "ticker_boxed": false}' | newsfetch --settings
```

## Status

Pre-1.0; the CLI surface is stable enough to use day-to-day but config schema
and source list may change. Issues are turned off for now — this is a personal
project.
