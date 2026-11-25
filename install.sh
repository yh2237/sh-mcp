#!/bin/sh
# Installs sh-mcp from GitHub Releases on Linux and macOS (and Git Bash on Windows).
#
#   curl -fsSL https://raw.githubusercontent.com/yh2237/sh-mcp/main/install.sh | sh
#
# SH_MCP_VERSION     release tag to install, e.g. v1.2.0 (default: latest)
# SH_MCP_INSTALL_DIR where to put the binary (default: ~/.local/bin)

set -eu

err() {
	echo "install.sh: $*" >&2
	exit 1
}

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		err "curl or wget is required"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		err "sha256sum or shasum is required"
	fi
}

main() {
	base="${SH_MCP_BASE_URL:-https://github.com/yh2237/sh-mcp/releases}"
	version="${SH_MCP_VERSION:-latest}"
	install_dir="${SH_MCP_INSTALL_DIR:-$HOME/.local/bin}"

	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	MINGW* | MSYS* | CYGWIN*) os=windows ;;
	*) err "unsupported OS: $(uname -s)" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) err "unsupported architecture: $(uname -m)" ;;
	esac
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
		arch=arm64
	fi
	ext=
	if [ "$os" = windows ]; then
		ext=.exe
	fi

	asset="sh-mcp-$os-$arch$ext"
	if [ "$version" = latest ]; then
		url="$base/latest/download"
	else
		url="$base/download/$version"
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' INT TERM

	echo "Downloading $asset ($version)"
	fetch "$url/$asset" "$tmp/$asset" || err "download failed: $url/$asset"
	fetch "$url/SHA256SUMS" "$tmp/SHA256SUMS" || err "download failed: $url/SHA256SUMS"

	want=$(awk -v f="$asset" '$2 == f || $2 == "*" f {print $1}' "$tmp/SHA256SUMS")
	[ -n "$want" ] || err "$asset is not listed in SHA256SUMS"
	got=$(sha256 "$tmp/$asset")
	[ "$want" = "$got" ] || err "checksum mismatch for $asset (want $want, got $got)"

	mkdir -p "$install_dir"
	chmod +x "$tmp/$asset"
	mv -f "$tmp/$asset" "$install_dir/sh-mcp$ext"
	echo "Installed $("$install_dir/sh-mcp$ext" --version) to $install_dir/sh-mcp$ext"

	case ":$PATH:" in
	*":$install_dir:"*) ;;
	*)
		echo
		echo "$install_dir is not on PATH. Add it to your shell profile, e.g.:"
		echo "  export PATH=\"$install_dir:\$PATH\""
		;;
	esac
}

main "$@"
