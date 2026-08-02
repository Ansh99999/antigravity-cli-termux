#!/usr/bin/env bash
set -Eeuo pipefail

manifest_url="${MANIFEST_URL:-https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_arm64.json}"

if [[ -z "${GITHUB_OUTPUT:-}" ]]; then
  echo "GITHUB_OUTPUT is required." >&2
  exit 1
fi

if [[ -z "${GITHUB_REPOSITORY:-}" ]]; then
  echo "GITHUB_REPOSITORY is required." >&2
  exit 1
fi

if [[ -z "${GH_TOKEN:-}" && -z "${GITHUB_TOKEN:-}" ]]; then
  echo "GH_TOKEN or GITHUB_TOKEN is required for GitHub CLI authentication." >&2
  exit 1
fi

echo "Querying official manifest..."
manifest_json="$(curl -fsSL "$manifest_url")"
manifest_fields="$(
  jq -er '
    .version as $version
    | .url as $url
    | select(
        ($version | type) == "string" and ($version | length) > 0 and
        ($url | type) == "string" and ($url | length) > 0
      )
    | $version, $url
  ' <<<"$manifest_json"
)"
mapfile -t manifest <<<"$manifest_fields"

if [[ ${#manifest[@]} -lt 2 ]]; then
  echo "Error: Incomplete manifest data (expected version and download URL)." >&2
  exit 1
fi

latest_official="${manifest[0]:-}"
download_url="${manifest[1]:-}"
release_tag="v${latest_official}"

echo "Reading repository release tag from Actions Toolbox environment..."
latest_release="${GH_LATEST_RELEASE_TAG:-}"

if [[ "${DRY_RUN:-}" == "true" ]]; then
  echo "Dry run enabled! Overriding latest tag to v0.0.0 and forcing build execution."
  latest_release="v0.0.0"
fi

latest_release="${latest_release#v}"

if [[ -z "$latest_release" ]]; then
  latest_release="0.0.0"
fi

echo "Official Latest Version  : $latest_official"
echo "Your Latest Release Tag  : v$latest_release"

if [[ "${DRY_RUN:-}" != "true" ]] && gh release view "$release_tag" --repo "$GITHUB_REPOSITORY" >/dev/null 2>&1; then
  echo "Release $release_tag already exists."
  printf 'build_needed=false\n' >>"$GITHUB_OUTPUT"
  exit 0
fi

echo "New version detected! Preparing automated build for $release_tag..."
{
  printf 'build_needed=true\n'
  printf 'version=%s\n' "$latest_official"
  printf 'url=%s\n' "$download_url"
  printf 'latest_release=%s\n' "$latest_release"
} >>"$GITHUB_OUTPUT"
