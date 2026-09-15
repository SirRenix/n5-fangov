// Command pvefand is a guarded fan controller for Proxmox VE and Debian.
package main

import (
	"fmt"
	"os"

	"github.com/SirRenix/pvefand/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("pvefand", version.Version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: pvefand <serve|status|set|auto|curve|log|check|detect|test|failsafe|version>")
}