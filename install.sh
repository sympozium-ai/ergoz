#!/bin/sh
# ergoz installer
#
# While the repo is PRIVATE (gh auth required):
#   gh api repos/sympozium-ai/ergoz/contents/install.sh -H "Accept: application/vnd.github.raw" | sh
#
# Once PUBLIC (sympozium-style):
#   curl -fsSL https://raw.githubusercontent.com/sympozium-ai/ergoz/main/install.sh | sh
#
# Options (append `-s -- --local` when piping):
#   --local, -l   Install to ~/.local/bin (no sudo)
#
# Download strategy, in order:
#   1. `gh release download`      — works private AND public (uses gh auth)
#   2. plain curl of the release  — works once the repo is public
#   3. curl + $GH_TOKEN API asset — private without gh installed

set -e

REPO="sympozium-ai/ergoz"
BINARY="ergoz"
LOCAL_INSTALL=""

info() { printf '  \033[1;34m>\033[0m %s\n' "$*"; }
err()  { printf '  \033[1;31m!\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
    case "$1" in
        --local|-l) LOCAL_INSTALL="1" ;;
        --help|-h)
            echo "Usage: install.sh [--local]"
            exit 0
            ;;
        *) err "Unknown option: $1" ;;
    esac
    shift
done

case "$(uname -s)" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *) err "Unsupported OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) err "Unsupported architecture: $(uname -m)" ;;
esac
ASSET="${BINARY}-${OS}-${ARCH}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

download() {
    # 1. gh: authenticated, works for private and public repos alike.
    if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
        info "Downloading latest ${ASSET} via gh..."
        gh release download --repo "$REPO" --pattern "$ASSET" --output "$TMP/$BINARY" \
            && return 0
        err "gh could not download ${ASSET}. Does the latest release have CLI assets? (https://github.com/${REPO}/releases)"
    fi

    # 2. Anonymous curl: only works once the repo is public.
    info "Trying anonymous download (public repo path)..."
    if curl -fsSL -o "$TMP/$BINARY" \
        "https://github.com/${REPO}/releases/latest/download/${ASSET}" 2>/dev/null; then
        return 0
    fi

    # 3. Token-authenticated API: private repo without gh.
    [ -n "$GH_TOKEN" ] || [ -n "$GITHUB_TOKEN" ] \
        || err "Repo appears private and 'gh' is not available. Install gh (https://cli.github.com) or export GH_TOKEN."
    TOKEN="${GH_TOKEN:-$GITHUB_TOKEN}"
    info "Downloading via GitHub API with token..."
    RELEASE_JSON="$(curl -fsSL -H "Authorization: Bearer ${TOKEN}" \
        "https://api.github.com/repos/${REPO}/releases/latest")" \
        || err "Could not query latest release (check token scopes: repo)."
    # Extract the asset id whose name matches $ASSET without requiring jq:
    # the "id" field precedes "name" within each asset object.
    ASSET_ID="$(printf '%s' "$RELEASE_JSON" \
        | tr ',' '\n' \
        | grep -B5 "\"name\": *\"${ASSET}\"" \
        | grep -o '"id": *[0-9]*' \
        | head -1 \
        | grep -o '[0-9]*')"
    [ -n "$ASSET_ID" ] || err "Asset ${ASSET} not found in the latest release."
    curl -fsSL -H "Authorization: Bearer ${TOKEN}" \
        -H "Accept: application/octet-stream" \
        -o "$TMP/$BINARY" \
        "https://api.github.com/repos/${REPO}/releases/assets/${ASSET_ID}" \
        || err "Asset download failed."
}

download
chmod +x "$TMP/$BINARY"

# Pick the install directory: /usr/local/bin with sudo, else ~/.local/bin.
if [ -n "$LOCAL_INSTALL" ] || ! command -v sudo >/dev/null 2>&1; then
    DEST="${HOME}/.local/bin"
    mkdir -p "$DEST"
    mv "$TMP/$BINARY" "$DEST/$BINARY"
else
    DEST="/usr/local/bin"
    info "Installing to ${DEST} (sudo)..."
    sudo mv "$TMP/$BINARY" "$DEST/$BINARY"
fi

info "Installed: $("$DEST/$BINARY" version)"
case ":$PATH:" in
    *":$DEST:"*) ;;
    *) info "Note: ${DEST} is not on your PATH." ;;
esac
info "Next: ${BINARY} install    # deploy the agent + collector into your cluster"
