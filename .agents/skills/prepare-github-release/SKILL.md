---
name: prepare-github-release
description: Prepare and publish the next GitHub release for this repository. Use when the user asks to do a patch, minor, or major release, or to prepare and publish a named release version through the repository's GitHub Actions release workflow.
---

# Prepare GitHub Release

Use `gh` against the current repository. Treat publishing a release as authorized by a request to do a release; do not create local version-bump commits.

## Determine the release version

1. Find the latest published, non-prerelease release:

   ```sh
   gh release list --exclude-drafts --exclude-pre-releases --limit 1 \
     --json tagName,publishedAt
   ```

2. Require a tag in the form `vMAJOR.MINOR.PATCH`. Compute the requested semantic-version increment:
   - patch: increment PATCH;
   - minor: increment MINOR and set PATCH to zero;
   - major: increment MAJOR and set MINOR and PATCH to zero.
3. If the user supplied an explicit version, require that it is the valid next version for the requested release type.

## Prepare the draft release

Dispatch the `Prepare Release` workflow from `main` with the target tag:

```sh
gh workflow run prepare-release.yml --ref main -f release_name="vMAJOR.MINOR.PATCH"
```

Wait for the dispatched run and stop if it fails:

```sh
run_id=$(gh run list --workflow prepare-release.yml --event workflow_dispatch \
  --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$run_id" --exit-status
```

Locate the draft PR by head branch `release/vMAJOR.MINOR.PATCH` and base branch `main`.

## Merge only a green release PR

Wait for every required pull-request check to pass:

```sh
gh pr checks <pr-number> --watch --fail-fast
```

If a check fails, do not merge or publish. Once all checks pass, squash-merge the release PR into `main`.

```sh
gh pr merge <pr-number> --squash
```

## Wait for image promotion

The merged release PR triggers `promote-docker-images.yml`, which promotes the
release-branch images to `main`. Wait for that workflow to succeed before
publishing the release; otherwise `release-flow.yml` cannot tag the release
images.

```sh
run_id=""
while [ -z "$run_id" ]; do
  run_id=$(gh run list --workflow promote-docker-images.yml --event pull_request \
    --limit 20 --json databaseId,headBranch \
    --jq '[.[] | select(.headBranch == "release/vMAJOR.MINOR.PATCH")][0].databaseId // empty')
  [ -n "$run_id" ] || sleep 5
done
gh run watch "$run_id" --exit-status
```

Stop if image promotion fails. Do not publish the draft release.

## Publish from main

The draft release created by the workflow initially targets `release/vMAJOR.MINOR.PATCH`. After the squash merge, retarget the draft release to `main` before publishing:

```sh
gh release edit "vMAJOR.MINOR.PATCH" --target main
gh release edit "vMAJOR.MINOR.PATCH" --draft=false
```

Wait for the release-triggered `release-flow.yml` run to finish successfully:

```sh
run_id=$(gh run list --workflow release-flow.yml --event release --limit 1 \
  --json databaseId --jq '.[0].databaseId')
gh run watch "$run_id" --exit-status
```

Report the release URL, tag, merged PR, and workflow result. Do not attempt a replacement release or retag manually after a failure.
