#!/usr/bin/env bash
# Build nubilo with wired relay support (-tags wired).
#
# wired's Go binding links a Rust static library that is not part of its Go
# module, so this builds it from a wired checkout and points a temporary
# go.work at that checkout. Default builds (go build ./...) need neither.
#
#   WIRED_DIR=~/repos/wired scripts/build-wired.sh [out] [goos/goarch]
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
BIND="$WIRED_DIR/bindings/go"
if [[ ! -d "$BIND" ]]; then
  echo "wired checkout not found at $WIRED_DIR (set WIRED_DIR)" >&2
  exit 2
fi
LIB="$BIND/lib/${GOOS_T}_${GOARCH_T}/libwired_ffi.a"
if [[ "$TARGET" == "$(go env GOOS)/$(go env GOARCH)" ]]; then
  "$BIND/scripts/build.sh"
else
  case "$TARGET" in
    linux/amd64) RT=x86_64-unknown-linux-gnu ;;
    linux/arm64) RT=aarch64-unknown-linux-gnu ;;
    darwin/arm64) RT=aarch64-apple-darwin ;;
    darwin/amd64) RT=x86_64-apple-darwin ;;
    *) echo "unsupported target $TARGET" >&2; exit 2 ;;
  esac
  cargo build --manifest-path "$WIRED_DIR/Cargo.toml" -p wired-ffi --release --target "$RT"
  mkdir -p "$(dirname "$LIB")"
  cp "$WIRED_DIR/target/$RT/release/libwired_ffi.a" "$LIB"
fi
WORK="$(mktemp -d)/go.work"
trap 'rm -rf "$(dirname "$WORK")"' EXIT
printf 'go 1.26.0\n\nuse (\n\t%s\n\t%s\n)\n' "$ROOT" "$BIND" > "$WORK"
mkdir -p "$(dirname "$OUT")"
GOWORK="$WORK" CGO_ENABLED=1 GOOS="$GOOS_T" GOARCH="$GOARCH_T" go build -tags wired -trimpath -o "$OUT" "$ROOT/cmd/nubilo"
echo "built $OUT ($TARGET, wired)"
"$OUT" version 2>/dev/null || true
