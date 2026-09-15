# n5-fangov build. Plain make + dpkg-deb, no debhelper. Needs Go 1.25 (or run
# inside the golang:1.25-alpine container via tools/remote-go.ps1).
#
#   make build   -> dist/n5-fangov (linux/amd64, static, stripped)
#   make deb     -> dist/n5-fangov_<version>_amd64.deb (no conffile: the config is
#                   written by `n5-fangov setup`; the example goes to /usr/share/doc)
#   make check   -> go vet + go test
#   make clean

MODULE   := github.com/SirRenix/n5-fangov
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
# Debian version: strip a leading v, "-" is not allowed in upstream versions
DEBVER   := $(shell echo '$(VERSION)' | sed -e 's/^v//' -e 's/-/+/g')
ARCH     ?= amd64
LDFLAGS  := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)
DIST     := dist
PKGDIR   := $(DIST)/pkg
DEB      := $(DIST)/n5-fangov_$(DEBVER)_$(ARCH).deb

export CGO_ENABLED = 0
export GOOS        = linux
export GOARCH      = $(ARCH)

.PHONY: build check deb clean version

build:
	mkdir -p $(DIST)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/n5-fangov ./cmd/n5-fangov

check:
	go vet ./...
	go test ./...

version:
	@echo $(VERSION) '(deb: $(DEBVER))'

deb: build
	rm -rf $(PKGDIR)
	install -d -m 0755 $(PKGDIR)/DEBIAN \
	    $(PKGDIR)/usr/bin \
	    $(PKGDIR)/usr/libexec/n5-fangov \
	    $(PKGDIR)/lib/systemd/system \
	    $(PKGDIR)/etc/n5-fangov/presets \
	    $(PKGDIR)/usr/share/n5-fangov/pve-notification \
	    $(PKGDIR)/usr/share/doc/n5-fangov
	install -d -m 0750 $(PKGDIR)/var/log/n5-fangov
	install -m 0755 $(DIST)/n5-fangov                  $(PKGDIR)/usr/bin/n5-fangov
	install -m 0755 deploy/n5-fangov-onfailure         $(PKGDIR)/usr/libexec/n5-fangov/n5-fangov-onfailure
	install -m 0644 deploy/n5-fangov.service           $(PKGDIR)/lib/systemd/system/n5-fangov.service
	install -m 0644 deploy/n5-fangov-onfailure.service $(PKGDIR)/lib/systemd/system/n5-fangov-onfailure.service
	install -m 0644 deploy/config.example.toml         $(PKGDIR)/usr/share/doc/n5-fangov/config.example.toml
	install -m 0644 deploy/apt-90n5-fangov.conf        $(PKGDIR)/usr/share/n5-fangov/apt-90n5-fangov.conf
	install -m 0644 deploy/pve-notification/*.hbs      $(PKGDIR)/usr/share/n5-fangov/pve-notification/
	install -m 0644 deploy/README-DEPLOY.md DESIGN.md  $(PKGDIR)/usr/share/doc/n5-fangov/
	install -m 0644 LICENSE                            $(PKGDIR)/usr/share/doc/n5-fangov/copyright
	sed -e 's/@VERSION@/$(DEBVER)/' -e 's/^Architecture: .*/Architecture: $(ARCH)/' \
	    deploy/debian/control.in > $(PKGDIR)/DEBIAN/control
	install -m 0755 deploy/debian/postinst deploy/debian/prerm deploy/debian/postrm $(PKGDIR)/DEBIAN/
	dpkg-deb --build --root-owner-group $(PKGDIR) $(DEB)
	@echo built $(DEB)

clean:
	rm -rf $(DIST)
