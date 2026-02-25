#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/update-readme-help.sh --write   # update README section in-place
  ./scripts/update-readme-help.sh --check   # fail if README section is stale
EOF
}

mode="${1:---check}"
case "$mode" in
  --write | --check) ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    usage
    exit 2
    ;;
esac

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readme_path="$root_dir/README.md"

begin_marker="<!-- BEGIN GENERATED: kpf-help -->"
end_marker="<!-- END GENERATED: kpf-help -->"

if [[ ! -f "$readme_path" ]]; then
  echo "README not found at $readme_path" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "go is required to generate help output" >&2
  exit 1
fi

help_output="$(
  cd "$root_dir"
  if [[ -z "${GOCACHE:-}" ]]; then
    export GOCACHE=/tmp/kpf-go-build-cache
  fi
  if [[ -z "${GOMODCACHE:-}" ]]; then
    export GOMODCACHE=/tmp/kpf-go-mod-cache
  fi
  KUBECONFIG=/dev/null go run . --help
)"

kpf_option_lines="$(
  printf '%s\n' "$help_output" | awk '
    /^kpf options:$/ {in_options=1; next}
    /^Environment variables:$/ {in_options=0}
    in_options {print}
  '
)"

if [[ -z "$kpf_option_lines" ]]; then
  echo "failed to extract kpf-specific options from help output" >&2
  exit 1
fi

tmp_file="$(mktemp)"
trap 'rm -f "$tmp_file"' EXIT

inside_markers=0
replaced=0
while IFS= read -r line || [[ -n "$line" ]]; do
  if [[ "$inside_markers" -eq 0 && "$line" == "$begin_marker" ]]; then
    replaced=1
    inside_markers=1
    printf '%s\n' "$begin_marker" >>"$tmp_file"
    printf '%s\n' >>"$tmp_file"
    printf '%s\n' 'Supports all options from `kubectl port-forward`, plus:' >>"$tmp_file"
    printf '%s\n' >>"$tmp_file"
    printf '%s\n' '```text' >>"$tmp_file"
    printf '%s\n' "$kpf_option_lines" >>"$tmp_file"
    printf '%s\n' '```' >>"$tmp_file"
    printf '%s\n' >>"$tmp_file"
    printf '%s\n' "$end_marker" >>"$tmp_file"
    continue
  fi
  if [[ "$inside_markers" -eq 1 ]]; then
    if [[ "$line" == "$end_marker" ]]; then
      inside_markers=0
    fi
    continue
  fi
  printf '%s\n' "$line" >>"$tmp_file"
done <"$readme_path"

if [[ "$inside_markers" -eq 1 ]]; then
  echo "README markers are malformed: missing end marker $end_marker" >&2
  exit 1
fi
if [[ "$replaced" -eq 0 ]]; then
  echo "README markers not found. Expected $begin_marker ... $end_marker" >&2
  exit 1
fi

if [[ "$mode" == "--write" ]]; then
  mv "$tmp_file" "$readme_path"
  echo "Updated README help section."
  exit 0
fi

if ! cmp -s "$tmp_file" "$readme_path"; then
  echo "README help section is stale." >&2
  echo "Run: ./scripts/update-readme-help.sh --write" >&2
  exit 1
fi
