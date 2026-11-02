// about.go is the About card (GET /api/about): name, version, licence,
// repository and credits.
package main

import (
	"runtime"

	"github.com/SirRenix/n5-fangov/internal/version"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// aboutInfo is the About card the dashboard shows.
func aboutInfo() web.About {
	return web.About{
		Name:       "n5-fangov",
		Version:    version.Version,
		Prerelease: version.Prerelease(),
		License:    "GPL-2.0-only",
		LicenseURL: "https://www.gnu.org/licenses/old-licenses/gpl-2.0.html",
		Repo:       "https://github.com/SirRenix/n5-fangov",
		Author:     "SirRenix",
		AuthorURL:  "https://github.com/SirRenix",
		Go:         runtime.Version(),
		Credits: []web.Credit{
			{Name: "SirRenix/minisforum-n5pro-fan-proxmox", URL: "https://github.com/SirRenix/minisforum-n5pro-fan-proxmox", Note: "DKMS package of the IT5571 kernel driver with downstream patches (write gate, hand-back, shutdown, ABI) and the test kit; the loaded version is in the header"},
			{Name: "ltdstudio/minisforum-n5-it5571", URL: "https://github.com/ltdstudio/minisforum-n5-it5571", Note: "upstream kernel driver the package is based on"},
			{Name: "Sl0thC0der/proxfansx", URL: "https://github.com/Sl0thC0der/proxfansx", Note: "dashboard idea; the nct67xx/it87xx profiles follow its chip handling (from documentation, untested here)"},
		},
	}
}
