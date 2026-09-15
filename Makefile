# pvefand build. Plain make + dpkg-deb, no debhelper. Needs Go 1.25 (or run
# inside the golang:1.25-alpine container via tools/remote-go.ps1).
#
#   make build   -> dist/pvefand (linux/amd64, static, stripped)
#   make deb     -> dist/pvefand_<version>_amd64.deb
#   make check   -> go vet + go test
#   make clean

MODULE   := github.com/SirRenix/pvefand
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
# Debian version: strip a leading v, "-" is not allowed in upstream versions
DEBVER   := $(shell echo '$(VERSION)' | sed -e 's/^v//' -e 's/-/+/g')
ARCH     ?= amd64
LDFLAGS  := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)
DIST     := dist
PKGDIR   := $(DIST)/pkg
DEB      := $(DIST)/pvefand_$(DEBVER)_$(ARCH).deb

export CGO_ENABLED = 0
export GOOS        = linux
export GOARCH      = $(ARCH)

.PHONY: build check deb clean version

build:
	mkdir -p $(DIST)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/pvefand ./cmd/pvefand

check:
	go vet ./...
	go test ./...

version:
	@echo $(VERSION) '(deb: $(DEBVER))'

deb: build
	rm -rf $(PKGDIR)
	install -d -m 0755 $(PKGDIR)/DEBIAN \
	    $(PKGDIR)/usr/bin \
	    $(PKGDIR)/usr/libexec/pvefand \
	    $(PKGDIR)/lib/systemd/system \
	    $(PKGDIR)/etc/pvefand/presets \
	    $(PKGDIR)/usr/share/pvefand/pve-notification \
	    $(PKGDIR)/usr/share/doc/pvefand
	install -m 0755 $(DIST)/pvefand                   $(PKGDIR)/usr/bin/pvefand
	install -m 0755 deploy/pvefand-onfailure          $(PKGDIR)/usr/libexec/pvefand/pvefand-onfailure
	install -m 0644 deploy/pvefand.service            $(PKGDIR)/lib/systemd/system/pvefand.service
	install -m 0644 deploy/pvefand-onfailure.service  $(PKGDIR)/lib/systemd/system/pvefand-onfailure.service
	install -m 0644 deploy/config.example.toml        $(PKGDIR)/etc/pvefand/config.toml
	install -m 0644 deploy/pve-notification/*.hbs     $(PKGDIR)/usr/share/pvefand/pve-notification/
	install -m 0644 deploy/README-DEPLOY.md DESIGN.md $(PKGDIR)/usr/share/doc/pvefand/
	install -m 0644 LICENSE                           $(PKGDIR)/usr/share/doc/pvefand/copyright
	sed -e 's/@VERSION@/$(DEBVER)/' -e 's/^Architecture: .*/Architecture: $(ARCH)/' \
	    deploy/debian/control.in > $(PKGDIR)/DEBIAN/control
	install -m 0644 deploy/debian/conffiles $(PKGDIR)/DEBIAN/conffiles
	install -m 0755 deploy/debian/postinst deploy/debian/prerm deploy/debian/postrm $(PKGDIR)/DEBIAN/
	dpkg-deb --build --root-owner-group $(PKGDIR) $(DEB)
	@echo built $(DEB)

clean:
	rm -rf $(DIST)
