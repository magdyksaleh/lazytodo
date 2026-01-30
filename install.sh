#!/usr/bin/env bash
set -euo pipefail

REPO_OWNER="magdyksaleh"
REPO_NAME="lazytodo"
BINARY_NAME="lazytodo"
DEFAULT_INSTALL_DIR="/usr/local/bin"
API_ROOT="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}"
DOWNLOAD_ROOT="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases"

usage() {
  cat <<'USAGE'
Install the latest lazytodo release.

Usage: install.sh [--version <tag>] [--install-dir <path>]

Options:
  -v, --version <tag>     Install a specific release tag (defaults to latest)
  -p, --install-dir <dir> Install into the provided directory
  -h, --help              Show this help message

Environment variables:
  LAZYTODO_VERSION        Same as --version
  LAZYTODO_INSTALL_DIR    Same as --install-dir
USAGE
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

log() {
  echo "[lazytodo] $*"
}

die() {
  echo "[lazytodo] $*" >&2
  exit 1
}

require_commands() {
  for cmd in "$@"; do
    command_exists "$cmd" || die "Required command '$cmd' not found"
  done
}

parse_args() {
  VERSION="${LAZYTODO_VERSION:-latest}"
  INSTALL_DIR="${LAZYTODO_INSTALL_DIR:-}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -v|--version)
        [[ $# -ge 2 ]] || die "Missing value for $1"
        VERSION="$2"
        shift 2
        ;;
      -p|--install-dir|--prefix)
        [[ $# -ge 2 ]] || die "Missing value for $1"
        INSTALL_DIR="$2"
        shift 2
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        die "Unknown option: $1"
        ;;
    esac
  done
}

canonicalize_version() {
  if [[ "$VERSION" == "latest" ]]; then
    VERSION=$(curl -fsSL "${API_ROOT}/releases/latest" | awk -F '"' '/"tag_name"/ {print $4; exit}')
    [[ -n "$VERSION" ]] || die "Unable to determine latest release"
  fi
  if [[ "$VERSION" != v* ]]; then
    VERSION="v${VERSION}"
  fi
}

detect_platform() {
  local os arch
  os=$(uname -s)
  case "$os" in
    Linux) OS="linux" ;;
    Darwin) OS="darwin" ;;
    *) die "Unsupported OS: $os" ;;
  esac
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) die "Unsupported architecture: $arch" ;;
  esac
}

resolve_install_dir() {
  if [[ -z "${INSTALL_DIR:-}" ]]; then
    if [[ -w "$DEFAULT_INSTALL_DIR" ]]; then
      INSTALL_DIR="$DEFAULT_INSTALL_DIR"
    elif [[ $(id -u) -eq 0 ]]; then
      INSTALL_DIR="$DEFAULT_INSTALL_DIR"
    else
      INSTALL_DIR="$HOME/.local/bin"
    fi
  fi
  mkdir -p "$INSTALL_DIR"
}

select_checksum_tool() {
  if command_exists sha256sum; then
    CHECK_CMD=(sha256sum -c)
  elif command_exists shasum; then
    CHECK_CMD=(shasum -a 256 -c)
  else
    die "Need sha256sum or shasum installed"
  fi
}

verify_checksum() {
  local checksums_file archive_name
  checksums_file="$1"
  archive_name="$2"
  grep "  ${archive_name}$" "$checksums_file" >"${checksums_file}.match" || die "Checksum for ${archive_name} not found"
  (cd "$(dirname "$checksums_file")" && "${CHECK_CMD[@]}" "$(basename "${checksums_file}.match")") >/dev/null
}

install_binary() {
  local source_bin target_bin sudo_cmd=""
  source_bin="$1"
  target_bin="$INSTALL_DIR/$BINARY_NAME"
  if [[ ! -w "$INSTALL_DIR" ]]; then
    if command_exists sudo; then
      sudo_cmd="sudo"
    elif [[ $(id -u) -ne 0 ]]; then
      die "Cannot write to $INSTALL_DIR (try --install-dir or run with sudo)"
    fi
  fi
  ${sudo_cmd:-} install -m 0755 "$source_bin" "$target_bin"
  log "Installed to $target_bin"
}

main() {
  require_commands curl tar awk
  parse_args "$@"
  canonicalize_version
  detect_platform
  resolve_install_dir
  select_checksum_tool

  local tmp_dir archive_name archive_url archive_path checksums_url checksums_path extract_dir bin_path
  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT

  archive_name="${BINARY_NAME}_${VERSION}_${OS}_${ARCH}.tar.gz"
  archive_url="${DOWNLOAD_ROOT}/download/${VERSION}/${archive_name}"
  archive_path="${tmp_dir}/${archive_name}"
  checksums_url="${DOWNLOAD_ROOT}/download/${VERSION}/checksums.txt"
  checksums_path="${tmp_dir}/checksums.txt"

  log "Downloading ${archive_url}"
  curl -fsSL "$archive_url" -o "$archive_path"
  log "Downloading checksums"
  curl -fsSL "$checksums_url" -o "$checksums_path"
  verify_checksum "$checksums_path" "$archive_name"

  extract_dir="${tmp_dir}/extract"
  mkdir -p "$extract_dir"
  tar -xzf "$archive_path" -C "$extract_dir"

  bin_path=$(find "$extract_dir" -type f -name "$BINARY_NAME" | head -n1)
  [[ -n "$bin_path" ]] || die "Binary not found inside archive"
  install_binary "$bin_path"

  if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
    log "WARNING: $INSTALL_DIR is not on your PATH"
  fi
  log "Done"
}

main "$@"
