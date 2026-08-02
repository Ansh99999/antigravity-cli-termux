#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $(basename "$0") <target_version> [latest_release_version]" >&2
  exit 1
fi

VERSION="${1#v}"

if [[ "${DRY_RUN:-}" == "true" ]]; then
  echo "Dry run enabled! Overriding latest tag to v0.0.0 for changelog compilation."
  LATEST_RELEASE="0.0.0"
elif [[ $# -ge 2 ]]; then
  LATEST_RELEASE="${2#v}"
else
  latest_env="${GH_LATEST_RELEASE_TAG:-}"
  LATEST_RELEASE="${latest_env#v}"
fi

LATEST_RELEASE="${LATEST_RELEASE:-0.0.0}"

# Query fork commits since the last release tag
echo "Extracting fork-specific release changelog..."
FORK_CHANGELOG=""
if [[ "$LATEST_RELEASE" != "0.0.0" ]] && git rev-parse "v$LATEST_RELEASE" >/dev/null 2>&1; then
  FORK_CHANGELOG=$(git log "v$LATEST_RELEASE..HEAD" --oneline --pretty=format:"* %s (%h)")
else
  FORK_CHANGELOG=$(git log -n 20 --oneline --pretty=format:"* %s (%h)")
fi

if [[ -z "$FORK_CHANGELOG" ]]; then
  FORK_CHANGELOG="* Maintenance rebuild / sync with upstream release."
fi

# Download the upstream changelog file
mkdir -p staging
echo "Downloading upstream CHANGELOG.md..."
if ! curl -fsSL -o staging/CHANGELOG.md "https://raw.githubusercontent.com/google-antigravity/antigravity-cli/refs/heads/main/CHANGELOG.md"; then
  echo "Warning: Failed to retrieve upstream CHANGELOG.md." >&2
  rm -f staging/CHANGELOG.md
fi

# Extract the release notes section for the target version using mq
echo "Extracting release notes for version ${VERSION} using mq..."
UPSTREAM_NOTES=""
if [[ -f "staging/CHANGELOG.md" ]]; then
  UPSTREAM_NOTES=$(mq -A "import \"section\" | section::section(\"${VERSION}\")" staging/CHANGELOG.md 2>/dev/null || true)
fi

if [[ -z "${UPSTREAM_NOTES}" ]]; then
  UPSTREAM_NOTES="Sync with upstream version v${VERSION}."
fi

escape_bash_replacement() {
  local value="${1//\\/\\\\}"
  printf '%s' "${value//&/\\&}"
}

# Render the final release notes from the template file
REPO="${GITHUB_REPOSITORY:-wallentx/antigravity-cli-termux}"
OWNER="${REPO%/*}"

TEMPLATE_PATH=".github/release-template.md"
if [[ -f "$TEMPLATE_PATH" ]]; then
  # Append a dummy character to prevent command substitution from stripping trailing newlines
  TEMPLATE=$(cat "$TEMPLATE_PATH"; printf 'x')
  TEMPLATE="${TEMPLATE%x}"
  
  # Substitute placeholders with compiled values
  VERSION_REPLACEMENT="$(escape_bash_replacement "$VERSION")"
  FORK_CHANGELOG_REPLACEMENT="$(escape_bash_replacement "$FORK_CHANGELOG")"
  UPSTREAM_NOTES_REPLACEMENT="$(escape_bash_replacement "$UPSTREAM_NOTES")"
  REPO_REPLACEMENT="$(escape_bash_replacement "$REPO")"
  OWNER_REPLACEMENT="$(escape_bash_replacement "$OWNER")"

  OUTPUT="${TEMPLATE//\{\{VERSION\}\}/$VERSION_REPLACEMENT}"
  OUTPUT="${OUTPUT//\{\{FORK_CHANGELOG\}\}/$FORK_CHANGELOG_REPLACEMENT}"
  OUTPUT="${OUTPUT//\{\{UPSTREAM_NOTES\}\}/$UPSTREAM_NOTES_REPLACEMENT}"
  OUTPUT="${OUTPUT//\{\{REPO\}\}/$REPO_REPLACEMENT}"
  OUTPUT="${OUTPUT//\{\{OWNER\}\}/$OWNER_REPLACEMENT}"
  
  echo "$OUTPUT" > staging/release-notes.md
  echo "Release notes generated successfully in staging/release-notes.md."
else
  echo "Error: Release template file not found at $TEMPLATE_PATH" >&2
  exit 1
fi
