#!/usr/bin/env bash
# VMT-23 cross-platform release builder.
#
# The default mode builds one target and embeds the already-exported frontend.
# `./build.sh --release` builds and packages all supported desktop/server targets.
#
# Environment variables:
#   VMT23_TARGET_OS=linux             One target OS in single-target mode.
#   VMT23_TARGET_ARCH=amd64           One target arch in single-target mode.
#   VMT23_TARGETS=linux/amd64,...     Comma-separated targets for multi-target mode.
#   VMT23_BUILD_VERSION=v0.3.3        Version embedded in the binary and archive name.
#   VMT23_OUTPUT=/path/to/vmt-23       Explicit binary path in single-target mode.
#   VMT23_OUTPUT_DIR=dist             Directory for default binary paths.
#   VMT23_PACKAGE=1                   Create a zip archive for each target.
#   VMT23_PACKAGE_DIR=dist            Directory for release archives.
#   VMT23_COMPRESS=off                UPX mode: off, auto, or required.
#   VMT23_UPX_ARGS="--best --lzma"    Arguments passed to UPX.
#   VMT23_SKIP_FRONTEND=1             Reuse server/webui/dist (for CI artifact builds).
#   VMT23_SKIP_NPM_CI=1               Skip npm ci while rebuilding the frontend.
#   VMT23_GOSUMDB=sum.golang.org      Go checksum database.
set -euo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok() { printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage:
  ./build.sh                         Build for the current host OS and architecture
  ./build.sh --target linux/amd64   build for one specified target
  ./build.sh --release               build and package all supported targets

Options:
  --release              Build amd64/arm64 targets for Linux, macOS, and Windows and create a ZIP
  --target OS/ARCH       Set a single target, for example windows/amd64
  --upx                  Force UPX compression of the binary (may affect compatibility in some Linux environments)
  --no-compress          Do not use UPX; use only the Go linker to strip and compress the ZIP
  --help                 Show help

Override the multi-target list with VMT23_TARGETS, for example:
  VMT23_TARGETS=linux/amd64,windows/amd64 ./build.sh --release
EOF
}

RELEASE_TARGETS_DEFAULT="linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64"
VMT23_RELEASE="${VMT23_RELEASE:-0}"
VMT23_COMPRESS="${VMT23_COMPRESS:-off}"
VMT23_PACKAGE="${VMT23_PACKAGE:-0}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --release)
      VMT23_RELEASE=1
      VMT23_PACKAGE=1
      shift
      ;;
    --target)
      [ "$#" -ge 2 ] || die "--target requires an OS/ARCH argument"
      target_arg="$2"
      case "$target_arg" in
        */*)
          VMT23_TARGET_OS="${target_arg%%/*}"
          VMT23_TARGET_ARCH="${target_arg##*/}"
          VMT23_TARGETS="$target_arg"
          ;;
        *) die "Target must be OS/ARCH, for example linux/amd64" ;;
      esac
      shift 2
      ;;
    --no-compress)
      VMT23_COMPRESS=0
      shift
      ;;
    --upx)
      VMT23_COMPRESS=required
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *) die "Unknown argument: $1 (run --help for usage)" ;;
  esac
done

command -v go >/dev/null 2>&1 || die "Go not found (the project requires Go 1.26 or later)"

VMT23_GOSUMDB="${VMT23_GOSUMDB:-sum.golang.org}"
if [ -z "${VMT23_BUILD_VERSION:-}" ]; then
  if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    VMT23_BUILD_VERSION="$(git describe --tags --always --dirty)"
  else
    VMT23_BUILD_VERSION="dev"
  fi
fi
# Release tags are commonly passed as v0.3.3; keep the binary version consistent.
VMT23_BUILD_VERSION="${VMT23_BUILD_VERSION#v}"
VMT23_OUTPUT_DIR="${VMT23_OUTPUT_DIR:-dist}"
VMT23_PACKAGE_DIR="${VMT23_PACKAGE_DIR:-$VMT23_OUTPUT_DIR}"
VMT23_UPX_ARGS="${VMT23_UPX_ARGS:---best --lzma}"

if [ "${VMT23_RELEASE}" = "1" ]; then
  VMT23_TARGETS="${VMT23_TARGETS:-$RELEASE_TARGETS_DEFAULT}"
else
  VMT23_TARGET_OS="${VMT23_TARGET_OS:-$(GOSUMDB="$VMT23_GOSUMDB" go env GOOS)}"
  VMT23_TARGET_ARCH="${VMT23_TARGET_ARCH:-$(GOSUMDB="$VMT23_GOSUMDB" go env GOARCH)}"
  VMT23_TARGETS="${VMT23_TARGETS:-${VMT23_TARGET_OS}/${VMT23_TARGET_ARCH}}"
fi

if [ "${VMT23_SKIP_FRONTEND:-0}" = "1" ]; then
  [ -d server/webui/dist ] || die "VMT23_SKIP_FRONTEND=1 but server/webui/dist does not exist"
else
  command -v npm >/dev/null 2>&1 || die "npm not found (Node.js/npm is required to build the frontend static assets)"
  command -v rsync >/dev/null 2>&1 || die "rsync not found"
  info "Building frontend static assets"
  if [ "${VMT23_SKIP_NPM_CI:-0}" != "1" ]; then
    (cd web && npm ci)
  fi
  (cd web && npm run build:static)
  info "Syncing frontend assets to server/webui/dist"
  mkdir -p server/webui/dist
  rsync -a --delete web/out/ server/webui/dist/
fi

compress_binary() {
  binary="$1"
  goos="$2"
  case "$VMT23_COMPRESS" in
    0|off|false|none)
      info "Skipping UPX:$binary"
      return 0
      ;;
    auto|required|true|1) ;;
    *) die "VMT23_COMPRESS must be off, auto, or required" ;;
  esac

  if ! command -v upx >/dev/null 2>&1; then
    if [ "$VMT23_COMPRESS" = "required" ]; then
      die "VMT23_COMPRESS=required but UPX was not found"
    fi
    warn "UPX not found; keeping the linker-compressed result:$binary"
    return 0
  fi

  before=$(wc -c < "$binary" | tr -d ' ')
  upx_args="$VMT23_UPX_ARGS"
  [ "$goos" = "darwin" ] && upx_args="$upx_args --force-macos"
  # shellcheck disable=SC2086
  if ! upx $upx_args -- "$binary"; then
    if [ "$VMT23_COMPRESS" = "required" ]; then
      die "UPX compression failed:$binary"
    fi
    warn "UPX does not support this target format; keeping the uncompressed binary:$binary"
    return 0
  fi
  after=$(wc -c < "$binary" | tr -d ' ')
  ok "UPX compression complete:$binary (${before} -> ${after} bytes)"
}

package_binary() {
  binary="$1"
  goos="$2"
  goarch="$3"
  package_name="vmt-23-${VMT23_BUILD_VERSION}-${goos}-${goarch}"
  package_root="${VMT23_PACKAGE_DIR}/${package_name}"
  archive="${VMT23_PACKAGE_DIR}/${package_name}.zip"

  command -v zip >/dev/null 2>&1 || die "zip is required for packaging"
  rm -rf "$package_root" "$archive"
  mkdir -p "$package_root"
  cp "$binary" "$package_root/"
  # The supervisor startup script is the production entry point: one-click updates in the UI rely on it to relaunch the process after it exits,
  # otherwise vmt-23 itself will not restart after an update. Include only the script for the target operating system.
  if [ "$goos" = "windows" ]; then
    cp start.bat "$package_root/"
  else
    cp start.sh "$package_root/"
    chmod +x "$package_root/start.sh"
  fi
  cp -R skills "$package_root/"
  cp config.example.json "$package_root/"
  if [ -f README.md ]; then cp README.md "$package_root/"; fi
  (cd "$VMT23_PACKAGE_DIR" && zip -q -r -9 "$(basename "$archive")" "$(basename "$package_root")")
  rm -rf "$package_root"
  ok "Release archive:$archive"
}

build_target() {
  target="$1"
  case "$target" in
    */*) ;;
    *) die "Invalid target:$target (must be OS/ARCH)" ;;
  esac
  goos="${target%%/*}"
  goarch="${target##*/}"
  case "$goos" in
    linux|darwin|windows) ;;
    *) die "Unsupported OS:$goos (supported values are linux, darwin, and windows)" ;;
  esac

  binary_name="vmt-23"
  [ "$goos" = "windows" ] && binary_name="vmt-23.exe"
  if [ -n "${VMT23_OUTPUT:-}" ] && [ "$VMT23_RELEASE" != "1" ]; then
    output="$VMT23_OUTPUT"
  else
    output="${VMT23_OUTPUT_DIR}/vmt-23-${goos}-${goarch}/${binary_name}"
  fi
  mkdir -p "$(dirname "$output")"

  info "Building ${goos}/${goarch}，version ${VMT23_BUILD_VERSION}"
  GOSUMDB="$VMT23_GOSUMDB" \
  CGO_ENABLED=0 \
  GOOS="$goos" \
  GOARCH="$goarch" \
  go build \
    -tags embedui \
    -trimpath \
    -ldflags "-s -w -buildid= -X main.version=${VMT23_BUILD_VERSION}" \
    -o "$output" \
    ./cmd/vmt-23

  compress_binary "$output" "$goos"
  if command -v file >/dev/null 2>&1; then file "$output"; fi
  if [ "$VMT23_PACKAGE" = "1" ]; then package_binary "$output" "$goos" "$goarch"; fi
  ok "Build complete:$output"
}

write_checksums() {
  [ "$VMT23_PACKAGE" = "1" ] || return 0
  checksum_file="$VMT23_PACKAGE_DIR/SHA256SUMS"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$VMT23_PACKAGE_DIR" && for archive in *.zip; do sha256sum "$archive"; done > "$(basename "$checksum_file")")
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$VMT23_PACKAGE_DIR" && for archive in *.zip; do shasum -a 256 "$archive"; done > "$(basename "$checksum_file")")
  else
    warn "Neither sha256sum nor shasum found; skipping SHA256SUMS"
    return 0
  fi
  ok "Checksum file:$checksum_file"
}

mkdir -p "$VMT23_OUTPUT_DIR"
if [ "$VMT23_PACKAGE" = "1" ]; then mkdir -p "$VMT23_PACKAGE_DIR"; fi

old_ifs="$IFS"
IFS=','
read -r -a targets <<< "$VMT23_TARGETS"
IFS="$old_ifs"
[ "${#targets[@]}" -gt 0 ] || die "VMT23_TARGETS must not be empty"
for target in "${targets[@]}"; do
  target="${target//[[:space:]]/}"
  [ -n "$target" ] || continue
  build_target "$target"
done

if [ "$VMT23_PACKAGE" = "1" ]; then
  write_checksums
  info "Release package created at:$VMT23_PACKAGE_DIR"
fi
