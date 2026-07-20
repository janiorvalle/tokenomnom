#!/bin/sh
set -eu

repo="${TOKENOMNOM_INSTALL_REPO:-janiorvalle/tokenomnom}"
install_dir="${TOKENOMNOM_INSTALL_DIR:-$HOME/.local/bin}"
base_url="${TOKENOMNOM_INSTALL_BASE_URL:-}"
version="${TOKENOMNOM_INSTALL_VERSION:-}"
archive_name="${TOKENOMNOM_INSTALL_ARCHIVE:-}"

fail() {
  printf 'tokenomnom installer: %s\n' "$*" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported operating system; Windows users should download the release zip" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$version" ]; then
  release_json=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest") || fail "no published release found for $repo; see https://github.com/$repo/releases or use go install github.com/$repo/cmd/tokenomnom@latest"
  version=$(printf '%s\n' "$release_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$version" ] || fail "latest release did not include a tag_name"
fi
version=${version#v}

if [ -z "$archive_name" ]; then
  archive_name="tokenomnom_${version}_${os}_${arch}.tar.gz"
fi
if [ -z "$base_url" ]; then
  base_url="https://github.com/$repo/releases/download/v$version"
fi
base_url=${base_url%/}

tmp_dir=$(mktemp -d 2>/dev/null || mktemp -d -t tokenomnom-install)
stage_tokenomnom=""
stage_nomnom=""
dest_tokenomnom="$install_dir/tokenomnom"
dest_nomnom="$install_dir/nomnom"
backup_tokenomnom="$tmp_dir/previous-tokenomnom"
backup_nomnom="$tmp_dir/previous-nomnom"
had_tokenomnom=false
had_nomnom=false
transaction_active=false

restore_install() {
  transaction_active=false
  if [ "$had_tokenomnom" = true ]; then
    mv -f "$backup_tokenomnom" "$dest_tokenomnom"
  else
    rm -f "$dest_tokenomnom"
  fi
  if [ "$had_nomnom" = true ]; then
    mv -f "$backup_nomnom" "$dest_nomnom"
  else
    rm -f "$dest_nomnom"
  fi
}
cleanup() {
  [ "$transaction_active" = false ] || restore_install
  rm -rf "$tmp_dir"
  [ -z "$stage_tokenomnom" ] || rm -f "$stage_tokenomnom"
  [ -z "$stage_nomnom" ] || rm -f "$stage_nomnom"
}
trap cleanup EXIT HUP INT TERM

archive="$tmp_dir/$archive_name"
checksums="$tmp_dir/checksums.txt"
printf 'Downloading tokenomnom %s for %s/%s...\n' "$version" "$os" "$arch"
curl -fsSL "$base_url/$archive_name" -o "$archive" || fail "could not download $archive_name"
curl -fsSL "$base_url/checksums.txt" -o "$checksums" || fail "could not download checksums.txt"

expected=$(awk -v file="$archive_name" '$2 == file || $2 == "*" file { print $1; exit }' "$checksums")
[ -n "$expected" ] || fail "checksums.txt has no entry for $archive_name"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$archive" | awk '{print $1}')
else
  fail "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $archive_name"

tar -xzf "$archive" -C "$tmp_dir"
[ -x "$tmp_dir/tokenomnom" ] || fail "archive did not contain tokenomnom"
[ -x "$tmp_dir/nomnom" ] || fail "archive did not contain nomnom"
tokenomnom_version=$("$tmp_dir/tokenomnom" --version) || fail "release tokenomnom failed its version smoke test"
nomnom_version=$("$tmp_dir/nomnom" --version) || fail "release nomnom failed its version smoke test"
[ "$tokenomnom_version" = "$nomnom_version" ] || fail "release binaries reported different versions"

mkdir -p "$install_dir"
stage_tokenomnom="$install_dir/.tokenomnom.new.$$"
stage_nomnom="$install_dir/.nomnom.new.$$"
install -m 0755 "$tmp_dir/tokenomnom" "$stage_tokenomnom"
install -m 0755 "$tmp_dir/nomnom" "$stage_nomnom"
if [ -e "$dest_tokenomnom" ] || [ -L "$dest_tokenomnom" ]; then
  cp -p "$dest_tokenomnom" "$backup_tokenomnom"
  had_tokenomnom=true
fi
if [ -e "$dest_nomnom" ] || [ -L "$dest_nomnom" ]; then
  cp -p "$dest_nomnom" "$backup_nomnom"
  had_nomnom=true
fi
transaction_active=true
mv -f "$stage_tokenomnom" "$dest_tokenomnom" || fail "could not replace tokenomnom"
stage_tokenomnom=""
mv -f "$stage_nomnom" "$dest_nomnom" || fail "could not replace nomnom"
stage_nomnom=""

"$dest_tokenomnom" --version >/dev/null || fail "installed tokenomnom failed its version smoke test"
"$dest_nomnom" --version >/dev/null || fail "installed nomnom failed its version smoke test"
transaction_active=false
printf 'Installed tokenomnom and nomnom to %s\n' "$install_dir"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) printf 'Add %s to PATH to run tokenomnom from any directory.\n' "$install_dir" ;;
esac
