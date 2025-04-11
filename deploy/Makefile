UNAME_S := $(shell uname -s)
UNAME_P = $(shell uname -p)
ifeq ($(UNAME_S),Linux)
	os=linux
else
	os=darwin
endif
ifeq ($(UNAME_P),arm)
	arch=arm64
else
	arch=amd64
endif

tar_version := $(shell tar --version)
ifeq ($(findstring GNU tar,$(tar_version)),GNU tar)
	tar_ignore_arg=--ignore-failed-read
	tar_absolute_names=--absolute-names
else
	tar_ignore_arg=
	tar_absolute_names=
endif

bin=bin
tmp=./tmp

# See https://github.com/helm/helm/releases for available versions
helm_version=$(shell cat .helm-version)
helm_archive_name=helm-v$(helm_version)-$(os)-$(arch).tar.gz
helm_archive=$(tmp)/$(helm_archive_name)
helm=$(bin)/helm

# Deploying to local by default
DEPLOY_ENV ?= default

$(tmp):
	mkdir -p $(tmp)

$(bin):
	mkdir -p $(bin)

$(helm_archive): .helm-version $(tmp)
	curl https://get.helm.sh/$(helm_archive_name) -o $(helm_archive)

$(helm): $(helm_archive) $(bin)
	tar -C $(tmp) -xvf $(helm_archive)
	cp $(tmp)/$(os)-$(arch)/helm $(helm)
	touch $(helm)

.PHONY: tools
tools: $(helm)