#!/bin/sh
set -eu

repo="kimus/piccolo"
version="latest"
install_dir="${PICCOLO_INSTALL_DIR:-}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      shift
      [ "$#" -gt 0 ] || { echo "--version requires a value" >&2; exit 2; }
      version="$1"
      ;;
    --dir)
      shift
      [ "$#" -gt 0 ] || { echo "--dir requires a value" >&2; exit 2; }
      install_dir="$1"
      ;;
    -h|--help)
      echo "usage: install.sh [--version VERSION] [--dir DIR]"
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      exit 2
      ;;
  esac
  shift
done

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin|linux) ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [ "$version" = "latest" ]; then
  if command -v curl >/dev/null 2>&1; then
    version="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  else
    echo "curl is required" >&2
    exit 1
  fi
fi
[ -n "$version" ] || { echo "could not resolve latest Piccolo release" >&2; exit 1; }

asset="piccolo_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Installing piccolo $version for $os/$arch"
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp" && grep "  $asset$" checksums.txt | sha256sum -c -)
elif command -v shasum >/dev/null 2>&1; then
  expected="$(grep "  $asset$" "$tmp/checksums.txt" | awk '{print $1}')"
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
  [ "$expected" = "$actual" ] || { echo "checksum mismatch" >&2; exit 1; }
else
  echo "sha256sum or shasum is required for checksum verification" >&2
  exit 1
fi

if [ -z "$install_dir" ]; then
  install_dir="$HOME/.local/bin"
  mkdir -p "$install_dir" 2>/dev/null || true
  if [ ! -w "$install_dir" ]; then
    install_dir="/usr/local/bin"
  fi
fi
mkdir -p "$install_dir"
tar -C "$tmp" -xzf "$tmp/$asset"
install -m 0755 "$tmp/piccolo" "$install_dir/piccolo"

echo "Installed piccolo to $install_dir/piccolo"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to PATH to run piccolo from any shell." ;;
esac
