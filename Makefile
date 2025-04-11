.PHONY: tools test cmd

cover_dir=.cover
cover_profile=$(cover_dir)/profile.out
cover_html=$(cover_dir)/coverage.html

.DEFAULT_GOAL := all

all: test

bin/golangci-lint: .golangci-version
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s $(shell cat .golangci-version)

lint: bin/golangci-lint
	bin/golangci-lint run

$(cover_dir):
	mkdir -p $(cover_dir)

dist/bin: 
	go build \
		-tags=release \
		-o dist/bin/ ./cmd/...;

go_path=$(shell go env GOPATH)
go-test-coverage=$(go_path)/bin/go-test-coverage

$(go-test-coverage):
	go install github.com/vladopajic/go-test-coverage/v2@latest

.PHONY: $(cover_profile)
$(cover_profile): $(cover_dir)
	TZ=US/Alaska go test -shuffle=on -failfast -coverpkg=./internal/...,./cmd/... -coverprofile=$(cover_profile) -covermode=atomic ./...

test: $(go-test-coverage) $(cover_profile)
	go tool cover -html=$(cover_profile) -o $(cover_html)
	@echo "Test coverage report: $(shell realpath $(cover_html))"
	$(go-test-coverage) --badge-file-name $(cover_dir)/coverage.svg --config .testcoverage.yaml --profile $(cover_profile)

$(cover_dir)/repo-name-with-owner.txt:
	gh repo view --json nameWithOwner -q .nameWithOwner > $@

$(cover_dir)/coverage.%.blob-sha: $(cover_dir)/repo-name-with-owner.txt
	gh api \
		--method GET \
		-H "Accept: application/vnd.github+json" \
		-H "X-GitHub-Api-Version: 2022-11-28" \
		/repos/$(shell cat $(cover_dir)/repo-name-with-owner.txt)/contents/coverage/golang-coverage.$*?ref=test-artifacts \
		| jq -jr '.sha' > $@

$(cover_dir)/coverage.%.gh-cli-body.json: $(cover_dir)/coverage.% $(cover_dir)/coverage.%.blob-sha
	@echo "{" > $@
	@echo "\"branch\": \"test-artifacts\"," >> $@
	@printf "\"sha\": \"">> $@
	@cat $(cover_dir)/coverage.$*.blob-sha >> $@
	@printf "\",\n">> $@
	@echo "\"message\": \"Updating golang coverage.$*\",">> $@
	@printf "\"content\": \"">> $@
	@base64 -i $< | tr -d '\n' >> $@
	@printf "\"\n}">> $@

# Orphan branch will need to be created prior to running this
# git checkout --orphan test-artifacts
# git rm -rf .
# rm -f .gitignore
# echo '# Test Artifacts' > README.md
# git add README.md
# git commit -m 'init'
# git push origin test-artifacts
.PHONY: push-test-artifacts
push-test-artifacts: $(cover_dir)/coverage.svg.gh-cli-body.json $(cover_dir)/coverage.html.gh-cli-body.json $(cover_dir)/repo-name-with-owner.txt
	@gh api \
		--method PUT \
		-H "Accept: application/vnd.github+json" \
		-H "X-GitHub-Api-Version: 2022-11-28" \
		/repos/$(shell cat $(cover_dir)/repo-name-with-owner.txt)/contents/coverage/golang-coverage.svg \
		--input $(cover_dir)/coverage.svg.gh-cli-body.json
	@gh api \
		--method PUT \
		-H "Accept: application/vnd.github+json" \
		-H "X-GitHub-Api-Version: 2022-11-28" \
		/repos/$(shell cat $(cover_dir)/repo-name-with-owner.txt)/contents/coverage/golang-coverage.html \
		--input $(cover_dir)/coverage.html.gh-cli-body.json