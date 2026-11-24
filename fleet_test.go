package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

var testSettings = Settings{
	TasmotaURL: "http://192.168.178.54", WarmMargin: 5, FilamentWords: []string{"filament"},
	Scenes: map[string]string{"printing": "Power2 ON"},
}

func telemetry(device, path string, value any) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"path": path, "value": value})
	out, _ := json.Marshal(map[string]any{
		"type": "com.prusa.connect.device.telemetry.sampled", "subject": "device:" + device, "data": json.RawMessage(data),
	})

	return out
}

func envelope(kind, device string, data any) json.RawMessage {
	encoded, _ := json.Marshal(data)
	out, _ := json.Marshal(map[string]any{"type": kind, "subject": "device:" + device, "data": json.RawMessage(encoded)})

	return out
}

// mini is the MINI's record as Prusa Connect answered it, trimmed to what a mood reads.
func mini(id, state string) deviceapi.Device {
	return deviceapi.Device{
		Kind: "printer", ID: id, Name: "MINI " + id, Connected: true,
		State: map[string]json.RawMessage{
			"/state": json.RawMessage(`"` + state + `"`), "/axis/x": json.RawMessage(`180.4`),
			"/temperature/bed": json.RawMessage(`12.1`), "/temperature/bed_target": json.RawMessage(`0`),
			"/tools/0/nozzle_temp": json.RawMessage(`11.4`), "/tools/0/nozzle_target_temp": json.RawMessage(`null`),
		},
	}
}

func TestASeededPrinterShowsTheMoodItsRecordSays(t *testing.T) {
	fleet := NewFleet(testSettings)
	fleet.Seed(mini("a", "IDLE"))

	if mood, id := fleet.Mood(); mood != MoodIdle || id != "a" {
		t.Errorf("mood is %q for %q, want idle for a", mood, id)
	}
}

func TestTelemetryMovesTheMoodOfThePrinterItIsAbout(t *testing.T) {
	fleet := NewFleet(testSettings)
	fleet.Seed(mini("a", "IDLE"))

	fleet.Window([]json.RawMessage{
		telemetry("a", "/state", "PRINTING"),
		telemetry("a", "/tools/0/nozzle_target_temp", 215),
	}, nil)

	if mood, _ := fleet.Mood(); mood != MoodWarming {
		t.Fatalf("mood is %q after the print started cold, want warming", mood)
	}

	fleet.Window([]json.RawMessage{telemetry("a", "/tools/0/nozzle_temp", 213)}, nil)

	if mood, _ := fleet.Mood(); mood != MoodPrinting {
		t.Errorf("mood is %q once the nozzle is at temperature, want printing", mood)
	}
}

func TestADialogArrivesAndLeavesWithTheIssuesFrame(t *testing.T) {
	fleet := NewFleet(testSettings)
	fleet.Seed(mini("a", "PRINTING"))

	fleet.Window(nil, []json.RawMessage{envelope("com.prusa.connect.device.issues.changed", "a",
		map[string]any{"dialog": map[string]any{"code": "x", "title": "Load filament"}})})

	if mood, _ := fleet.Mood(); mood != MoodFilament {
		t.Fatalf("mood is %q with a filament dialog up, want filament", mood)
	}

	fleet.Window(nil, []json.RawMessage{envelope("com.prusa.connect.device.issues.changed", "a", map[string]any{})})

	if mood, _ := fleet.Mood(); mood != MoodPrinting {
		t.Errorf("mood is %q after the dialog was answered, want printing", mood)
	}
}

func TestADepartedPrinterIsOfflineAndAnArrivedOneIsAskedForAgain(t *testing.T) {
	fleet := NewFleet(testSettings)
	fleet.Seed(mini("a", "PRINTING"))

	fleet.Window(nil, []json.RawMessage{envelope("com.prusa.connect.device.departed", "a", map[string]any{})})

	if mood, _ := fleet.Mood(); mood != MoodOffline {
		t.Errorf("mood is %q after the printer left, want offline", mood)
	}

	arrived := fleet.Window(nil, []json.RawMessage{envelope("com.prusa.connect.device.arrived", "a", map[string]any{})})
	if len(arrived) != 1 || arrived[0] != "a" {
		t.Errorf("arrived is %q, want [a] so the caller reads its record again", arrived)
	}
}

// With two printers and no printer setting, one light shows whichever printer
// most needs somebody.
func TestTheLightsFollowThePrinterThatMostNeedsAPerson(t *testing.T) {
	fleet := NewFleet(testSettings)
	fleet.Seed(mini("a", "PRINTING"), mini("b", "ERROR"))

	if mood, id := fleet.Mood(); mood != MoodError || id != "b" {
		t.Errorf("mood is %q for %q, want error for b", mood, id)
	}
}

func TestThePrinterSettingNarrowsTheLightsToOnePrinterByIDOrName(t *testing.T) {
	for _, name := range []string{"a", "MINI a"} {
		settings := testSettings
		settings.Printer = name

		fleet := NewFleet(settings)
		fleet.Seed(mini("a", "PRINTING"), mini("b", "ERROR"))

		if mood, id := fleet.Mood(); mood != MoodPrinting || id != "a" {
			t.Errorf("with printer %q the mood is %q for %q, want printing for a", name, mood, id)
		}
	}
}

func TestSettingsWithABadAddressOrAnUnknownSceneAreRefused(t *testing.T) {
	cases := map[string]func(*Settings){
		"no scheme":     func(s *Settings) { s.TasmotaURL = "192.168.178.54" },
		"unknown scene": func(s *Settings) { s.Scenes = map[string]string{"warmup": "Power1 ON"} },
		"negative":      func(s *Settings) { s.WarmMargin = -1 },
	}

	for name, spoil := range cases {
		settings := testSettings
		spoil(&settings)

		if err := settings.check(); !errors.Is(err, errSettings) {
			t.Errorf("%s: check returned %v, want errSettings", name, err)
		}
	}

	if err := testSettings.check(); err != nil {
		t.Errorf("good settings refused: %v", err)
	}
}
