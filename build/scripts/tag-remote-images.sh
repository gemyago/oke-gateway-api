#!/usr/bin/env bash

# --- Usage Examples ---
#
# This script retags an existing Docker image (identified by its source commit SHA tag)
# with one or more new target tags.
#
# Prerequisites:
# - The 'crane' tool must be installed (e.g., via 'make -C build install-crane')
# - For actual tagging (not --noop), crane needs registry authentication.
#
# Example 1: Using direct image list (--remote-images) and --noop
#
#   build/scripts/tag-remote-images.sh \
#     --source-commit-sha "abcdef1234567" \
#     --target-tags "v1.2.3 v1.2-latest latest" \
#     --remote-images $'ghcr.io/owner/repo-server\nghcr.io/owner/repo-jobs' \
#     --noop
#
# Expected Output (Example 1):
#
#   Info: Using remote images provided via --remote-images argument.
#   --- Reading Configuration ---
#   Using stable branches: <branches_from_build.cfg>
#
#   --- Target Tags ---
#   Provided target tags: v1.2.3 v1.2-latest latest
#
#   --- Tagging Remote Images ---
#   *** NOOP mode enabled: Printing commands instead of executing them ***
#   Source image tag: git-commit-abcdef1
#   Reading base images from --remote-images argument...
#   Processing base image: ghcr.io/owner/repo-server
#     [NOOP] Would run: '<path_to_crane>' tag 'ghcr.io/owner/repo-server:git-commit-abcdef1' 'v1.2.3'
#     [NOOP] Would run: '<path_to_crane>' tag 'ghcr.io/owner/repo-server:git-commit-abcdef1' 'v1.2-latest'
#     [NOOP] Would run: '<path_to_crane>' tag 'ghcr.io/owner/repo-server:git-commit-abcdef1' 'latest'
#   
#   Processing base image: ghcr.io/owner/repo-jobs
#     [NOOP] Would run: '<path_to_crane>' tag 'ghcr.io/owner/repo-jobs:git-commit-abcdef1' 'v1.2.3'
#     [NOOP] Would run: '<path_to_crane>' tag 'ghcr.io/owner/repo-jobs:git-commit-abcdef1' 'v1.2-latest'
#     [NOOP] Would run: '<path_to_crane>' tag 'ghcr.io/owner/repo-jobs:git-commit-abcdef1' 'latest'
#   
#   --- Tagging Summary ---
#   NOOP mode finished. No changes were made.
#
# Example 2: Using image file (--remote-images-file) and actual execution
#   # Assume /tmp/images.txt contains:
#   # ghcr.io/owner/repo-server
#   # ghcr.io/owner/repo-jobs
#
#   echo $'ghcr.io/owner/repo-server\nghcr.io/owner/repo-jobs' > /tmp/images.txt
#
#   build/scripts/tag-remote-images.sh \
#     --source-commit-sha "abcdef1234567" \
#     --target-tags "v1.2.3" \
#     --remote-images-file /tmp/images.txt
#
#   # (Ensure crane is authenticated before running without --noop)
#
# --- End Usage Examples ---

# This script tags existing remote Docker images using the crane tool.
# It reads a list of base image names (e.g., ghcr.io/namespace/repo-app)
# from a specified file. It determines the target tags based on the
# target commit SHA and git reference by calling resolve-docker-tags.sh.
# Then, it uses 'crane tag' to apply these target tags to the images
# originally tagged with the source commit SHA.
# NOTE: Both source and target commit SHAs are automatically shortened
#       to the first 7 characters internally for tagging purposes.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- Configuration ---
# Assumed relative paths from the repository root
readonly CRANE_PATH="${SCRIPT_DIR}/../bin/crane"
readonly RESOLVE_TAGS_SCRIPT_PATH="${SCRIPT_DIR}/resolve-docker-tags.sh"
readonly BUILD_CFG_PATH="${SCRIPT_DIR}/../build.cfg"
# Default location for the file listing remote image base names
DEFAULT_REMOTE_IMAGES_FILE="${SCRIPT_DIR}/../docker/.remote-images"

# --- Script Functions ---

# Function to print usage information
usage() {
  echo "Usage: $0 --source-commit-sha <sha> --target-tags \"<tag1> <tag2> ...\" [options]"
  echo ""
  echo "Required Arguments:"
  echo "  --source-commit-sha <sha>   The commit SHA the images are currently tagged with."
  echo "  --target-tags \"<tags>\"      A space-separated list of tags to apply."
  echo ""
  echo "Options:"
  echo "  --remote-images-file <path> Path to the file containing base image names (use this OR --remote-images)."
  echo "                              (default: ${DEFAULT_REMOTE_IMAGES_FILE} if neither option is specified)"
  echo "  --remote-images <images>    Newline-separated string containing base image names (use this OR --remote-images-file)."
  echo "  --noop                        Print the crane commands instead of executing them."
  echo "  -h, --help                    Show this help message"
}

# Function to read a value from the build.cfg file
# Usage: read_config <key_name>
read_config() {
    local key="$1"
    local value
    value=$(grep -m 1 "^${key}\s*=\s*" "${BUILD_CFG_PATH}" | cut -d'=' -f2 | sed 's/^[[:space:]]*//;s/[[:space:]]*$$//')
    if [[ -z "$value" ]]; then
        echo "Error: Key '${key}' not found or empty in ${BUILD_CFG_PATH}" >&2
        return 1
    fi
    echo "$value"
}

# --- Argument Parsing ---
REMOTE_IMAGES_FILE="" # Default to empty, will use default path later if needed
REMOTE_IMAGES_STRING="" # New variable for direct image list
SOURCE_COMMIT_SHA=""
TARGET_TAGS=""
NOOP=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remote-images-file)
      REMOTE_IMAGES_FILE="$2"
      shift 2
      ;;
    --remote-images) # New option
      REMOTE_IMAGES_STRING="$2"
      shift 2
      ;;
    --source-commit-sha)
      SOURCE_COMMIT_SHA="$2"
      shift 2
      ;;
    --target-tags)
      TARGET_TAGS="$2"
      shift 2
      ;;
    --noop)
      NOOP=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

# --- Input Validation ---
if [[ -z "$SOURCE_COMMIT_SHA" ]]; then
  echo "Error: --source-commit-sha is required" >&2
  usage
  exit 1
fi

if [[ -z "$TARGET_TAGS" ]]; then
  echo "Error: --target-tags is required" >&2
  usage
  exit 1
fi

# Validate remote image input source
if [[ -n "$REMOTE_IMAGES_STRING" && -n "$REMOTE_IMAGES_FILE" ]]; then
    echo "Error: Cannot use both --remote-images and --remote-images-file options." >&2
    usage
    exit 1
elif [[ -z "$REMOTE_IMAGES_STRING" && -z "$REMOTE_IMAGES_FILE" ]]; then
    # If neither is provided, use the default file path
    REMOTE_IMAGES_FILE="${DEFAULT_REMOTE_IMAGES_FILE}"
    echo "Info: Neither --remote-images nor --remote-images-file specified. Using default file: ${REMOTE_IMAGES_FILE}" >&2
elif [[ -n "$REMOTE_IMAGES_STRING" ]]; then
    echo "Info: Using remote images provided via --remote-images argument." >&2
    # Clear REMOTE_IMAGES_FILE just in case it was set to default initially but --remote-images was also given
    REMOTE_IMAGES_FILE=""
else # Only REMOTE_IMAGES_FILE is set (explicitly)
    echo "Info: Using remote images file specified via --remote-images-file: ${REMOTE_IMAGES_FILE}" >&2
fi

# --- Derive Short SHAs ---
SHORT_SOURCE_COMMIT_SHA="${SOURCE_COMMIT_SHA:0:7}"

# Validate file existence only if using file input
if [[ -n "$REMOTE_IMAGES_FILE" && ! -f "$REMOTE_IMAGES_FILE" ]]; then
    echo "Error: Remote images file not found: ${REMOTE_IMAGES_FILE}" >&2
    exit 1
fi

if [[ ! -x "$CRANE_PATH" ]] && [[ "$NOOP" == "false" ]]; then # Only check if crane exists if not noop
    echo "Error: crane executable not found or not executable: ${CRANE_PATH}" >&2
    echo "Please ensure crane is installed, e.g., by running 'make build/bin/crane'" >&2
    exit 1
fi

if [[ ! -x "$RESOLVE_TAGS_SCRIPT_PATH" ]]; then
    echo "Error: resolve-docker-tags.sh script not found or not executable: ${RESOLVE_TAGS_SCRIPT_PATH}" >&2
    exit 1
fi

if [[ ! -f "$BUILD_CFG_PATH" ]]; then
    echo "Error: Build config file not found: ${BUILD_CFG_PATH}" >&2
    exit 1
fi

# --- Main Logic ---

echo "--- Reading Configuration ---"
STABLE_BRANCHES=$(read_config "stable_branches")
if [[ $? -ne 0 ]]; then
    exit 1
fi
echo "Using stable branches: ${STABLE_BRANCHES}"

echo ""
echo "--- Target Tags ---"
echo "Provided target tags: ${TARGET_TAGS}"

echo ""
echo "--- Tagging Remote Images ---"
if [[ "$NOOP" == "true" ]]; then
    echo "*** NOOP mode enabled: Printing commands instead of executing them ***"
fi
source_image_tag="git-commit-${SHORT_SOURCE_COMMIT_SHA}"
echo "Source image tag: ${source_image_tag}"

tagging_errors=0

# --- Process Base Images ---
process_base_image() {
    local base_image="$1"
    source_image="${base_image}:${source_image_tag}"
    if [[ -z "$base_image" ]]; then
        return # Skip empty lines
    fi

    echo "Processing base image: ${base_image}"

    for target_tag in $TARGET_TAGS; do
        tag_command="'${CRANE_PATH}' tag '${source_image}' '${target_tag}'"

        if [[ "$NOOP" == "true" ]]; then
            echo "  [NOOP] Would run: ${tag_command}"
        else
            echo "  Attempting tag: ${source_image} -> ${target_tag}"
            if eval "${tag_command}"; then # Use eval to correctly handle quotes in paths/tags
                echo "    Success."
            else
                echo "    Error: Failed to apply tag ${target_tag} to ${base_image} (Source SHA: ${SHORT_SOURCE_COMMIT_SHA}). Check crane output above." >&2
                tagging_errors=$((tagging_errors + 1)) # Use arithmetic expansion
            fi
        fi
    done
    echo ""
}

# Choose input source based on validation logic result
if [[ -n "$REMOTE_IMAGES_STRING" ]]; then
    echo "Reading base images from --remote-images argument..."
    # Use a while loop to read newline-separated images from the string
    echo "${REMOTE_IMAGES_STRING}" | while IFS= read -r image_line || [[ -n "$image_line" ]]; do
        process_base_image "$image_line"
    done
elif [[ -n "$REMOTE_IMAGES_FILE" ]]; then
    echo "Reading base images from file: ${REMOTE_IMAGES_FILE}"
    while IFS= read -r image_line || [[ -n "$image_line" ]]; do
       process_base_image "$image_line"
    done < "${REMOTE_IMAGES_FILE}"
else
    # This case should ideally not be reached due to earlier validation, but added for safety
    echo "Error: No source for remote images specified or determined." >&2
    exit 1
fi

echo "--- Tagging Summary ---"
if [[ "$NOOP" == "true" ]]; then
    echo "NOOP mode finished. No changes were made."
    exit 0
fi

if [[ $tagging_errors -eq 0 ]]; then
    echo "All target tags applied successfully."
    exit 0
else
    echo "${tagging_errors} error(s) occurred during tagging." >&2
    exit 1
fi 