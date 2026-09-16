package main

import (
	"strings"
	"testing"
)

func TestAboutInfo(t *testing.T) {
	a := aboutInfo()
	if a.Name != "n5-fangov" || a.Version == "" || a.License != "GPL-2.0-only" || a.Go == "" || len(a.Credits) != 2 {
		t.Errorf("about: %+v", a)
	}
	if !strings.HasPrefix(a.Repo, "https://github.com/SirRenix/") || a.Author != "SirRenix" || !strings.Contains(a.LicenseURL, "gpl-2.0") {
		t.Errorf("links: %+v", a)
	}
}
