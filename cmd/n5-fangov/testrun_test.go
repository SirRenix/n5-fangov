package main

import "testing"

// The verification run must not hand pwm3 back to the EC on the N5 Pro:
// the EC does not regulate that channel after a write (measured
// 2026-09-14). The stop value therefore comes from the sanitized channel
// list, not from the raw config.
func TestTestStopPolicy(t *testing.T) {
	hdd := chanSpec{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{36, 105}, {46, 255}}, Critical: 56, Stop: "auto"}
	cpu := chanSpec{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 88, Stop: "auto"}
	fixed := chanSpec{Name: "case", PWM: 2, Sensor: "coretemp", Curve: [][2]int{{40, 90}, {70, 255}}, Critical: 80, Stop: "120"}

	cases := []struct {
		name    string
		profile string
		chans   []chanSpec
		pwm     int
		want    string
	}{
		{"n5pro pwm3 not in config", "n5pro", nil, 3, "140"},
		{"n5pro pwm3 configured auto", "n5pro", []chanSpec{hdd}, 3, "140"},
		{"n5pro pwm3 configured fixed", "n5pro", []chanSpec{{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: hdd.Curve, Critical: 56, Stop: "160"}}, 3, "160"},
		{"n5pro pwm1 not in config", "n5pro", []chanSpec{hdd}, 1, "auto"},
		{"n5pro pwm1 configured auto", "n5pro", []chanSpec{cpu, hdd}, 1, "auto"},
		{"n5pro pwm4 unowned", "n5pro", []chanSpec{cpu, hdd}, 4, "auto"},
		{"nct67xx configured fixed", "nct67xx", []chanSpec{fixed}, 2, "120"},
		{"nct67xx pwm3 not in config stays auto", "nct67xx", []chanSpec{fixed}, 3, "auto"},
		{"no config, generic profile", "it87xx", nil, 1, "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testStopPolicy(tc.profile, tc.chans, tc.pwm); got != tc.want {
				t.Errorf("testStopPolicy(%s, pwm%d) = %q, want %q", tc.profile, tc.pwm, got, tc.want)
			}
		})
	}
}

// testStopPolicy must not modify the caller's list.
func TestTestStopPolicyDoesNotMutate(t *testing.T) {
	chans := []chanSpec{{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{36, 105}, {46, 255}}, Critical: 56, Stop: "auto"}}
	_ = testStopPolicy("n5pro", chans, 3)
	if chans[0].Stop != "auto" {
		t.Errorf("input mutated: stop = %q", chans[0].Stop)
	}
}
