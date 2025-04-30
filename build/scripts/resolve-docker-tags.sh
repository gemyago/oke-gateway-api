#!/usr/bin/env bash

# Docker Tag Resolution Logic:
# - For git tags: Uses the git-tag-<tag name>
# - For non-stable branches: Uses branch name and git-commit-<commit-sha>
# - For stable branches: Uses latest-<branch-name> and git-commit-<commit-sha>
# - When --latest is provided: Also includes the "latest" tag
# All generated tags are sanitized to match Docker's tag format requirements.

set -euo pipefail

# Function to print usage information
usage() {
  echo "Usage: $0 [options]"
  echo "Options:"
  echo "  --stable-branches <branches>  Comma-separated list of stable branches"
  echo "  --git-ref <ref>               Git reference (branch or tag)"
  echo "  --commit-sha <sha>            Git commit SHA"
  echo "  --latest                   Include 'latest' tag in the output"
  echo "  --self-test                   Run internal tests"
  echo "  -h, --help                    Show this help message"
}

# Function to sanitize Docker tag according to the pattern /[\w][\w.-]{0,127}/
# First character must be alphanumeric, remaining can be alphanumeric, dot, or dash
sanitize_docker_tag() {
  local tag="$1"
  local sanitized=""
  
  if [[ "${tag:0:1}" =~ [a-zA-Z0-9] ]]; then
    sanitized="${tag:0:1}"
  else
    sanitized="x-"
  fi
  
  local remaining="${tag:1}"
  local sanitized_remaining=$(echo "$remaining" | sed -E 's/[^a-zA-Z0-9.-]/-/g')
  
  sanitized="${sanitized}${sanitized_remaining}"
  echo "${sanitized:0:128}"
}

# Function to process a potentially SemVer tag and return derived tags
process_semver_tag() {
  local tag_name="$1"
  local semver_regex='^v?([0-9]+)(\.[0-9]+)?(\.[0-9]+)?(.*)$' # Regex vX(.Y)(.Z)(suffix)
  local generated_tags=""

  if [[ "$tag_name" =~ $semver_regex ]]; then
    # BASH_REMATCH indices: 1=major, 2=.minor, 3=.patch, 4=suffix
    local suffix="${BASH_REMATCH[4]}"

    # Construct main SemVer tag (vX.Y.Z or vX.Y or vX) + suffix if exists
    local main_semver_tag="v${BASH_REMATCH[1]}"
    local minor_num=""
    local patch_num=""
    [[ -n "${BASH_REMATCH[2]}" ]] && minor_num="${BASH_REMATCH[2]#.}"
    [[ -n "${BASH_REMATCH[3]}" ]] && patch_num="${BASH_REMATCH[3]#.}"

    if [[ -n "$minor_num" ]]; then
      main_semver_tag="${main_semver_tag}.${minor_num}"
      if [[ -n "$patch_num" ]]; then
        main_semver_tag="${main_semver_tag}.${patch_num}"
      fi
    fi
    main_semver_tag="${main_semver_tag}${suffix}" # Append suffix

    # If there is a pre-release suffix (starting with -), only return the full tag
    if [[ -n "$suffix" && "$suffix" == -* ]]; then
        generated_tags="$(sanitize_docker_tag "$main_semver_tag")"
    else
      # No pre-release suffix, generate broader tags
      local semver_tags=()
      # Use the tag *without* the suffix for broader tag generation
      local base_semver_tag="${main_semver_tag%$suffix}"
      semver_tags+=( "$(sanitize_docker_tag "$base_semver_tag")" ) # Add vX.Y.Z or vX.Y or vX

      # Add vX.Y-latest tag if minor exists
      if [[ -n "$minor_num" ]]; then
        local tag_v_major="v${BASH_REMATCH[1]}"
        local tag_v_minor_latest="${tag_v_major}.${minor_num}-latest"
        semver_tags+=( "$(sanitize_docker_tag "$tag_v_minor_latest")" )
      fi

      # Add vX-latest tag
      local tag_v_major_latest="v${BASH_REMATCH[1]}-latest"
      semver_tags+=( "$(sanitize_docker_tag "$tag_v_major_latest")" )

      # Join semver tags
      generated_tags=$(IFS=' '; echo "${semver_tags[*]}")
    fi
  fi
  echo "$generated_tags"
}

# Function to resolve Docker tags based on git reference
resolve_docker_tags() {
  local ref=$1
  local sha=$2
  local stable_branches=$3
  local is_latest=${4:-false}
  local tags=""

  # Normalize the ref (handle both refs/heads/branch and branch format)
  if [[ $ref == refs/tags/* ]]; then
    # Tag reference
    local tag_name="${ref#refs/tags/}"
    tags="$(sanitize_docker_tag "git-tag-$tag_name")" # Always include original git-tag

    local semver_tags=$(process_semver_tag "$tag_name")
    [[ -n "$semver_tags" ]] && tags="$tags $semver_tags"
    
    if [[ "$is_latest" == "true" ]]; then
      tags="$tags latest"
    fi
  elif [[ $ref == refs/heads/* ]]; then
    # Branch reference with refs/heads/ prefix
    local branch_name="${ref#refs/heads/}"
    process_branch_tags "$branch_name" "$sha" "$stable_branches" "$is_latest"
    return $?
  else
    # Assume it's a branch name without refs/heads/ prefix
    # Check if it's actually a tag ref without the prefix
    if [[ $ref == tags/* ]]; then
      local tag_name="${ref#tags/}"
      tags="$(sanitize_docker_tag "git-tag-$tag_name")" # Always include original git-tag

      local semver_tags=$(process_semver_tag "$tag_name")
      [[ -n "$semver_tags" ]] && tags="$tags $semver_tags"

      if [[ "$is_latest" == "true" ]]; then
        tags="$tags latest"
      fi
    else
      process_branch_tags "$ref" "$sha" "$stable_branches" "$is_latest"
      return $?
    fi
  fi

  echo "$tags"
}

# Helper function to process branch tags
process_branch_tags() {
  local branch_name=$1
  local sha=$2
  local stable_branches=$3
  local is_latest=${4:-false}
  local is_stable=false
  local sanitized_branch="$(sanitize_docker_tag "$branch_name")"
  local sanitized_commit="git-commit-$(sanitize_docker_tag "${sha:0:7}")"
  
  # Check if branch is in the list of stable branches
  IFS=',' read -ra STABLE_BRANCHES <<< "$stable_branches"
  for stable in "${STABLE_BRANCHES[@]}"; do
    if [[ "$stable" == "$branch_name" ]]; then
      is_stable=true
      break
    fi
  done
  
  if [[ "$is_stable" == "true" ]]; then
    # Stable branches: latest-<branch-name>
    if [[ "$is_latest" == "true" ]]; then
      echo "latest-${sanitized_branch} latest"
    else
      echo "latest-${sanitized_branch}"
    fi
  else
    # Non-stable branches: <branch-name> and git-commit-<commit-sha>
    if [[ "$is_latest" == "true" ]]; then
      echo "${sanitized_branch} ${sanitized_commit} latest"
    else
      echo "${sanitized_branch} ${sanitized_commit}"
    fi
  fi
}

run_tests() {
  assert() {
    local actual="$1"
    local expected="$2"
    local message="$3"
    
    if [[ "$actual" == "$expected" ]]; then
      echo "PASS: $message"
      return 0
    else
      echo "FAIL: $message"
      echo "  Expected: '$expected'"
      echo "  Actual:   '$actual'"
      return 1
    fi
  }

  echo "Running self-tests..."
  local failures=0
  local test_result
  
  echo ""
  echo "Running sanitize_docker_tag tests:"
  test_result=$(sanitize_docker_tag "valid-tag")
  assert "$test_result" "valid-tag" "Simple valid tag" || ((failures++))
  
  test_result=$(sanitize_docker_tag "_invalid-first-char")
  assert "$test_result" "x-invalid-first-char" "Invalid first character" || ((failures++))
  
  test_result=$(sanitize_docker_tag "branch/with/slashes")
  assert "$test_result" "branch-with-slashes" "Replace slashes with dashes" || ((failures++))
  
  test_result=$(sanitize_docker_tag "tag@with#special&chars")
  assert "$test_result" "tag-with-special-chars" "Replace special chars with dashes" || ((failures++))
  
  test_result=$(sanitize_docker_tag "very-long-tag-$(printf '%0.s-' {1..150})")
  assert "${#test_result}" "128" "Truncate long tag to 128 chars" || ((failures++))
  
  local resolve_result
  local commit_sha="abc1234567890"
  local short_commit_sha="${commit_sha:0:7}" # Use the first 7 characters for tests

  echo ""
  echo "Running resolve_docker_tags tests:"
  # Test with refs/tags prefix
  resolve_result=$(resolve_docker_tags "refs/tags/v1.2.3" "$commit_sha" "main,develop" "false")
  assert "$resolve_result" "git-tag-v1.2.3 v1.2.3 v1.2-latest v1-latest" "SemVer tag (vX.Y.Z)" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/tags/1.2.3" "$commit_sha" "main,develop" "false")
  assert "$resolve_result" "git-tag-1.2.3 v1.2.3 v1.2-latest v1-latest" "SemVer tag (X.Y.Z)" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/tags/v1.2" "$commit_sha" "main,develop" "false")
  assert "$resolve_result" "git-tag-v1.2 v1.2 v1.2-latest v1-latest" "SemVer tag (vX.Y)" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/tags/v1" "$commit_sha" "main,develop" "false")
  assert "$resolve_result" "git-tag-v1 v1 v1-latest" "SemVer tag (vX)" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/tags/v1.2.3-alpha1" "$commit_sha" "main,develop" "false")
  assert "$resolve_result" "git-tag-v1.2.3-alpha1 v1.2.3-alpha1" "Pre-release SemVer tag (vX.Y.Z-suffix)" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/tags/my-tag" "$commit_sha" "main,develop" "false")
  assert "$resolve_result" "git-tag-my-tag" "Non-SemVer tag" || ((failures++))
  
  # Test with refs/heads prefix
  resolve_result=$(resolve_docker_tags "refs/heads/feature/xyz" "$commit_sha" "main,develop" "false")
  assert "$resolve_result" "feature-xyz git-commit-$short_commit_sha" "Non-stable branch resolution with refs/heads prefix" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/heads/main" "$commit_sha" "main,develop" "false")
  assert "$resolve_result" "latest-main" "Stable branch resolution with refs/heads prefix" || ((failures++))
  
  # Test without refs prefix
  resolve_result=$(resolve_docker_tags "feature/xyz" "$commit_sha" "main,develop")
  assert "$resolve_result" "feature-xyz git-commit-$short_commit_sha" "Non-stable branch resolution without refs prefix" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "main" "$commit_sha" "main,develop")
  assert "$resolve_result" "latest-main" "Stable branch resolution without refs prefix" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "tags/v1.0.0" "$commit_sha" "main,develop")
  assert "$resolve_result" "git-tag-v1.0.0 v1.0.0 v1.0-latest v1-latest" "SemVer tag resolution with tags/ prefix" || ((failures++))
  
  # Test with special characters
  resolve_result=$(resolve_docker_tags "refs/tags/v1.0.0@special" "$commit_sha" "main,develop")
  assert "$resolve_result" "git-tag-v1.0.0-special v1.0.0 v1.0-latest v1-latest" "SemVer Tag with special characters in suffix" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "feature/special@chars#here" "$commit_sha" "main,develop")
  assert "$resolve_result" "feature-special-chars-here git-commit-$short_commit_sha" "Branch with special characters without refs prefix" || ((failures++))
  
  # Test --latest flag
  resolve_result=$(resolve_docker_tags "refs/tags/v1.2.3" "$commit_sha" "main,develop" "true")
  assert "$resolve_result" "git-tag-v1.2.3 v1.2.3 v1.2-latest v1-latest latest" "SemVer Tag resolution with is-latest flag" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/tags/my-tag" "$commit_sha" "main,develop" "true")
  assert "$resolve_result" "git-tag-my-tag latest" "Non-SemVer Tag resolution with is-latest flag" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/heads/feature/xyz" "$commit_sha" "main,develop" "true")
  assert "$resolve_result" "feature-xyz git-commit-$short_commit_sha latest" "Non-stable branch with is-latest flag" || ((failures++))
  
  resolve_result=$(resolve_docker_tags "refs/heads/main" "$commit_sha" "main,develop" "true")
  assert "$resolve_result" "latest-main latest" "Stable branch with is-latest flag" || ((failures++))
  
  if [[ $failures -eq 0 ]]; then
    echo ""
    echo "All tests passed!"
    return 0
  else
    echo ""
    echo "$failures test(s) failed."
    return 1
  fi
}

STABLE_BRANCHES=""
GIT_REF=""
COMMIT_SHA=""
SELF_TEST=false
IS_LATEST=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stable-branches)
      STABLE_BRANCHES="$2"
      shift 2
      ;;
    --git-ref)
      GIT_REF="$2"
      shift 2
      ;;
    --commit-sha)
      COMMIT_SHA="$2"
      shift 2
      ;;
    --latest)
      IS_LATEST=true
      shift
      ;;
    --self-test)
      SELF_TEST=true
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

if $SELF_TEST; then
  run_tests
  exit $?
fi

if [[ -z "$GIT_REF" ]]; then
  echo "Error: --git-ref is required"
  usage
  exit 1
fi

if [[ -z "$COMMIT_SHA" ]]; then
  echo "Error: --commit-sha is required"
  usage
  exit 1
fi

if [[ -z "$STABLE_BRANCHES" ]]; then
  echo "Error: --stable-branches is required"
  usage
  exit 1
fi

tags=$(resolve_docker_tags "$GIT_REF" "$COMMIT_SHA" "$STABLE_BRANCHES" "$IS_LATEST")
echo "$tags"