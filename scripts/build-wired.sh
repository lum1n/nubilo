#!/usr/bin/env bash
# Build nubilo with wired relay support (-tags wired).
#
# wired's Go binding links a Rust static library that is not part of its Go
# module, so this builds it from a wired checkout and points a temporary
# go.work at it. Default builds (go build ./...) need neither.
#
#   WIRED_DIR=~/repos/wired scripts/build-wired.sh [out] [goos/goarch]
#
# The library is built from the wired commit go.mod names, in a temporary
# worktree of WIRED_DIR, whatever that checkout has checked out: nodes on
# different wired protocol versions cannot reach each other, so every
# nubilo binary must link the same wired. WIRED_REF overrides the commit.
#
# Cross builds need the matching Rust target and C toolchain, e.g.
#   rustup target add aarch64-unknown-linux-gnu  (linux/arm64)
#   rustup target add aarch64-apple-darwin       (darwin/arm64, on a Mac)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WIRED_DIR="${WIRED_DIR:-$ROOT/../wired}"
OUT="${1:-$ROOT/bin/nubilo}"
TARGET="${2:-$(go env GOOS)/$(go env GOARCH)}"
GOOS_T="${TARGET%/*}"
GOARCH_T="${TARGET#*/}"
if [[ ! -d "$WIRED_DIR/.git" && ! -f "$WIRED_DIR/.git" ]]; then
  echo "wired checkout not found at $WIRED_DIR (set WIRED_DIR)" >&2
  exit 2
fi

# The commit: the last 12 hex digits of a pseudo-version
# (v0.0.0-20261029063154-4750c849d521), else the version as a tag.
MOD_VERSION="$(awk '$1 == "github.com/lum1n/wired/bindings/go" { print $2 }' "$ROOT/go.mod")"
if [[ -n "${WIRED_REF:-}" ]]; then
  REF="$WIRED_REF"
elif [[ "$MOD_VERSION" =~ -([0-9a-f]{12})$ ]]; then
  REF="${BASH_REMATCH[1]}"
else
  REF="$MOD_VERSION"
fi
if ! git -C "$WIRED_DIR" rev-parse -q --verify "$REF^{commit}" >/dev/null; then
  git -C "$WIRED_DIR" fetch -q origin
fi
COMMIT="$(git -C "$WIRED_DIR" rev-parse --verify "$REF^{commit}")"

TMP="$(mktemp -d)"
SRC="$TMP/wired"
cleanup() {
  git -C "$WIRED_DIR" worktree remove --force "$SRC" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT
git -C "$WIRED_DIR" worktree add -q --detach "$SRC" "$COMMIT"
BIND="$SRC/bindings/go"
# Reuse compiled dependencies across builds.
export CARGO_TARGET_DIR="$WIRED_DIR/target/nubilo-build"

case "$TARGET" in
  linux/amd64) RT=x86_64-unknown-linux-gnu ;;
  linux/arm64) RT=aarch64-unknown-linux-gnu ;;
  darwin/arm64) RT=aarch64-apple-darwin ;;
  darwin/amd64) RT=x86_64-apple-darwin ;;
  *) echo "unsupported target $TARGET" >&2; exit 2 ;;
esac
if [[ "$TARGET" == "$(go env GOOS)/$(go env GOARCH)" ]]; then
  cargo build -q --manifest-path "$SRC/Cargo.toml" -p wired-ffi --release
  BUILT="$CARGO_TARGET_DIR/release/libwired_ffi.a"
else
  cargo build -q --manifest-path "$SRC/Cargo.toml" -p wired-ffi --release --target "$RT"
  BUILT="$CARGO_TARGET_DIR/$RT/release/libwired_ffi.a"
fi
LIB="$BIND/lib/${GOOS_T}_${GOARCH_T}/libwired_ffi.a"
mkdir -p "$(dirname "$LIB")"
cp "$BUILT" "$LIB"

WORK="$TMP/go.work"
printf 'go 1.26.0\n\nuse (\n\t%s\n\t%s\n)\n' "$ROOT" "$BIND" > "$WORK"
mkdir -p "$(dirname "$OUT")"
# The wire protocol: nodes on different ones cannot reach each other, and the
# updater won't install a release that changes it behind the relay's back.
PROTOCOL="$(sed -n 's/^pub const PROTOCOL_VERSION: u8 = \([0-9]*\);/\1/p' "$SRC/crates/wired-core/src/proto.rs")"
LDFLAGS="-X nubilo/internal/wiredx.Commit=$COMMIT -X nubilo/internal/wiredx.Protocol=$PROTOCOL"
# NUBILO_VERSION (release builds) sets the version the updater compares.
if [[ -n "${NUBILO_VERSION:-}" ]]; then
  LDFLAGS="$LDFLAGS -X nubilo/internal/version.String=$NUBILO_VERSION"
fi
# Extra flags for test builds (e.g. the updater's test key and release URL).
LDFLAGS="$LDFLAGS ${NUBILO_LDFLAGS:-}"
GOWORK="$WORK" CGO_ENABLED=1 GOOS="$GOOS_T" GOARCH="$GOARCH_T" go build -tags wired -trimpath \
  -ldflags "$LDFLAGS" -o "$OUT" "$ROOT/cmd/nubilo"
echo "built $OUT ($TARGET, wired ${COMMIT:0:12}, protocol $PROTOCOL)"
"$OUT" version 2>/dev/null || true
