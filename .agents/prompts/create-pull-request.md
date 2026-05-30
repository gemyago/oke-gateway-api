# Use this instruction to create pull request

The fact that AI has this instruction in the context means AI must **automatically** start executing it.

1. Look on a commit history between a base (if not mentioned otherwise, use **main**). You will need to run command like below
```bash
# Make sure remote is up to date
git fetch origin

# to get current branch, you will use it in step 5
git branch

# git log
git log origin/<base branch>...HEAD --oneline | cat
```

2. Review commit history and come up with a sensible PR title
3. Review commit history and come up with a sensible PR description, it should follow the following format:
  * Short change description 1
  * Short change description 2
  * ...
4. Prepare PR title and description in the following format:
  ```md
  **PR title**:
  <PR title>
  ---
  **PR description**:
  <PR description>
  ```
5. Push pending changes and create a PR with a command below:
```bash
git push origin <current branch> --set-upstream

gh pr create --title "<PR title>" --body "<PR description>" --base <base branch> --head <current branch>
```
6. Show the PR to the user as a URL so user can click it, as well as full URL for copying.
