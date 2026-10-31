package main

import "flag"

// parseInterspersed parses flags before and after positional arguments
// ("test pwm3 --force" as well as "test --force pwm3") and returns the
// positional ones in order. The flag package alone stops at the first
// non-flag argument and would hand "--force" back as a second positional.
// A "--" ends flag parsing: everything after it is positional, also when it
// starts with "-". "-" alone is a positional (stdin/stdout).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" && !flagTakesValue(fs, args, n-1) {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// flagTakesValue reports whether args[i] is the separate value of the flag
// in args[i-1] ("--out --"), not a terminator.
func flagTakesValue(fs *flag.FlagSet, args []string, i int) bool {
	if i == 0 {
		return false
	}
	prev := args[i-1]
	if len(prev) < 2 || prev[0] != '-' || prev == "--" {
		return false
	}
	name := prev[1:]
	if name[0] == '-' {
		name = name[1:]
	}
	for j := 0; j < len(name); j++ {
		if name[j] == '=' {
			return false // value given inline
		}
	}
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return false
	}
	return true
}
