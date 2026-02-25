#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: tools/release.sh [--major | --minor | --patch]

Bumps the latest semver tag, pushes the branch and new tag, and dispatches
the GitHub Actions release workflow to build artifacts and create a draft release.

If no semver tags exist yet, this script creates and pushes v0.0.0.
EOF
}

bump=""
for arg in "$@"; do
	case "$arg" in
	--major | --minor | --patch)
		if [[ -n "$bump" ]]; then
			echo "only one bump flag is allowed" >&2
			usage
			exit 1
		fi
		bump="$arg"
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "unknown argument: $arg" >&2
		usage
		exit 1
		;;
	esac
done

if [[ -z "$bump" ]]; then
	echo "missing bump flag" >&2
	usage
	exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
	echo "gh CLI is required but not installed" >&2
	exit 1
fi

if ! gh auth status >/dev/null 2>&1; then
	echo "gh CLI is not authenticated; run: gh auth login" >&2
	exit 1
fi

if [[ -n "$(git status --porcelain)" ]]; then
	echo "working tree must be clean before releasing" >&2
	exit 1
fi

current_branch="$(git branch --show-current)"
if [[ -z "$current_branch" ]]; then
	echo "unable to determine current branch (detached HEAD)" >&2
	exit 1
fi

git fetch origin --tags

latest_tag="$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -n1)"
if [[ -z "$latest_tag" ]]; then
	new_tag="v0.0.0"
	echo "no existing version tag found; creating initial tag ${new_tag}"
else
	if [[ ! "$latest_tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
		echo "latest semver tag has unexpected format: $latest_tag" >&2
		exit 1
	fi
	major="${BASH_REMATCH[1]}"
	minor="${BASH_REMATCH[2]}"
	patch="${BASH_REMATCH[3]}"
	case "$bump" in
	--major)
		major="$((major + 1))"
		minor=0
		patch=0
		;;
	--minor)
		minor="$((minor + 1))"
		patch=0
		;;
	--patch)
		patch="$((patch + 1))"
		;;
	esac
	new_tag="v${major}.${minor}.${patch}"
fi

if git rev-parse -q --verify "refs/tags/${new_tag}" >/dev/null; then
	echo "tag already exists: ${new_tag}" >&2
	exit 1
fi

echo "pushing branch ${current_branch}..."
git push origin "$current_branch"

echo "creating tag ${new_tag}..."
git tag "$new_tag"
git push origin "$new_tag"

workflow_file="release.yml"
echo "dispatching workflow ${workflow_file} for tag ${new_tag}..."
gh workflow run "$workflow_file" --ref "$current_branch" -f "tag=${new_tag}"

run_url="$(gh run list --workflow "$workflow_file" --event workflow_dispatch --branch "$current_branch" --limit 1 --json url --jq '.[0].url' 2>/dev/null || true)"
if [[ -n "$run_url" && "$run_url" != "null" ]]; then
	echo "workflow run: ${run_url}"
fi

echo "release prepared for ${new_tag}"
