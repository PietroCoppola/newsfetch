# newsfetch

A small CLI that prints one piece of bite-sized tech news every time you open a
terminal. Written in Go. Reads from Hacker News, with more sources planned;
biased toward the topics you tell it you care about. The default render is a
one-line boxed panel that takes a few hundred milliseconds and gets out of the
way.

## Install

Use whichever path matches what you already have installed. They land
the same binary; the differences are only in how it gets there.

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

To remove the shell hook later: `newsfetch --uninstall`.

To edit your config later (topics, style, sources): `newsfetch --settings`.

### Scripted install

`--init` skips the interactive wizard when stdin is not a TTY and reads JSON
instead. `topics` and `style` are required; `sources` is optional (omit it
to inherit the default):

```
echo '{"topics": ["rust", "ai"], "style": "boxed"}' | newsfetch --init
echo '{"topics": [], "style": "boxed", "sources": ["hackernews", "lobsters"]}' | newsfetch --init
```

Field rules:

- `topics` — array of strings, may be `[]`. Empty means no topic bias; the
  ranker scores stories by points and recency only.
- `style` — one of `boxed`, `minimal`, `json`.
- `sources` — array of strings drawn from the supported list (`hackernews`,
  `lobsters`). When provided, must be non-empty.

### Scripted edit

`--settings` is the equivalent of `--init` for changing your existing
config. All three fields (`topics`, `style`, `sources`) are required when
piping JSON:

```
echo '{"topics": ["rust"], "style": "minimal", "sources": ["hackernews"]}' | newsfetch --settings
```

Same field rules as `--init`'s scripted install — `topics = []` means no
topic bias, `sources` must be non-empty.

## Flags

```
--style=<mode>    boxed (default) | minimal | json
--topics=<list>   comma-separated; explicit empty defeats config
--init            interactive setup
--settings        edit existing config (topics, style, sources)
--uninstall       remove the shell hook
--version
--help
```

## Status

Pre-1.0; the CLI surface is stable enough to use day-to-day but config schema
and source list may change. Issues are turned off for now — this is a personal
project.
