#!/usr/bin/env bash

set -euo pipefail

# Function to print usage information
usage() {
  echo "Usage: $0 --chart-file <path> --app-version <version> [--noop] [--commit]"
  echo "Options:"
  echo "  --chart-file <path>       Path to the Chart.yaml file (e.g., deploy/helm/controller/Chart.yaml)"
  echo "  --app-version <version>   Release version to set as appVersion (e.g., v1.2.3)"
  echo "  --noop                    Show what would be changed without making changes"
  echo "  --commit                  Commit changes after updating"
  echo "  -h, --help                Show this help message"
}

CHART_FILE=""
APP_VERSION=""
NOOP=false
COMMIT=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --chart-file)
      CHART_FILE="$2"
      shift 2
      ;;
    --app-version)
      APP_VERSION="$2"
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

if [[ -z "$CHART_FILE" ]]; then
  echo "Error: --chart-file is required"
  usage
  exit 1
fi

if [[ -z "$APP_VERSION" ]]; then
  echo "Error: --app-version is required"
  usage
  exit 1
fi

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

# Helm chart versions must be SemVer and cannot include a leading "v".
new_version="${APP_VERSION#v}"

if ! echo "$new_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
    echo "Error: App version '$APP_VERSION' does not resolve to a valid Helm chart version."
    echo "Expected a release version like v1.2.3 or v1.2.3-rc.1."
    exit 1
fi

echo ""
echo "Changes that would be made:"
echo "  New appVersion: $APP_VERSION"
echo "  New version: $new_version"

if [[ "$COMMIT" == "true" ]]; then
  commit_msg="chore: sync chart version $new_version with appVersion $APP_VERSION"
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
sed -i.bak "s/^appVersion:.*/appVersion: \"$APP_VERSION\"/" "$CHART_FILE"
rm -f "${CHART_FILE}.bak"

# Update version using sed
sed -i.bak "s/^version:.*/version: \"$new_version\"/" "$CHART_FILE"
rm -f "${CHART_FILE}.bak"

echo ""
echo "Chart $CHART_FILE updated successfully:"
echo "  New appVersion: $APP_VERSION"
echo "  New version: $new_version"

if [[ "$COMMIT" == "true" ]]; then
  echo ""
  echo "Committing changes..."
  git add "$CHART_FILE"
  git commit -m "$commit_msg"
  echo "Changes committed with message: $commit_msg"
fi
