package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func startLights(t *testing.T, device *fakeTasmota) *Lights {
	t.Helper()

	server := httptest.NewServer(device)
	t.Cleanup(server.Close)

	lights := NewLights(Tasmota{Base: server.URL, Client: server.Client()}, map[string]string{
		"warming": "Power1 ON; Color1 FF6000", "printing": "Power1 OFF; Power2 ON",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lights.Retry = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go lights.Run(ctx)

	return lights
}

// eventually waits for the lights to settle, since Run works on its own goroutine.
func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if done() {
			return
		}
	}

	t.Fatalf("timed out waiting for %s", what)
}

// Telemetry arrives several times a second. Each sample asks for the mood
// again, and Tasmota should hear about a mood once, not every time it is asked.
func TestTheSameMoodAskedForAgainIsSentOnce(t *testing.T) {
	device := &fakeTasmota{}
	lights := startLights(t, device)

	lights.Show(MoodWarming, "a")
	eventually(t, "the warming scene", func() bool { return len(device.commands()) == 2 })

	// Spaced out so Run reads each request. Asked back to back, only the newest
	// is read and the repeat is never seen, which would let this test pass for
	// the wrong reason.
	for range 3 {
		lights.Show(MoodWarming, "a")
		time.Sleep(30 * time.Millisecond)
	}

	if got := device.commands(); len(got) != 2 {
		t.Fatalf("asking for warming again sent it again: Tasmota was sent %q", got)
	}

	lights.Show(MoodPrinting, "a")
	eventually(t, "the printing scene", func() bool { return len(device.commands()) == 4 })

	want := []string{"Power1 ON", "Color1 FF6000", "Power1 OFF", "Power2 ON"}
	if got := device.commands(); !slices.Equal(got, want) {
		t.Errorf("Tasmota was sent %q, want %q", got, want)
	}
}

// A Tasmota that is rebooting or off WiFi misses a scene. The lights must catch
// up when it is back, without waiting for the printer to change again.
func TestAFailedSceneIsSentAgainUntilTasmotaTakesIt(t *testing.T) {
	var refusals atomic.Int32
	refusals.Store(2)

	device := &fakeTasmota{answer: func(string) string {
		if refusals.Add(-1) >= 0 {
			return `{"Command":"Error"}`
		}

		return `{"POWER2":"ON"}`
	}}
	lights := startLights(t, device)

	lights.Show(MoodPrinting, "a")
	eventually(t, "the scene to go through", func() bool {
		status := lights.Status()

		return status.Mood == MoodPrinting && status.Error == "" && !status.SentAt.IsZero()
	})

	if got := device.commands(); len(got) != 4 {
		t.Errorf("Tasmota was sent %q: want two refused first commands, then the whole scene", got)
	}
}

func TestAMoodWithNoSceneSendsNothingAndIsNotAnError(t *testing.T) {
	device := &fakeTasmota{}
	lights := startLights(t, device)

	lights.Show(MoodIdle, "a")
	eventually(t, "the idle mood to be recorded", func() bool { return lights.Status().Mood == MoodIdle })

	if got, status := device.commands(), lights.Status(); len(got) != 0 || status.Error != "" {
		t.Errorf("an idle mood with no scene sent %q and recorded error %q", got, status.Error)
	}
}
