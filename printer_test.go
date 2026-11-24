package main

import (
	"encoding/json"
	"testing"
)

var testRules = Rules{Margin: 5, FilamentWords: []string{"filament"}}

// feed applies nodes the way a telemetry window does, then settles the latch.
func feed(p *Printer, nodes map[string]any) {
	for path, value := range nodes {
		raw, _ := json.Marshal(value)
		p.Apply(path, raw)
	}

	p.Settle(testRules.Margin)
}

func connected(state string) *Printer {
	p := &Printer{Connected: true}
	feed(p, map[string]any{pathState: state})

	return p
}

func expectMood(t *testing.T, p *Printer, want Mood) {
	t.Helper()

	if got := p.Mood(testRules); got != want {
		t.Errorf("mood is %q, want %q (state %q, nozzle %.1f/%.1f, bed %.1f/%.1f, dialog %+v)",
			got, want, p.State, p.Nozzle, p.NozzleTarget, p.Bed, p.BedTarget, p.Dialog)
	}
}

func TestAPrinterThisServerHasNoLinkToIsOffline(t *testing.T) {
	p := &Printer{State: "PRINTING"}
	expectMood(t, p, MoodOffline)
}

func TestEachRestingStateHasItsOwnMood(t *testing.T) {
	for state, want := range map[string]Mood{
		"IDLE": MoodIdle, "READY": MoodIdle, "BUSY": MoodIdle, "UNKNOWN": MoodIdle,
		"PAUSED": MoodPaused, "FINISHED": MoodFinished, "STOPPED": MoodStopped,
		"ERROR": MoodError, "ATTENTION": MoodAttention, "PRINTING": MoodPrinting,
	} {
		t.Run(state, func(t *testing.T) { expectMood(t, connected(state), want) })
	}
}

// The start of a MINI print: heat to the probe temperature, probe, then heat
// to the print temperature. Both climbs are warming up; the probing between
// them is not.
func TestAPrintIsWarmingUntilItsTargetsAreReachedAndAgainWhenTheyRise(t *testing.T) {
	p := connected("PRINTING")

	feed(p, map[string]any{pathNozzleTarget: 170, pathBedTarget: 60, pathNozzle: 25, pathBed: 20})
	expectMood(t, p, MoodWarming)

	feed(p, map[string]any{pathNozzle: 168, pathBed: 58})
	expectMood(t, p, MoodPrinting)

	feed(p, map[string]any{pathNozzleTarget: 215})
	expectMood(t, p, MoodWarming)

	feed(p, map[string]any{pathNozzle: 214})
	expectMood(t, p, MoodPrinting)
}

// A part fan or a cold draught can pull the nozzle under its target for a few
// seconds. That is not warming up, and the lights must not flicker for it.
func TestAHeaterSaggingUnderTheSameTargetMidPrintStaysPrinting(t *testing.T) {
	p := connected("PRINTING")
	feed(p, map[string]any{pathNozzleTarget: 215, pathBedTarget: 60, pathNozzle: 215, pathBed: 60})
	feed(p, map[string]any{pathNozzle: 190})

	expectMood(t, p, MoodPrinting)
}

// A preheat from the printer's own menu does not start a print, so the state
// word stays IDLE while the heaters climb.
func TestAPreheatFromThePrintersMenuShowsAsWarming(t *testing.T) {
	p := connected("IDLE")
	feed(p, map[string]any{pathNozzleTarget: 215, pathNozzle: 40})

	expectMood(t, p, MoodWarming)
}

func TestAPrinterWithNoTargetsIsNeverWarming(t *testing.T) {
	p := connected("PRINTING")
	feed(p, map[string]any{pathNozzleTarget: nil, pathBedTarget: 0, pathNozzle: 25})

	expectMood(t, p, MoodPrinting)
}

func TestAFilamentDialogShowsAsFilamentEvenMidPrint(t *testing.T) {
	p := connected("PRINTING")
	p.Dialog = &Dialog{Code: "x", Title: "Filament runout", Text: "Load new Filament"}

	expectMood(t, p, MoodFilament)
}

func TestAFilamentDialogIsKnownByACodeTheOperatorListed(t *testing.T) {
	p := connected("ATTENTION")
	p.Dialog = &Dialog{Code: "12345", Title: "Load", Text: "Insert the spool"}

	rules := testRules
	rules.FilamentCodes = []string{"12345"}

	if got := p.Mood(rules); got != MoodFilament {
		t.Errorf("mood is %q with the code listed, want %q", got, MoodFilament)
	}

	if got := p.Mood(testRules); got != MoodAttention {
		t.Errorf("mood is %q with no code listed and no filament word, want %q", got, MoodAttention)
	}
}

func TestADialogThatIsNotAboutFilamentShowsAsAttention(t *testing.T) {
	p := connected("PRINTING")
	p.Dialog = &Dialog{Title: "Crash detected", Text: "Resume?"}

	expectMood(t, p, MoodAttention)
}

// An error is the thing a person has to see first, whatever dialog is up.
func TestAnErrorWinsOverAFilamentDialog(t *testing.T) {
	p := connected("ERROR")
	p.Dialog = &Dialog{Title: "Filament sensor not responding"}

	expectMood(t, p, MoodError)
}

func TestANodeThisModuleDoesNotReadIsNotApplied(t *testing.T) {
	p := &Printer{}
	if p.Apply("/axis/x", json.RawMessage(`180.4`)) {
		t.Error("Apply reported reading /axis/x, which decides no mood")
	}

	if !p.Apply(pathNozzle, json.RawMessage(`214.8`)) || p.Nozzle != 214.8 {
		t.Errorf("Apply did not read the nozzle temperature: nozzle is %v", p.Nozzle)
	}
}
