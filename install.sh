#!/bin/sh
# install.sh — one-line installer for newsfetch on macOS and Linux.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/PietroCoppola/newsfetch/main/install.sh | sh
#
# What it does:
#   1. Detect OS (darwin/linux) and arch (x86_64/arm64) from uname.
#   2. Hit the GitHub /releases/latest API to find the current tag.
#   3. Download the matching archive + SHA256SUMS to a tempdir.
#   4. Verify the archive's checksum against SHA256SUMS — abort loudly
#      on mismatch (no fall-through that installs anyway).
#   5. Install the binary to /usr/local/bin, requiring sudo if the
#      directory isn't writable. Doesn't escalate on its own; tells the
#      user how to rerun with sudo.
#   6. Print the installed version.
#
# No telemetry, no curl flags that follow malicious redirects, no
# implicit sudo. Reads the script before piping to sh is fine and
# encouraged.

set -eu

REPO="PietroCoppola/newsfetch"
INSTALL_DIR="/usr/local/bin"
BINARY="newsfetch"

err() {
	printf 'install: %s\n' "$1" >&2
	exit 1
}

# --- Step 1: detect OS / arch -------------------------------------------------

UNAME_OS="$(uname -s)"
case "$UNAME_OS" in
	Darwin) OS="darwin" ;;
	Linux)  OS="linux"  ;;
	*) err "unsupported OS: $UNAME_OS (only darwin and linux have prebuilt binaries; build from source with 'go install github.com/PietroCoppola/newsfetch/cmd/newsfetch@latest')" ;;
esac

UNAME_ARCH="$(uname -m)"
case "$UNAME_ARCH" in
	x86_64|amd64)  ARCH="x86_64" ;;
	arm64|aarch64) ARCH="arm64"  ;;
	*) err "unsupported architecture: $UNAME_ARCH" ;;
esac

# --- Step 2: resolve latest release tag --------------------------------------

# /releases/latest returns a JSON object including "tag_name". We don't
# pull jq as a dependency — the field is well-formed enough that grep+sed
# is reliable for this single key. If the response shape ever changes,
# this is the line to revisit.
LATEST_URL="https://api.github.com/repos/${REPO}/releases/latest"
TAG="$(curl -fsSL "$LATEST_URL" | grep '"tag_name":' | head -n1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
if [ -z "$TAG" ]; then
	err "could not resolve latest release tag from $LATEST_URL"
fi

# Strip the leading 'v' from vX.Y.Z to match the archive name template.
VERSION="${TAG#v}"

# --- Step 3: download archive + checksums ------------------------------------

ARCHIVE="${BINARY}_${VERSION}_${OS}_${ARCH}.tar.gz"
ARCHIVE_URL="https://github.com/${REPO}/releases/download/${TAG}/${ARCHIVE}"
SUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/SHA256SUMS"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

printf 'install: downloading %s\n' "$ARCHIVE_URL"
curl -fsSL -o "$TMPDIR/$ARCHIVE" "$ARCHIVE_URL" \
	|| err "failed to download $ARCHIVE_URL"

printf 'install: downloading SHA256SUMS\n'
curl -fsSL -o "$TMPDIR/SHA256SUMS" "$SUMS_URL" \
	|| err "failed to download $SUMS_URL"

# --- Step 4: verify checksum -------------------------------------------------

# sha256sum on Linux, shasum -a 256 on macOS. Both produce the same
# "HASH  FILENAME" line format that SHA256SUMS already uses.
if command -v sha256sum >/dev/null 2>&1; then
	SHA_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	SHA_CMD="shasum -a 256"
else
	err "no sha256sum / shasum available; cannot verify checksum"
fi

EXPECTED="$(grep " ${ARCHIVE}\$" "$TMPDIR/SHA256SUMS" | awk '{print $1}')"
if [ -z "$EXPECTED" ]; then
	err "no checksum for $ARCHIVE found in SHA256SUMS — release is malformed, aborting"
fi

ACTUAL="$(cd "$TMPDIR" && $SHA_CMD "$ARCHIVE" | awk '{print $1}')"
if [ "$EXPECTED" != "$ACTUAL" ]; then
	err "checksum mismatch — possibly tampered or corrupted, aborting
   expected: $EXPECTED
   actual:   $ACTUAL"
fi
printf 'install: checksum OK\n'

# --- Step 5: extract + install -----------------------------------------------

tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR" "$BINARY" \
	|| err "failed to extract $BINARY from $ARCHIVE"
chmod +x "$TMPDIR/$BINARY"

if [ -w "$INSTALL_DIR" ]; then
	mv "$TMPDIR/$BINARY" "$INSTALL_DIR/$BINARY" \
		|| err "failed to install to $INSTALL_DIR"
else
	err "$INSTALL_DIR is not writable. Re-run with sudo:
   curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo sh"
fi

# --- Step 6: report ----------------------------------------------------------

printf 'install: installed %s %s to %s/%s\n' "$BINARY" "$TAG" "$INSTALL_DIR" "$BINARY"
"$INSTALL_DIR/$BINARY" --version
