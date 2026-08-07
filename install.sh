#!/usr/bin/env bash
set -euo pipefail

repo="mupt-ai/context-drop"
version="${CONTEXT_DROP_VERSION:-}"
install_dir="${CONTEXT_DROP_INSTALL_DIR:-${INSTALL_DIR:-}}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "context-drop installer requires '$1'" >&2
    exit 1
  fi
}

need curl
need install
need tar
need mktemp

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin) archive_os="macOS" ;;
  linux) archive_os="linux" ;;
  *)
    echo "unsupported OS for context-drop install: $os" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="x86_64" ;;
  aarch64|arm64) arch="arm64" ;;
  *)
    echo "unsupported architecture for context-drop install: $(uname -m)" >&2
    exit 1
    ;;
esac

if [[ -z "$version" ]]; then
  latest_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${repo}/releases/latest")"
  version="${latest_url##*/}"
fi
if [[ "$version" != v* ]]; then
  version="v${version}"
fi
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-+][A-Za-z0-9._+-]+)?$ ]]; then
  echo "could not resolve context-drop release version: $version" >&2
  exit 1
fi

archive_version="${version#v}"
archive="context-drop_${archive_version}_${archive_os}_${arch}.tar.gz"
checksums="context-drop_${archive_version}_checksums.txt"
base_url="https://github.com/${repo}/releases/download/${version}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

curl -fsSL "${base_url}/${archive}" -o "${tmpdir}/${archive}"
curl -fsSL "${base_url}/${checksums}" -o "${tmpdir}/${checksums}"

expected="$(awk -v file="$archive" '$NF == file || $NF == "*" file { print $1; exit }' "${tmpdir}/${checksums}")"
if [[ -z "$expected" ]]; then
  echo "checksum file does not contain ${archive}" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${tmpdir}/${archive}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "${tmpdir}/${archive}" | awk '{print $1}')"
else
  echo "context-drop installer requires 'sha256sum' or 'shasum' to verify downloads" >&2
  exit 1
fi
if [[ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" != "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ]]; then
  echo "checksum mismatch for ${archive}" >&2
  exit 1
fi

tar -xzf "${tmpdir}/${archive}" -C "$tmpdir"
if [[ ! -f "${tmpdir}/context-drop" || ! -f "${tmpdir}/runtime/dist/src/main.js" ]]; then
  echo "release archive does not contain the context-drop runtime assets" >&2
  exit 1
fi

if [[ -z "$install_dir" ]]; then
  if [[ "$(id -u)" == "0" || -w "/usr/local/bin" ]]; then
    install_dir="/usr/local/bin"
  else
    install_dir="${HOME:-$PWD}/.local/bin"
  fi
fi

mkdir -p "$install_dir"
lib_dir="${install_dir}/../lib/context-drop"
runtime_dir="${lib_dir}/runtime"
new_bin="${install_dir}/.context-drop.new.$$"
new_runtime="${runtime_dir}/.dist.new.$$"
old_bin="${install_dir}/.context-drop.old.$$"
old_runtime="${runtime_dir}/.dist.old.$$"
service_label="dev.contextdrop.daemon"
service_was_loaded=0
if [[ "$os" == "darwin" ]] && launchctl print "gui/$(id -u)/${service_label}" >/dev/null 2>&1; then
  service_was_loaded=1
  launchctl bootout "gui/$(id -u)/${service_label}" || { echo "failed to stop Context Drop service before upgrade" >&2; exit 1; }
elif [[ "$os" == "linux" ]] && systemctl --user is-active --quiet context-drop-daemon.service; then
  service_was_loaded=1
  systemctl --user stop context-drop-daemon.service || { echo "failed to stop Context Drop service before upgrade" >&2; exit 1; }
fi
restart_service() {
  if [[ "$service_was_loaded" != 1 ]]; then return 0; fi
  if [[ "$os" == "darwin" ]]; then
    launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/${service_label}.plist"
  else
    systemctl --user start context-drop-daemon.service
  fi
}
rollback() {
  rm -f "$new_bin"; rm -rf "$new_runtime"
  rm -f "${install_dir}/context-drop"; rm -rf "${runtime_dir}/dist"
  [[ -e "$old_bin" ]] && mv "$old_bin" "${install_dir}/context-drop"
  [[ -e "$old_runtime" ]] && mv "$old_runtime" "${runtime_dir}/dist"
  restart_service >/dev/null 2>&1 || true
}
mkdir -p "$runtime_dir"
install -m 0755 "${tmpdir}/context-drop" "$new_bin"
cp -R "${tmpdir}/runtime/dist" "$new_runtime"
[[ -e "${install_dir}/context-drop" ]] && mv "${install_dir}/context-drop" "$old_bin"
[[ -e "${runtime_dir}/dist" ]] && mv "${runtime_dir}/dist" "$old_runtime"
if ! mv "$new_bin" "${install_dir}/context-drop" || ! mv "$new_runtime" "${runtime_dir}/dist"; then
  rollback
  echo "atomic Context Drop upgrade failed; prior installation restored" >&2
  exit 1
fi
rm -f "$old_bin"; rm -rf "$old_runtime"
if ! restart_service; then
  echo "upgrade installed but could not restart Context Drop service" >&2
  exit 1
fi

echo "context-drop ${version} installed at ${install_dir}/context-drop"
if [[ -n "${GITHUB_PATH:-}" ]]; then
  echo "$install_dir" >> "$GITHUB_PATH"
fi
if ! command -v context-drop >/dev/null 2>&1; then
  echo "Add ${install_dir} to your PATH to run 'context-drop' from anywhere."
fi
"${install_dir}/context-drop" version
