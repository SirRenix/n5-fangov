package main

import (
	"strings"
	"testing"
)

func TestAboutInfo(t *testing.T) {
	a := aboutInfo()
	if a.Name != "n5-fangov" || a.Version == "" || a.License != "GPL-2.0-only" || a.Go == "" || len(a.Credits) != 3 {
		t.Errorf("about: %+v", a)
	}
	// the driver the box runs is the downstream package; upstream is its base
	if len(a.Credits) == 3 && (a.Credits[0].Name != "SirRenix/minisforum-n5pro-fan-proxmox" || a.Credits[1].Name != "ltdstudio/minisforum-n5-it5571") {
		t.Errorf("credits order: %+v", a.Credits)
	}
	if !strings.HasPrefix(a.Repo, "https://github.com/SirRenix/") || a.Author != "SirRenix" || !strings.Contains(a.LicenseURL, "gpl-2.0") {
		t.Errorf("links: %+v", a)
	}
}
