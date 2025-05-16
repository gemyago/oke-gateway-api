#!/usr/bin/env bash

set -euo pipefail

# Function to print usage information
usage() {
  echo "Usage: $0 --chart-path <path> --git-ref <ref> [--noop] [--commit]"
  echo "Options:"
  echo "  --chart-path <path>       Path to the Helm chart directory (e.g., deploy/helm/controller)"
  echo "  --git-ref <ref>           Git reference (e.g., main, v1.2.3, feature/xyz)"
  echo "  --noop                    Show what would be changed without making changes"
  echo "  --commit                  Commit changes after updating"
  echo "  -h, --help                Show this help message"
}

CHART_PATH=""
GIT_REF=""
NOOP=false
COMMIT=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --chart-path)
      CHART_PATH="$2"
      shift 2
      ;;
    --git-ref)
      GIT_REF="$2"
      shift 2
      ;;
    --noop)
      NOOP=true
      shift
      ;;
    --commit)
      COMMIT=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      usage
      exit 1
      ;;
  esac
done

if [[ -z "$CHART_PATH" ]]; then
  echo "Error: --chart-path is required"
  usage
  exit 1
fi

if [[ -z "$GIT_REF" ]]; then
  echo "Error: --git-ref is required"
  usage
  exit 1
fi

CHART_FILE="${CHART_PATH}/Chart.yaml"

if [[ ! -f "$CHART_FILE" ]]; then
  echo "Error: Chart.yaml not found at $CHART_FILE"
  exit 1
fi

if [[ "$NOOP" == "true" ]]; then
  echo "Running in noop mode - no changes will be made"
fi

echo "Updating chart $CHART_FILE..."
echo "Current chart version (before update):"
grep -E '^version: ' "$CHART_FILE"
grep -E '^appVersion: ' "$CHART_FILE"

# Read current version and increment patch version
current_version=$(grep -E '^version: ' "$CHART_FILE" | cut -d' ' -f2 | tr -d '"')

# Ensure current_version is in a valid format like x.y.z
if ! echo "$current_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "Error: Current version '$current_version' is not in a valid x.y.z format."
    echo "To prevent partial updates, please ensure Chart.yaml versions are standard before running."
    exit 1
fi

# Increment patch version (e.g., 0.1.0 -> 0.1.1)
major=$(echo "$current_version" | cut -d. -f1)
minor=$(echo "$current_version" | cut -d. -f2)
patch=$(echo "$current_version" | cut -d. -f3)
new_patch=$((patch + 1))
new_version="${major}.${minor}.${new_patch}"

echo ""
echo "Changes that would be made:"
echo "  New appVersion: $GIT_REF"
echo "  New version: $new_version"

if [[ "$COMMIT" == "true" ]]; then
  commit_msg="chore: update chart version to $new_version and appVersion to $GIT_REF"
  if [[ "$NOOP" == "true" ]]; then
    echo ""
    echo "Commit message that would be used:"
    echo "  $commit_msg"
  fi
fi

if [[ "$NOOP" == "true" ]]; then
  echo ""
  echo "No changes made (noop mode)"
  exit 0
fi

# Update appVersion using sed
sed -i.bak "s/^appVersion:.*/appVersion: \"$GIT_REF\"/" "$CHART_FILE"
rm -f "${CHART_FILE}.bak"

# Update version using sed
sed -i.bak "s/^version:.*/version: \"$new_version\"/" "$CHART_FILE"
rm -f "${CHART_FILE}.bak"

echo ""
echo "Chart $CHART_FILE updated successfully:"
echo "  New appVersion: $GIT_REF"
echo "  New version: $new_version"

if [[ "$COMMIT" == "true" ]]; then
  echo ""
  echo "Committing changes..."
  git add "$CHART_FILE"
  git commit -m "$commit_msg"
  echo "Changes committed with message: $commit_msg"
fi