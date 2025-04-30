.PHONY: FORCE
.SECONDEXPANSION:

app_name:=$(shell basename `git rev-parse --show-toplevel`)

# Define a function to read values from any config file
# Usage: $(call read_config,config_file,key_name)
define read_config
$(shell grep -m 1 "^$(2)\s*=\s*" $(1) | cut -d'=' -f2 | sed 's/^[[:space:]]*//;s/[[:space:]]*$$//')
endef

# Reading config using the new function
docker_runtime_image:=$(call read_config,build.cfg,docker_runtime_image)
platforms:=$(call read_config,build.cfg,platforms)

comma:=,

# Detect OS and ARCH for host machine
os_lower := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ifeq ($(os_lower),darwin)
	detected_os ?= Darwin
else
	detected_os ?= Linux
endif

detected_arch ?= $(shell uname -m)
ifeq ($(detected_arch),x86_64)
	detected_arch := x86_64
else ifeq ($(detected_arch),aarch64)
	detected_arch := arm64
else ifeq ($(detected_arch),arm64)
	detected_arch := arm64
endif

# Tool Binaries (local to build directory)
bin_dir := $(CURDIR)/bin
tmp_dir := $(CURDIR)/tmp

# Crane Installation
crane_version := $(shell cat .crane-version)
crane_archive_name := go-containerregistry_$(detected_os)_$(detected_arch).tar.gz
crane_archive_url := https://github.com/google/go-containerregistry/releases/download/$(crane_version)/$(crane_archive_name)
crane_archive := $(tmp_dir)/$(crane_archive_name)
crane := $(bin_dir)/crane

$(tmp_dir):
	mkdir -p $(tmp_dir)

$(bin_dir):
	mkdir -p $(bin_dir)

$(crane_archive): .crane-version $(tmp_dir)
	@echo "Downloading crane v$(crane_version) for $(detected_os)/$(detected_arch)..."
	curl -sL $(crane_archive_url) -o $(crane_archive)

$(crane): $(crane_archive) $(bin_dir)
	@echo "Extracting crane binary..."
	tar -zxvf $(crane_archive) -C $(bin_dir)/ crane
	@echo "crane installed to $(crane)"
	touch $(crane) # Update timestamp for Make

# Target to ensure crane is installed
.PHONY: install-crane
install-crane: $(crane)

# platforms in config are in form linux/amd64,linux/arm64...
# transforming to dist/linux/amd64 dist/linux/arm64...
dist_platforms:=$(patsubst %,dist/%,$(subst $(comma), ,$(platforms)))

# Example: dist/linux/amd64
# If you care of the size, add -ldflags="-s -w"
# but keep in mind that profiling will be challenging.
# Disabled CGO explicitly speeds up the build. Remove if you need CGO.
dist/%: FORCE
	$(eval GOOS:=$(word 1,$(subst /, ,$*)))
	$(eval GOARCH:=$(word 2,$(subst /, ,$*)))
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -C .. \
			-tags=release \
			-o build/dist/$(GOOS)/$(GOARCH)/ ./cmd/...;

dist: FORCE
	@make -j4 $(dist_platforms)

.build-artifacts: dist
	@echo ./dist > $@

# Create a tarball of build artifacts
# Example: make build-artifacts.tar.gz
build-artifacts.tar.%: .build-artifacts
	rm -f $@
	tar -a -cf $@ $(shell cat $<)

.docker-artifacts: docker/.remote-images
	@rm -f $@
	@echo "docker/.remote-images" > $@
	@echo ./.git-meta >> $@

docker-artifacts.tar.%: .docker-artifacts
	rm -f $@
	tar -a -cf $@ $(shell cat $<)

# COMMIT_SHA is used since github may checkout on the temporary PR branch
# that will have a different commit SHA than the merge commit SHA
.git-meta/HEAD: FORCE
	@mkdir -p .git-meta
	@if [ -n "$$GIT_COMMIT_SHA" ]; then \
		echo "$$GIT_COMMIT_SHA" | cut -c1-7 > $@.tmp; \
	else \
		git rev-parse --short HEAD > $@.tmp; \
	fi
	@if cmp -s $@ $@.tmp; then \
		rm $@.tmp; \
	else \
		mv $@.tmp $@; \
	fi

# COMMIT_REF is used since github may checkout on the temporary PR branch
# that will always have HEAD as a ref name
.git-meta/ref: .git-meta/HEAD
	@if [ -n "$$GIT_REF" ]; then \
		echo "$$GIT_REF" > $@.tmp; \
	else \
		git rev-parse --abbrev-ref HEAD > $@.tmp; \
	fi
	@cmp -s $@.tmp $@ || mv $@.tmp $@
	@rm -f $@.tmp

.git-meta/repo-name: .git-meta/HEAD
	@basename $(shell git rev-parse --show-toplevel) > $@

.git-meta/remote-url: .git-meta/HEAD
	@git remote get-url origin | sed -E \
		-e 's|^git@([^:]+):|https://\1/|' \
		-e 's|^http:|https:|' \
		-e 's|\.git$$||' > $@.tmp
	@cmp -s $@.tmp $@ || mv $@.tmp $@
	@rm -f $@.tmp

.git-meta/repo-namespace: .git-meta/remote-url
	@cat .git-meta/remote-url | sed -E \
		-e 's|^https://[^/]+/||' \
		-e 's|/[^/]+$$||' > $@.tmp
	@cmp -s $@.tmp $@ || mv $@.tmp $@
	@rm -f $@.tmp

docker/.labels: .git-meta/HEAD .git-meta/repo-name .git-meta/remote-url .git-meta/ref
	@echo "org.opencontainers.image.created=\"$(shell git show -s --format=%cI HEAD)\"" > $@
	@echo "org.opencontainers.image.source=\"$(shell cat .git-meta/remote-url)\"" >> $@
	@echo "org.opencontainers.image.version=\"$(shell git describe --tags --always)\"" >> $@
	@echo "org.opencontainers.image.revision=\"$(shell cat .git-meta/HEAD)\"" >> $@
	@echo "org.opencontainers.image.ref.name=\"$(shell cat .git-meta/ref)\"" >> $@
	@echo "org.opencontainers.image.title=\"$(shell cat .git-meta/repo-name)\"" >> $@
	@echo "org.opencontainers.image.url=\"$(shell cat .git-meta/remote-url)\"" >> $@

docker/.local-args: docker/.labels
	@echo "--build-arg RUNTIME_IMAGE=$(call read_config,build.cfg,docker_runtime_image)" > $@
	@echo --platform $(platforms) >> $@
	@echo -f docker/Dockerfile >> $@
	@sed 's/^/--label /' docker/.labels >> $@
	@echo "../" >> $@

docker/.local-%-tags: .git-meta/HEAD .git-meta/repo-name .git-meta/ref
	$(eval registry := $(call read_config,build.cfg,docker_local_registry))
	$(eval image_base_name := $(registry)/$(shell cat .git-meta/repo-name)-$*)
	$(eval tag_names=$(shell ./scripts/resolve-docker-tags.sh \
		--latest \
		--commit-sha $(shell cat .git-meta/HEAD) \
		--git-ref $(shell cat .git-meta/ref) \
		--stable-branches $(call read_config,build.cfg,stable_branches) \
	))
	@echo $(tag_names) | tr ' ' '\n' | sed 's|^|--tag $(image_base_name):|' > $@

# Build local image
# Target must be in form: docker/.local-<binary>-image
# Example: make docker/.local-server-image
docker/.local-%-image: docker/.local-args docker/.local-%-tags
	@echo "Building local $* image"
	docker buildx build --load --build-arg TARGET_BINARY="$*" \
			--metadata-file $@ \
			--annotation org.opencontainers.image.description="Includes $* binary" \
			$(shell cat docker/.local-$*-tags) \
			$(shell cat docker/.local-args)

# Build all local images for each binary in dist/
# Fill use top folders from cmd folder to understand which binaries to build (wildcard)
.PHONY: docker/.local-images
docker/.local-images: $(patsubst %, docker/.local-%-image, $(notdir $(wildcard ../cmd/*)))
	@echo "Local images are ready: $?"

# Build image tags for remote registry
# Target must be in form: docker/.remote-<registry>-<binary>-tags
# Example: make docker/.remote-ghcr-server-tags
# The registry host is read from build.cfg under docker_<registry>_registry key
docker/.remote-%-tags: .git-meta/HEAD .git-meta/repo-name .git-meta/repo-namespace .git-meta/ref
	$(eval registry_name = $(word 1,$(subst -, ,$*)))
	$(eval target_binary = $(word 2,$(subst -, ,$*)))
	$(eval registry := $(call read_config,build.cfg,docker_$(registry_name)_registry))
	$(eval image_base_name := $(registry)/$(shell cat .git-meta/repo-namespace)/$(shell cat .git-meta/repo-name)-$(target_binary))
	$(eval tag_names=$(shell ./scripts/resolve-docker-tags.sh \
		--commit-sha $(shell cat .git-meta/HEAD) \
		--git-ref $(shell cat .git-meta/ref) \
		--stable-branches $(call read_config,build.cfg,stable_branches) \
	))
	@echo $(tag_names) | tr ' ' '\n' | sed 's|^|--tag $(image_base_name):|' > $@

# Push image to remote registry
# Target must be in form: docker/.remote-<registry>-<binary>-image
# Example: make docker/.remote-ghcr-server-image
# The registry host is read from build.cfg under docker_<registry>_registry_host key
docker/.remote-%-image: docker/.local-args docker/.remote-%-tags
	$(eval registry_name := $(word 1,$(subst -, ,$*)))
	$(eval target_binary := $(word 2,$(subst -, ,$*)))
	@echo "Building and pushing remote $* image for $(target_binary)"
	docker buildx build --push --build-arg TARGET_BINARY="$(target_binary)" \
			--debug \
			--metadata-file $@ \
			--annotation org.opencontainers.image.description="Includes $(target_binary) binary" \
			$(shell cat docker/.remote-$*-tags) \
			$(shell cat docker/.local-args)

# Push all images to remote registry
.PHONY: docker/remote-images
docker/.remote-images: .git-meta/repo-name .git-meta/repo-namespace $(eval docker_push_registries := $(call read_config,build.cfg,docker_push_registries))
docker/.remote-images: $(eval binaries := $(notdir $(wildcard ../cmd/*)))
docker/.remote-images: $(eval remote_image_targets := $(patsubst %, docker/.remote-%-image, $(foreach registry_name,$(subst $(comma), ,$(docker_push_registries)),$(foreach binary,$(binaries),$(registry_name)-$(binary)))))
docker/.remote-images: $(remote_image_targets)
	@echo "Writing unique base image names to docker/.remote-images"
	@rm -f docker/.remote-images docker/remote-images.tmp
	@touch docker/remote-images.tmp
	@$(foreach registry_name,$(subst $(comma), ,$(docker_push_registries)), \
		$(eval registry := $(call read_config,build.cfg,docker_$(registry_name)_registry)) \
		$(foreach binary,$(binaries), \
			echo "$(registry)/$(shell cat .git-meta/repo-namespace)/$(shell cat .git-meta/repo-name)-$(binary)" >> docker/remote-images.tmp; \
		) \
	)
	@sort -u docker/remote-images.tmp > docker/.remote-images
	@rm -f docker/remote-images.tmp
	@echo "Remote images pushed: $?"

# Target to generate a file containing the list of remote image base names
# without building/pushing the images.
docker/.remote-image-names: .git-meta/repo-name .git-meta/repo-namespace
	@echo "Generating list of remote image names to $@..."
	@rm -f $@ $@.tmp
	@touch $@.tmp
	$(eval docker_push_registries := $(call read_config,build.cfg,docker_push_registries))
	$(eval binaries := $(notdir $(wildcard ../cmd/*)))
	@$(foreach registry_name,$(subst $(comma), ,$(docker_push_registries)), \
		$(eval registry := $(call read_config,build.cfg,docker_$(registry_name)_registry)) \
		$(foreach binary,$(binaries), \
			echo "$(registry)/$(shell cat .git-meta/repo-namespace)/$(shell cat .git-meta/repo-name)-$(binary)" >> $@.tmp; \
		) \
	)
	@sort -u $@.tmp > $@
	@rm -f $@.tmp
	@echo "List of remote image names written to $@"

.PHONY: clean docker/clean dist/clean

docker/clean:
	rm -f docker/.local-* docker/.remote-* docker/.labels

dist/clean:
	rm -rf dist

clean: docker/clean dist/clean
	rm -rf .git-meta
	rm -f .build-artifacts
	rm -f build-artifacts.tar.*
	# Clean crane artifacts
	rm -rf $(bin_dir)
	rm -rf $(tmp_dir)

.PHONY: test
test:
	@echo "Running resolve-docker-tags self-test"
	scripts/resolve-docker-tags.sh --self-test

	@echo "Running python script tests"
	python -m unittest discover -s scripts/tests -p "test_*.py"