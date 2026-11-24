package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sync"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
)

// Settings is the instance's config block, with module.yaml's defaults under it.
type Settings struct {
	TasmotaURL    string            `json:"tasmota_url"`
	Printer       string            `json:"printer"`
	WarmMargin    float64           `json:"warm_margin"`
	FilamentCodes []string          `json:"filament_codes"`
	FilamentWords []string          `json:"filament_words"`
	Scenes        map[string]string `json:"scenes"`
}

var errSettings = errors.New("settings")

// check refuses settings the module could only fail on later, so the operator
// reads the reason on the instance at start rather than in a log at the first print.
func (s Settings) check() error {
	address, err := url.Parse(s.TasmotaURL)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
		return fmt.Errorf("%w: tasmota_url %q is not an http address such as http://192.168.1.50",
			errSettings, s.TasmotaURL)
	}

	if s.WarmMargin < 0 {
		return fmt.Errorf("%w: warm_margin is %v and cannot be under zero", errSettings, s.WarmMargin)
	}

	for key := range s.Scenes {
		if !slices.Contains(moods, Mood(key)) {
			return fmt.Errorf("%w: scenes has %q, which is not a mood. The moods are %v", errSettings, key, moods)
		}
	}

	return nil
}

func (s Settings) rules() Rules {
	return Rules{Margin: s.WarmMargin, FilamentCodes: s.FilamentCodes, FilamentWords: s.FilamentWords}
}

// urgency orders moods for a fleet of more than one printer: the lights show the
// printer that most needs a person.
var urgency = []Mood{
	MoodError, MoodFilament, MoodAttention, MoodWarming, MoodPrinting,
	MoodPaused, MoodFinished, MoodStopped, MoodIdle, MoodOffline,
}

// Fleet is every printer this module follows.
type Fleet struct {
	mu       sync.Mutex
	settings Settings
	printers map[string]*Printer
	names    map[string]string
}

func NewFleet(settings Settings) *Fleet {
	return &Fleet{settings: settings, printers: map[string]*Printer{}, names: map[string]string{}}
}

// Seed replaces what the fleet holds of each device with its record.
func (f *Fleet) Seed(devices ...deviceapi.Device) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, device := range devices {
		if device.Kind != "printer" || device.Retired {
			continue
		}

		p := &Printer{Connected: device.Connected}
		for path, value := range device.State {
			p.Apply(path, value)
		}

		p.Dialog = dialogOf(device.Dialog)
		p.Settle(f.settings.WarmMargin)

		f.printers[device.ID] = p
		f.names[device.ID] = device.Name
	}
}

func dialogOf(d *deviceapi.Dialog) *Dialog {
	if d == nil {
		return nil
	}

	return &Dialog{Code: d.Code, Title: d.Title, Text: d.Text}
}

// Window applies one subscription window and returns the devices that arrived
// in it, which the caller reads again whole.
func (f *Fleet) Window(telemetry, events []json.RawMessage) (arrived []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	touched := map[string]bool{}

	for _, raw := range telemetry {
		var envelope event.Envelope
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}

		var node struct {
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		}

		id := deviceOf(envelope)
		if p := f.printers[id]; p != nil && json.Unmarshal(envelope.Data, &node) == nil && p.Apply(node.Path, node.Value) {
			touched[id] = true
		}
	}

	for _, raw := range events {
		var envelope event.Envelope
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}

		id := deviceOf(envelope)

		switch envelope.Type {
		case event.TypeDeviceArrived:
			arrived = append(arrived, id)
		case event.TypeDeviceDeparted:
			if p := f.printers[id]; p != nil {
				p.Connected = false
			}
		case event.TypeDeviceIssuesChanged:
			var change struct {
				Dialog *deviceapi.Dialog `json:"dialog"`
			}

			if p := f.printers[id]; p != nil && json.Unmarshal(envelope.Data, &change) == nil {
				p.Dialog = dialogOf(change.Dialog)
			}
		}
	}

	for id := range touched {
		f.printers[id].Settle(f.settings.WarmMargin)
	}

	return arrived
}

// deviceOf reads the device an envelope is about. Every device event carries
// device:<id> as its subject.
func deviceOf(envelope event.Envelope) string {
	origin, err := event.ParseOrigin(envelope.Subject)
	if err != nil || origin.Kind() != event.KindDevice {
		return ""
	}

	return origin.ID()
}

// Mood is the one scene the lights show, and the printer it is for.
func (f *Fleet) Mood() (Mood, string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	best, bestID := MoodOffline, ""
	rules := f.settings.rules()

	for id, p := range f.printers {
		if f.settings.Printer != "" && f.settings.Printer != id && f.settings.Printer != f.names[id] {
			continue
		}

		if mood := p.Mood(rules); slices.Index(urgency, mood) < slices.Index(urgency, best) || bestID == "" {
			best, bestID = mood, id
		}
	}

	return best, bestID
}

// PrinterStatus is one printer as the status route reports it.
type PrinterStatus struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Connected bool    `json:"connected"`
	State     string  `json:"state"`
	Mood      Mood    `json:"mood"`
	Nozzle    float64 `json:"nozzle"`
	NozzleTo  float64 `json:"nozzle_target"`
	Bed       float64 `json:"bed"`
	BedTo     float64 `json:"bed_target"`
	Dialog    *Dialog `json:"dialog,omitempty"`
}

func (f *Fleet) Status() []PrinterStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]PrinterStatus, 0, len(f.printers))
	for id, p := range f.printers {
		out = append(out, PrinterStatus{
			ID: id, Name: f.names[id], Connected: p.Connected, State: p.State, Mood: p.Mood(f.settings.rules()),
			Nozzle: p.Nozzle, NozzleTo: p.NozzleTarget, Bed: p.Bed, BedTo: p.BedTarget, Dialog: p.Dialog,
		})
	}

	slices.SortFunc(out, func(a, b PrinterStatus) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}

		return 0
	})

	return out
}
