#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: bash setup.sh [--version REF] [--bin-dir DIRECTORY] [--no-path]

Install harness-gateway with Go (Go 1.27.3+ required).
  --version REF        Go module version or revision (default: v0.1.7)
  --bin-dir DIRECTORY  Installation directory (default: GOBIN or ~/.local/bin)
  --no-path            Leave ~/.bashrc unchanged
  -h, --help           Show this help
USAGE
}

fail() { printf 'setup: %s\n' "$*" >&2; exit 1; }

version=v0.1.7
bin_dir=${GOBIN:-"$HOME/.local/bin"}
update_path=true
while (($#)); do
  case "$1" in
    --version|--bin-dir)
      (($# >= 2)) || fail "$1 requires a value"
      [[ -n "$2" ]] || fail "$1 requires a nonempty value"
      if [[ "$1" == --version ]]; then version=$2; else bin_dir=$2; fi
      shift 2
      ;;
    --no-path) update_path=false; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1 (use --help)" ;;
  esac
done

command -v go >/dev/null 2>&1 || fail 'Go is required; install Go 1.27.3+ from https://go.dev/dl/'
[[ "$bin_dir" != *:* ]] || fail 'the installation directory cannot contain a colon (PATH separator)'
[[ "$version" != *[[:space:]]* ]] || fail 'the version cannot contain whitespace'
mkdir -p -- "$bin_dir"
bin_dir=$(cd -- "$bin_dir" && pwd -P)

printf 'Installing harness-gateway@%s into %s\n' "$version" "$bin_dir"
GOBIN="$bin_dir" GOWORK=off go install "github.com/harness-gateway/harness-gateway/cmd/harness-gateway@$version"
[[ -x "$bin_dir/harness-gateway" ]] || fail 'Go did not produce the expected harness-gateway executable'
"$bin_dir/harness-gateway" --version

resolved=$(command -v harness-gateway || true)
if [[ -n "$resolved" && "$resolved" -ef "$bin_dir/harness-gateway" ]]; then
  printf 'harness-gateway is available on PATH.\n'
else
  if [[ -n "$resolved" ]]; then
    printf 'Warning: harness-gateway resolves to %s, which shadows this installation.\n' "$resolved"
  fi
  printf -v path_line "export PATH=%q:\$PATH" "$bin_dir"
  if "$update_path"; then
    rc_file=$HOME/.bashrc
    if [[ ! -f "$rc_file" ]] || ! grep -Fqx -- "$path_line" "$rc_file"; then
      printf '\n# Harness-Gateway command path\n%s\n' "$path_line" >> "$rc_file"
      printf 'Added the command directory to %s.\n' "$rc_file"
    fi
  fi
  printf 'For this terminal, run:\n  %s\n' "$path_line"
fi
printf 'Run: harness-gateway (default: http://127.0.0.1:8080)\n'
printf 'Custom port: harness-gateway --listen 127.0.0.1:8090\n'
