# Command to commit changes.

The fact that AI has this instruction in the context means AI must **automatically** start executing it.

* All commands should be run from a repo root.
* You will be given a list of files to commit.
* Commit all updated files if not otherwise specified. In this case use `git add .` from a repo root as a first step to stage all updated files.
* If user requested to commit specific files, use `git add <file1> <file2> ...` to stage specific files only. Otherwise, commit all updated files.
* When committing, make sure to provide a sensible message. Figure out the message from chat history. Make message short and descriptive.
* If no relevant chat history available, use `git diff --staged | head -n 1000` to understand changes and figure-out a sensible message.

Do **NOT** do any other verification or actions unrelated to this instruction. You **SHOULD** just commit the changes as specified here.
