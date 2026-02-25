#!/usr/bin/env bash
set -euo pipefail

# setup-go-dev.sh - Portable Go development environment setup script
# Usage: sudo bash setup-go-dev.sh [GO_VERSION]
# Example: sudo bash setup-go-dev.sh 1.21.7

# Determine Go version: use provided arg or fetch latest from go.dev
DRY_RUN=0
if [ "${1-}" = "--dry-run" ]; then
  DRY_RUN=1
  shift
fi
if [ -n "${1-}" ]; then
  GO_VERSION="${1}"
else
  echo "Fetching latest Go version..."
  if command -v curl >/dev/null 2>&1; then
    LATEST_RAW=$(curl -fsSL "https://go.dev/VERSION?m=text" || true)
  elif command -v wget >/dev/null 2>&1; then
    LATEST_RAW=$(wget -qO- "https://go.dev/VERSION?m=text" || true)
  else
    LATEST_RAW=""
  fi
  # sanitize the response: take the first token that looks like 'goX.Y[.Z]'
  if [ -n "$LATEST_RAW" ]; then
    LATEST_LINE=$(printf '%s' "$LATEST_RAW" | sed -n '1p' | tr -d '\r\n' | awk '{print $1}')
    GO_TOKEN=$(printf '%s' "$LATEST_LINE" | grep -oE 'go[0-9]+([.][0-9]+)*' || true)
    if [ -n "$GO_TOKEN" ]; then
      GO_VERSION="${GO_TOKEN#go}"
      echo "Latest Go version is $GO_VERSION"
    else
      GO_VERSION="1.21.7"
      echo "Could not parse latest Go version; falling back to $GO_VERSION"
    fi
  else
    GO_VERSION="1.21.7"
    echo "Could not fetch latest Go version; falling back to $GO_VERSION"
  fi
fi
GOPATH_DEFAULT="$HOME/go"
GOBIN_DEFAULT="$GOPATH_DEFAULT/bin"

echo "==> Starting Go dev environment setup"

# Helpers
has_cmd() { command -v "$1" >/dev/null 2>&1; }

detect_pkg_manager() {
  if has_cmd apt-get; then echo "apt"; return; fi
  if has_cmd yum; then echo "yum"; return; fi
  if has_cmd dnf; then echo "dnf"; return; fi
  if has_cmd pacman; then echo "pacman"; return; fi
  if has_cmd apk; then echo "apk"; return; fi
  echo "none"
}

PKG_MANAGER=$(detect_pkg_manager)

# 1) Check if go is already installed and matches requested version
if has_cmd go; then
  INSTALLED_GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
  echo "Detected Go version: $INSTALLED_GO_VERSION"
  if [ "$INSTALLED_GO_VERSION" = "$GO_VERSION" ]; then
    echo "Requested Go version $GO_VERSION already installed. Skipping Go install."
  else
    echo "Different Go version detected. Proceeding with installation of $GO_VERSION (will replace if using tarball)."
  fi
fi

# 2) Install Go: use official tarball; ensure download utilities exist (use package manager only for utilities)

install_download_utils() {
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "Dry-run: would install download utilities via package manager"
    return 0
  fi
  case "$PKG_MANAGER" in
    apt)
      sudo apt-get update -qq
      sudo apt-get install -y --no-install-recommends curl wget ca-certificates tar || return 1
      ;;
    yum)
      sudo yum install -y curl wget ca-certificates tar || return 1
      ;;
    dnf)
      sudo dnf install -y curl wget ca-certificates tar || return 1
      ;;
    pacman)
      sudo pacman -Sy --noconfirm curl wget ca-certificates tar || return 1
      ;;
    apk)
      sudo apk add --no-cache curl wget ca-certificates tar || return 1
      ;;
    *)
      return 1
      ;;
  esac
  return 0
}

install_go_via_tarball() {
  ARCH="amd64"
  OS="linux"
  TARFILE="go${GO_VERSION}.${OS}-${ARCH}.tar.gz"
  DOWNLOAD_URL="https://go.dev/dl/${TARFILE}"

  if [ "$DRY_RUN" -eq 1 ]; then
    echo "Dry-run: would download $DOWNLOAD_URL"
    return 0
  fi

  echo "Downloading $DOWNLOAD_URL"
  if has_cmd curl; then
    curl -fsSL "$DOWNLOAD_URL" -o "/tmp/$TARFILE"
  elif has_cmd wget; then
    wget -qO "/tmp/$TARFILE" "$DOWNLOAD_URL"
  else
    echo "Error: curl or wget required to download Go tarball." >&2
    return 1
  fi

  echo "Extracting to /usr/local"
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "/tmp/$TARFILE"
  rm -f "/tmp/$TARFILE"
}

# Ensure we have a downloader available; if not, try to install utilities via package manager
if ! has_cmd curl && ! has_cmd wget; then
  if [ "${PKG_MANAGER}" != "none" ]; then
    echo "No curl/wget found; installing download utilities via $PKG_MANAGER"
    if ! install_download_utils; then
      echo "Warning: failed to install download utilities via $PKG_MANAGER; please install curl or wget and rerun." >&2
    fi
  else
    if [ "$DRY_RUN" -eq 1 ]; then
      echo "Dry-run: no curl/wget present but proceeding in dry-run mode"
    else
      echo "Error: no curl or wget and no supported package manager to install them." >&2
      exit 1
    fi
  fi
fi

# Always install Go from official tarball to ensure requested/latest version
install_go_via_tarball

# 3) Ensure GOPATH/GOBIN and PATH are configured
GOPATH="${GOPATH:-$GOPATH_DEFAULT}"
GOBIN="${GOBIN:-$GOBIN_DEFAULT}"

mkdir -p "$GOPATH" "$GOBIN"

apply_shell_profile() {
  local file="$1"
  [ -f "$file" ] || touch "$file"

  # Add exports idempotently
  grep -q "# >>> go dev setup >>>" "$file" 2>/dev/null || cat >> "$file" <<'EOF'
# >>> go dev setup >>>
export GOPATH="$HOME/go"
export GOBIN="$GOPATH/bin"
export PATH="$PATH:$GOBIN:/usr/local/go/bin"
export GO111MODULE=on
# <<< go dev setup <<<
EOF
}

apply_shell_profile "$HOME/.profile"
apply_shell_profile "$HOME/.bashrc"

# Apply changes to current shell environment for the remainder of the script
export GOPATH="$GOPATH"
export GOBIN="$GOBIN"
export PATH="$PATH:$GOBIN:/usr/local/go/bin"
export GO111MODULE=on

# 4) Install common Go tooling
echo "Installing common Go tools in $GOBIN"
# Use go install with @latest (module-aware)
TOOLS=(
  "golang.org/x/tools/gopls@latest"
  "github.com/go-delve/delve/cmd/dlv@latest"
  "github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
)

for t in "${TOOLS[@]}"; do
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "Dry-run: would install $t"
    continue
  fi
  echo " - Installing $t"
  if ! go install "$t"; then
    echo "Warning: failed to install $t" >&2
  fi
done

# 5) Verify installations
echo "Verifying installations:"
for bin in gopls dlv golangci-lint go; do
  if has_cmd "$bin"; then
    echo " - $bin: $(command -v $bin)"
  else
    echo " - $bin: not found (install may have failed)" >&2
  fi
done

# 6) Final message
cat <<EOF

Done. To finish setup for your shell session run:
  source ~/.profile

Common next steps:
  - Open your editor and ensure GOPATH-aware tools are enabled (gopls)
  - Configure golangci-lint in your repo if desired (create .golangci.yml)

EOF
