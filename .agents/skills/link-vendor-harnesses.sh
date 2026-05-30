#!/usr/bin/env bash
# Symlink each skill under .agents/skills/ into vendor harness skill dirs
# (.codex/skills, .opencode/skills, .cursor/skills) and force-add them to git.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILLS_SRC="$SCRIPT_DIR"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if command -v /opt/homebrew/bin/git >/dev/null 2>&1; then
  GIT=/opt/homebrew/bin/git
else
  GIT=git
fi

# Vendor harness roots (each exposes skills at <harness>/skills/<name>)
VENDOR_HARNESSES=(
  .codex
  .opencode
  .cursor
)

link_target="../../.agents/skills"

ensure_harness_skills_dir() {
  local harness="$1"
  local skills_dir="$REPO_ROOT/$harness/skills"

  if [[ -L "$skills_dir" ]]; then
    rm "$skills_dir"
    "$GIT" -C "$REPO_ROOT" rm -f --ignore-unmatch "$harness/skills" 2>/dev/null || true
  fi

  mkdir -p "$skills_dir"
}

for harness in "${VENDOR_HARNESSES[@]}"; do
  ensure_harness_skills_dir "$harness"
done

shopt -s nullglob
for skill_path in "$SKILLS_SRC"/*/; do
  skill_name="$(basename "$skill_path")"
  [[ -d "$skill_path" ]] || continue

  for harness in "${VENDOR_HARNESSES[@]}"; do
    skills_dir="$REPO_ROOT/$harness/skills"
    link_path="$skills_dir/$skill_name"

    ln -sfn "${link_target}/${skill_name}" "$link_path"
    "$GIT" -C "$REPO_ROOT" add -f "$harness/skills/$skill_name"
  done
done

echo "Linked skills from $SKILLS_SRC into vendor harnesses: ${VENDOR_HARNESSES[*]}"
