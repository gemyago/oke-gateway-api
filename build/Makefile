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

# platforms in config are in form linux/amd64,linux/arm64...
# transforming to dist/linux/amd64 dist/linux/arm64...
dist_platforms:=$(patsubst %,dist/%,$(subst $(comma), ,$(platforms)))

# Example: dist/linux/amd64
dist/%: FORCE
	$(eval GOOS:=$(word 1,$(subst /, ,$*)))
	$(eval GOARCH:=$(word 2,$(subst /, ,$*)))
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -C .. \
			-tags=release \
			-o build/dist/$(GOOS)/$(GOARCH)/ ./cmd/...;

dist: $(dist_platforms)

.build-artifacts: dist
	@echo ./dist > $@

# Create a tarball of build artifacts
# Example: make build-artifacts.tar.gz
build-artifacts.tar.%: .build-artifacts
	rm -f $@
	tar -a -cf $@ $(shell cat $<)

.git-meta/HEAD: FORCE
	@mkdir -p .git-meta
	@git rev-parse --short HEAD > $@.tmp
	@if cmp -s $@ $@.tmp; then \
		rm $@.tmp; \
	else \
		mv $@.tmp $@; \
	fi

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

docker/.labels: .git-meta/HEAD .git-meta/repo-name .git-meta/remote-url
	@echo "org.opencontainers.image.created=\"$(shell git show -s --format=%cI HEAD)\"" > $@
	@echo "org.opencontainers.image.source=\"$(shell cat .git-meta/remote-url)\"" >> $@
	@echo "org.opencontainers.image.version=\"$(shell git describe --tags --always)\"" >> $@
	@echo "org.opencontainers.image.revision=\"$(shell cat .git-meta/HEAD)\"" >> $@
	@echo "org.opencontainers.image.ref.name=\"$(shell git rev-parse --abbrev-ref HEAD)\"" >> $@
	@echo "org.opencontainers.image.title=\"$(shell cat .git-meta/repo-name)\"" >> $@
	@echo "org.opencontainers.image.url=\"$(shell cat .git-meta/remote-url)\"" >> $@

docker/.local-args: docker/.labels
	@echo "--build-arg RUNTIME_IMAGE=$(call read_config,build.cfg,docker_runtime_image)" > $@
	@echo --platform $(platforms) >> $@
	@echo -f docker/Dockerfile >> $@
	@sed 's/^/--label /' docker/.labels >> $@
	@echo "../" >> $@

docker/.local-%-tags: .git-meta/HEAD .git-meta/repo-name
	$(eval registry := $(call read_config,build.cfg,docker_local_registry))
	$(eval image_base_name := $(registry)/$(shell cat .git-meta/repo-name)-$*)
	$(eval tag_names=$(shell ./scripts/resolve-docker-tags.sh \
		--latest \
		--commit-sha $(shell cat .git-meta/HEAD) \
		--git-ref $(shell git rev-parse --abbrev-ref HEAD) \
		--stable-branches $(call read_config,build.cfg,stable_branches) \
	))
	@echo $(tag_names) | tr ' ' '\n' | sed 's|^|-t $(image_base_name):|' > $@

# Build local image
# Target must be in form: docker/.local-<binary>-image
# Example: make docker/.local-server-image
docker/.local-%-image: docker/.local-args docker/.local-%-tags
	@echo "Building local $* image"
	docker build --build-arg TARGET_BINARY="$*" \
			--iidfile $@ \
			--annotation org.opencontainers.image.description="Includes $* binary" \
			$(shell cat docker/.local-$*-tags) \
			$(shell cat docker/.local-args)

# Build all local images for each binary in dist/
# Fill use top folders from cmd folder to understand which binaries to build (wildcard)
.PHONY: docker/local-images
docker/local-images: $(patsubst %, docker/.local-%-image, $(notdir $(wildcard ../cmd/*)))
	@echo "Local images are ready: $?"

# Build image tags for remote registry
# Target must be in form: docker/.remote-<registry>-<binary>-tags
# Example: make docker/.remote-ghcr-server-tags
# The registry host is read from build.cfg under docker_<registry>_registry key
docker/.remote-%-tags: .git-meta/HEAD .git-meta/repo-name .git-meta/repo-namespace
	$(eval registry_name = $(word 1,$(subst -, ,$*)))
	$(eval target_binary = $(word 2,$(subst -, ,$*)))
	$(eval registry := $(call read_config,build.cfg,docker_$(registry_name)_registry))
	$(eval image_base_name := $(registry)/$(shell cat .git-meta/repo-namespace)/$(shell cat .git-meta/repo-name)-$(target_binary))
	$(eval tag_names=$(shell ./scripts/resolve-docker-tags.sh \
		--commit-sha $(shell cat .git-meta/HEAD) \
		--git-ref $(shell git rev-parse --abbrev-ref HEAD) \
		--stable-branches $(call read_config,build.cfg,stable_branches) \
	))
	@echo $(tag_names) | tr ' ' '\n' | sed 's|^|$(image_base_name):|' > $@

# Push image to remote registry
# Target must be in form: docker/.remote-<registry>-<binary>-image
# Example: make docker/.remote-ghcr-server-image
# The registry host is read from build.cfg under docker_<registry>_registry_host key
docker/.remote-%-image: docker/.local-$$(word 2, $$(subst -, ,$$*))-image docker/.remote-%-tags
	$(eval registry_name := $(word 1,$(subst -, ,$*)))
	$(eval target_binary := $(word 2,$(subst -, ,$*)))
	$(eval local_image_id := $(shell cat docker/.local-$(target_binary)-image))
	$(eval remote_tags := $(shell cat docker/.remote-$*-tags))
	@for tag in $$(cat docker/.remote-$*-tags); do \
			echo "Pushing $(local_image_id) as $$tag"; \
			docker tag $(local_image_id) $$tag; \
			docker push $$tag; \
	done
	docker inspect $(local_image_id) > $@

# Push all images to remote registry
.PHONY: docker/remote-images
docker/remote-images: $(eval docker_push_registries := $(call read_config,build.cfg,docker_push_registries))
docker/remote-images: $(eval binaries := $(notdir $(wildcard ../cmd/*)))
docker/remote-images: $(patsubst %, docker/.remote-%-image, $(foreach registry,$(subst $(comma), ,$(docker_push_registries)),$(foreach binary,$(binaries),$(registry)-$(binary))))
		@echo "Remote images pushed: $?"

.PHONY: clean docker/clean dist/clean

docker/clean:
	rm -f docker/.local-* docker/.remote-* docker/.labels

dist/clean:
	rm -rf dist

clean: docker/clean dist/clean
	rm -rf .git-meta
	rm -f .build-artifacts
	rm -f build-artifacts.tar.*

.PHONY: test
test:
	@echo "Running resolve-docker-tags self-test"
	scripts/resolve-docker-tags.sh --self-test

	@echo "Running python script tests"
	python -m unittest discover -s scripts/tests -p "test_*.py"