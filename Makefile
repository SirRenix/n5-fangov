# n5-fangov build. Plain make + dpkg-deb, no debhelper. Needs Go 1.26 (or run
# inside the golang:1.26-alpine container via tools/remote-go.ps1).
#
#   make build   -> dist/n5-fangov (linux/amd64, static, stripped)
#   make deb     -> dist/n5-fangov_<version>_amd64.deb (no conffile: the config is
#                   written by `n5-fangov setup`; the example goes to /usr/share/doc)
#   make check   -> go vet + go test
#   make verify-deploy -> systemd-analyze verify on the units and apt-config
#                   on the apt hook (each skipped with a note when the tool
#                   is absent, e.g. in the build container); always diffs the
#                   PVE template copies (deploy/ vs. the embedded ones)
#   make clean

MODULE   := github.com/SirRenix/n5-fangov
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
# Debian version: strip a leading v; "-" is not allowed in upstream versions.
# A pre-release tag (-alpha/-beta/-rc) becomes "~" so it sorts BEFORE the
# release (0.3.0~beta.1 < 0.3.0); every other "-" (git describe's -N-gHASH,
# -dirty) becomes "+" so it sorts after the tag it is based on.
DEBVER   := $(shell echo '$(VERSION)' | sed -e 's/^v//' -e 's/-\(alpha\|beta\|rc\)/~\1/' -e 's/-/+/g')
ARCH     ?= amd64
LDFLAGS  := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)
DIST     := dist
PKGDIR   := $(DIST)/pkg
DEB      := $(DIST)/n5-fangov_$(DEBVER)_$(ARCH).deb

export CGO_ENABLED = 0
export GOOS        = linux
export GOARCH      = $(ARCH)

.PHONY: build check test-race verify-deploy deb deb-only release hooks secrets clean version

build:
	mkdir -p $(DIST)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/n5-fangov ./cmd/n5-fangov

check:
	go vet ./...
	go test ./...

# Static checks of the deploy files. Needs a systemd host for the unit
# check (the Docker build container has none). The units are copied to a
# temp dir; on a host without the package installed the Exec* paths are
# pointed at /bin/true so verify judges the directives, not the install.
# apt-config parses the hook the way apt does (exit 100 on a syntax error).
# The PVE template pair exists twice — deploy/pve-notification for install.sh
# and postinst (shell cannot read the binary's embed) and
# internal/alert/templates for the daemon/CLI — and must stay identical.
verify-deploy:
	@for f in n5-fangov-subject.txt.hbs n5-fangov-body.txt.hbs; do \
	    cmp -s deploy/pve-notification/$$f internal/alert/templates/$$f \
	        || { echo "PVE template $$f: deploy/pve-notification and internal/alert/templates differ"; exit 1; }; \
	done; echo "PVE templates: deploy copy matches the embedded copy"
	@if command -v systemd-analyze >/dev/null 2>&1; then \
	    tmp=$$(mktemp -d); cp deploy/n5-fangov.service deploy/n5-fangov-onfailure.service $$tmp/; \
	    if [ ! -x /usr/bin/n5-fangov ]; then \
	        sed -i -e 's#/usr/bin/n5-fangov#/bin/true#g' -e 's#/usr/libexec/n5-fangov/n5-fangov-onfailure#/bin/true#g' $$tmp/*.service; \
	        echo "n5-fangov not installed here: Exec* paths replaced by /bin/true for the check"; \
	    fi; \
	    systemd-analyze verify --man=no $$tmp/n5-fangov.service $$tmp/n5-fangov-onfailure.service; rc=$$?; \
	    rm -rf $$tmp; [ $$rc -eq 0 ] && echo "systemd-analyze verify: ok"; exit $$rc; \
	else echo "systemd-analyze not found: unit check skipped"; fi
	@if command -v apt-config >/dev/null 2>&1; then \
	    apt-config -c deploy/apt-90n5-fangov.conf dump DPkg::Post-Invoke >/dev/null && echo "apt-config: hook parses"; \
	else echo "apt-config not found: apt hook check skipped"; fi

version:
	@echo $(VERSION) '(deb: $(DEBVER))'

# deb builds first; deb-only packages the binary that is already in dist/ (the release
# workflow uses it so the .deb carries the very same file that is uploaded as the asset).
deb: build
	$(MAKE) deb-only VERSION='$(VERSION)'

deb-only:
	@test -x $(DIST)/n5-fangov || { echo "deb-only: $(DIST)/n5-fangov missing (make build first)"; exit 1; }
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
	install -d -m 0755 $(PKGDIR)/usr/share/doc/n5-fangov/examples
	install -m 0644 deploy/emergency.example.sh        $(PKGDIR)/usr/share/doc/n5-fangov/examples/emergency.example.sh
	install -m 0644 deploy/apt-90n5-fangov.conf        $(PKGDIR)/usr/share/n5-fangov/apt-90n5-fangov.conf
	install -m 0644 deploy/pve-notification/*.hbs      $(PKGDIR)/usr/share/n5-fangov/pve-notification/
	install -d -m 0755 $(PKGDIR)/usr/share/doc/n5-fangov/docs
	install -m 0644 README.md CHANGELOG.md DESIGN.md      $(PKGDIR)/usr/share/doc/n5-fangov/
	install -m 0644 docs/*.md                            $(PKGDIR)/usr/share/doc/n5-fangov/docs/
	install -m 0644 deploy/debian/copyright            $(PKGDIR)/usr/share/doc/n5-fangov/copyright
	sed -e 's/@VERSION@/$(DEBVER)/' -e 's/^Architecture: .*/Architecture: $(ARCH)/' \
	    deploy/debian/control.in > $(PKGDIR)/DEBIAN/control
	install -m 0755 deploy/debian/postinst deploy/debian/prerm deploy/debian/postrm $(PKGDIR)/DEBIAN/
	cd $(PKGDIR) && find . -type f -not -path "./DEBIAN/*" | sed 's|^\./||' | LC_ALL=C sort | xargs md5sum > DEBIAN/md5sums && test -s DEBIAN/md5sums && chmod 0644 DEBIAN/md5sums
	dpkg-deb --build --root-owner-group $(PKGDIR) $(DEB)
	@echo built $(DEB)

clean:
	rm -rf $(DIST)

# release: tag first (git tag -a vX.Y.Z), then `make release NOTES="..."`. Builds from
# the tag, uploads the static binary + sha256 to the GitHub release; a hyphen in the
# version (-beta.1, -rc1) marks it as a pre-release. Needs the gh CLI signed in.
NOTES ?= see DESIGN.md and README.md
release: build
	@case '$(VERSION)' in *-g*|*dirty*) echo "not on a clean tag: $(VERSION)"; exit 1;; esac
	cp $(DIST)/n5-fangov $(DIST)/n5-fangov-$(VERSION)-linux-amd64
	cd $(DIST) && sha256sum n5-fangov-$(VERSION)-linux-amd64 > n5-fangov-$(VERSION)-linux-amd64.sha256
	gh release create $(VERSION) $(if $(findstring -,$(VERSION)),--prerelease,) --title "$(VERSION)" --notes "$(NOTES)" \
	    $(DIST)/n5-fangov-$(VERSION)-linux-amd64 $(DIST)/n5-fangov-$(VERSION)-linux-amd64.sha256

# test-race: the race detector needs cgo, so this target is for a host with a
# full Go toolchain (CI, a Debian box); the static release build stays cgo-free.
# Over the Docker builder: tools/remote-go.ps1 -Image golang:1.26-bookworm -Cmd "make test-race".
test-race:
	CGO_ENABLED=1 go test -race -count=1 ./...

# hooks: enable the repository's git hooks (pre-commit runs gitleaks over the
# staged changes; see .githooks/pre-commit and .gitleaks.toml).
hooks:
	git config core.hooksPath .githooks
	@echo "pre-commit hook enabled (needs gitleaks on PATH)"

# secrets: scan the working tree and the whole history with the repository rules.
secrets:
	gitleaks dir . --config .gitleaks.toml --no-banner
	gitleaks git . --config .gitleaks.toml --no-banner
