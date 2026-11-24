package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeTasmota answers like the device does, and records the commands it was sent.
type fakeTasmota struct {
	mu   sync.Mutex
	sent []string
	// answer is the body for a command; the default is {"POWER1":"ON"}.
	answer func(command string) string
}

func (f *fakeTasmota) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	command := r.URL.Query().Get("cmnd")

	f.mu.Lock()
	f.sent = append(f.sent, command)
	f.mu.Unlock()

	body := `{"POWER1":"ON"}`
	if f.answer != nil {
		body = f.answer(command)
	}

	_, _ = w.Write([]byte(body))
}

func (f *fakeTasmota) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.sent)
}

func TestASceneIsSentOneCommandPerRequestInOrder(t *testing.T) {
	device := &fakeTasmota{}
	server := httptest.NewServer(device)
	defer server.Close()

	err := Tasmota{Base: server.URL + "/", Client: server.Client()}.
		Run(context.Background(), " Power1 ON ;Color1 FF6000;; Power2 OFF ")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"Power1 ON", "Color1 FF6000", "Power2 OFF"}
	if got := device.commands(); !slices.Equal(got, want) {
		t.Errorf("Tasmota was sent %q, want %q", got, want)
	}
}

func TestAnEmptySceneSendsNothing(t *testing.T) {
	device := &fakeTasmota{}
	server := httptest.NewServer(device)
	defer server.Close()

	if err := (Tasmota{Base: server.URL, Client: server.Client()}).Run(context.Background(), " ; "); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := device.commands(); len(got) != 0 {
		t.Errorf("an empty scene sent %q", got)
	}
}

// Tasmota answers 200 to a command it does not know. A misspelled scene has to
// surface as an error, and the commands after it must not run half a scene.
func TestACommandTasmotaDoesNotKnowIsAnErrorAndStopsTheScene(t *testing.T) {
	device := &fakeTasmota{answer: func(command string) string {
		if command == "Colr1 FF0000" {
			return `{"Command":"Unknown"}`
		}

		return `{"POWER1":"ON"}`
	}}
	server := httptest.NewServer(device)
	defer server.Close()

	err := Tasmota{Base: server.URL, Client: server.Client()}.
		Run(context.Background(), "Power1 ON; Colr1 FF0000; Power2 OFF")
	if !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "Colr1") {
		t.Fatalf("Run returned %v, want errRefused naming the misspelled command", err)
	}

	if got := device.commands(); !slices.Equal(got, []string{"Power1 ON", "Colr1 FF0000"}) {
		t.Errorf("Tasmota was sent %q after the refusal", got)
	}
}

func TestATasmotaWithAWebPasswordIsReportedAsRefusing(t *testing.T) {
	device := &fakeTasmota{answer: func(string) string {
		return `{"WARNING":"Need user=<username>&password=<password>"}`
	}}
	server := httptest.NewServer(device)
	defer server.Close()

	err := Tasmota{Base: server.URL, Client: server.Client()}.Run(context.Background(), "Power1 ON")
	if !errors.Is(err, errRefused) || !strings.Contains(err.Error(), "Need user") {
		t.Errorf("Run returned %v, want errRefused carrying Tasmota's warning", err)
	}
}
