package main

import (
	"encoding/json"
	"strings"
)

// Mood is the light scene for what a printer is doing. Its value is the key an
// operator writes a Tasmota command under in the scenes setting.
type Mood string

const (
	MoodOffline   Mood = "offline"
	MoodIdle      Mood = "idle"
	MoodWarming   Mood = "warming"
	MoodFilament  Mood = "filament"
	MoodAttention Mood = "attention"
	MoodPrinting  Mood = "printing"
	MoodPaused    Mood = "paused"
	MoodFinished  Mood = "finished"
	MoodStopped   Mood = "stopped"
	MoodError     Mood = "error"
)

// moods is every mood, so the scenes setting can be checked for a key nothing reads.
var moods = []Mood{
	MoodOffline, MoodIdle, MoodWarming, MoodFilament, MoodAttention,
	MoodPrinting, MoodPaused, MoodFinished, MoodStopped, MoodError,
}

// The state tree paths a Buddy printer reports, as Prusa Connect stores them.
const (
	pathState        = "/state"
	pathBed          = "/temperature/bed"
	pathBedTarget    = "/temperature/bed_target"
	pathNozzle       = "/tools/0/nozzle_temp"
	pathNozzleTarget = "/tools/0/nozzle_target_temp"
)

// Dialog is the question a printer has stopped on, in the machine's own words.
type Dialog struct {
	Code  string `json:"code,omitempty"`
	Title string `json:"title,omitempty"`
	Text  string `json:"text,omitempty"`
}

// Rules are the settings that decide a mood beyond the printer's state word.
type Rules struct {
	// Margin is how many degrees under its target a heater still counts as there.
	Margin float64
	// FilamentCodes are dialog codes that mean the printer wants filament.
	FilamentCodes []string
	// FilamentWords mark a filament dialog by its title or text, for codes nobody listed.
	FilamentWords []string
}

// Printer is what this module holds of one printer.
type Printer struct {
	Connected bool
	State     string

	Nozzle, NozzleTarget float64
	Bed, BedTarget       float64

	Dialog *Dialog

	// heatedFor is the pair of targets the printer last reached. Warming up is
	// "these targets were never reached", not "below target right now", so a
	// heater sagging mid-print does not flip the lights back to warming.
	heatedFor [2]float64
	heated    bool
}

// Apply records one state tree node and reports whether this module reads it.
func (p *Printer) Apply(path string, value json.RawMessage) bool {
	switch path {
	case pathState:
		var state string
		if json.Unmarshal(value, &state) != nil {
			return false
		}

		p.State = state
	case pathBed:
		p.Bed = number(value)
	case pathBedTarget:
		p.BedTarget = number(value)
	case pathNozzle:
		p.Nozzle = number(value)
	case pathNozzleTarget:
		p.NozzleTarget = number(value)
	default:
		return false
	}

	return true
}

// number reads a temperature. A printer reports null for a target it has none
// of, which is the same as zero: nothing to heat to.
func number(value json.RawMessage) float64 {
	var n *float64
	if json.Unmarshal(value, &n) != nil || n == nil {
		return 0
	}

	return *n
}

// Settle moves the warming latch on. Call it after every batch of Apply.
func (p *Printer) Settle(margin float64) {
	targets := [2]float64{p.NozzleTarget, p.BedTarget}
	if targets != p.heatedFor {
		p.heatedFor, p.heated = targets, false
	}

	if !p.heated && near(p.Nozzle, p.NozzleTarget, margin) && near(p.Bed, p.BedTarget, margin) {
		p.heated = true
	}
}

// near reports whether a heater is at its target, and a heater with no target
// always is.
func near(temp, target, margin float64) bool {
	return target <= 0 || temp >= target-margin
}

// warming reports a heater still on its way to a target it was given.
func (p *Printer) warming() bool {
	return (p.NozzleTarget > 0 || p.BedTarget > 0) && !p.heated
}

// Mood decides the scene. A state that needs a person wins over one that
// does not, so a filament prompt in the middle of a print shows as one.
func (p *Printer) Mood(rules Rules) Mood {
	switch {
	case !p.Connected:
		return MoodOffline
	case p.State == "ERROR":
		return MoodError
	case p.Dialog != nil && rules.isFilament(*p.Dialog):
		return MoodFilament
	case p.State == "ATTENTION" || p.Dialog != nil:
		return MoodAttention
	case p.State == "PAUSED":
		return MoodPaused
	case p.State == "FINISHED":
		return MoodFinished
	case p.State == "STOPPED":
		return MoodStopped
	case p.warming():
		return MoodWarming
	case p.State == "PRINTING":
		return MoodPrinting
	default:
		return MoodIdle
	}
}

func (r Rules) isFilament(d Dialog) bool {
	for _, code := range r.FilamentCodes {
		if code != "" && d.Code == code {
			return true
		}
	}

	text := strings.ToLower(d.Title + " " + d.Text)
	for _, word := range r.FilamentWords {
		if word != "" && strings.Contains(text, strings.ToLower(word)) {
			return true
		}
	}

	return false
}
