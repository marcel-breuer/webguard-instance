#!/usr/bin/env bash
set -euo pipefail

notes_file="${1:-RELEASE_NOTES.md}"
changelog_file="${CHANGELOG_FILE:-CHANGELOG.md}"
tag="${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
release_date="${CHANGELOG_DATE:-$(date -u +%Y-%m-%d)}"

if [ ! -f "$notes_file" ]; then
  echo "Release notes file not found: $notes_file" >&2
  exit 1
fi

existing_changelog="$(mktemp)"
trap 'rm -f "$existing_changelog"' EXIT

if [ -f "$changelog_file" ]; then
  awk -v tag="$tag" '
    $0 == "## " tag || $0 ~ "^## " tag " - " {
      skip = 1
      next
    }
    skip && /^## / {
      skip = 0
    }
    !skip {
      print
    }
  ' "$changelog_file" > "$existing_changelog"
else
  : > "$existing_changelog"
fi

{
  echo "# Changelog"
  echo
  echo "## ${tag} - ${release_date}"
  echo
  cat "$notes_file"
  echo
  awk '
    drop && /^# Changelog$/ { next }
    drop && /^$/ { next }
    { drop = 0; print }
  ' drop=1 "$existing_changelog"
} > "$changelog_file"
