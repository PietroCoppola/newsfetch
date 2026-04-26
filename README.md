# newsfetch

A small CLI that prints one piece of bite-sized tech news every time you open a
terminal. Written in Go. Reads from Hacker News, with more sources planned;
biased toward the topics you tell it you care about. The default render is a
one-line boxed panel that takes a few hundred milliseconds and gets out of the
way.

## Install

```
go install github.com/PietroCoppola/newsfetch/cmd/newsfetch@latest
```

The binary lands in `$GOBIN` (or `$HOME/go/bin`). Make sure that's on your
`$PATH`.

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

Style must be one of `boxed`, `minimal`, `json`. `topics` may be `[]`.

### Scripted edit

`--settings` is the equivalent of `--init` for changing your existing
config. All three fields (`topics`, `style`, `sources`) are required when
piping JSON; `sources` must be non-empty:

```
echo '{"topics": ["rust"], "style": "minimal", "sources": ["hackernews"]}' | newsfetch --settings
```

## Flags

```
--style=<mode>    boxed (default) | minimal | json
--topics=<list>   comma-separated; explicit empty defeats config
--init            interactive setup
--uninstall       remove the shell hook
--version
--help
```

## Status

Pre-1.0; the CLI surface is stable enough to use day-to-day but config schema
and source list may change. Issues are turned off for now — this is a personal
project.
