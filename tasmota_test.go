package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
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

// defaultScenes reads the scenes default out of module.yaml, which is where the
// module's defaults live. There is no YAML parser in the build, so it joins the
// folded block under "default: >-" and reads that as the JSON it is.
func defaultScenes(t *testing.T) map[string]string {
	t.Helper()

	manifest, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatal(err)
	}

	_, after, found := strings.Cut(string(manifest), "- key: scenes\n")
	if !found {
		t.Fatal("module.yaml has no scenes option")
	}

	_, after, found = strings.Cut(after, "default: >-\n")
	if !found {
		t.Fatal("the scenes option has no folded default")
	}

	var block []string
	for line := range strings.SplitSeq(after, "\n") {
		if !strings.HasPrefix(line, "      ") {
			break
		}
		block = append(block, strings.TrimSpace(line))
	}

	var scenes map[string]string
	if err := json.Unmarshal([]byte(strings.Join(block, " ")), &scenes); err != nil {
		t.Fatalf("the scenes default %q is not JSON: %v", strings.Join(block, " "), err)
	}

	return scenes
}

// On an RGBW light split in two (SetOption37 128), a six-digit Color1 also
// writes the white channel as 00 and switches Power2 off, so the next Power2 ON
// lit the white at 0%. Seen on the LC04 on 27 November 2026: Dimmer2 went from
// 57 to 0. HSBColor1 sets the colour and leaves the white alone.
func TestDefaultScenesNeverWriteTheWhiteChannel(t *testing.T) {
	colour := regexp.MustCompile(`(?i)^color[0-9]*\s+[0-9a-f#]`)

	for mood, scene := range defaultScenes(t) {
		for _, command := range Commands(scene) {
			if colour.MatchString(command) {
				t.Errorf("the %s scene sends %q, which zeroes the white light; use HSBColor1", mood, command)
			}
		}
	}
}

// A finished print is green; a printer stopped for a person (an error, the
// printer asking for attention, or a pause, which is what Obico sends when it
// sees a failure) is red.
func TestDefaultScenesShowGreenForFinishedAndRedForStopped(t *testing.T) {
	scenes := defaultScenes(t)

	want := map[Mood]string{
		MoodFinished:  "HSBColor1 120,100,100",
		MoodError:     "HSBColor1 0,100,100",
		MoodAttention: "HSBColor1 0,100,100",
		MoodPaused:    "HSBColor1 0,100,100",
	}

	for mood, colour := range want {
		if commands := Commands(scenes[string(mood)]); !slices.Contains(commands, colour) {
			t.Errorf("the %s scene is %q, want it to include %q", mood, commands, colour)
		}
	}
}

// Prusa Connect refuses to start a module whose binary answers a different
// version from the manifest it was installed with. Version 0.2.0 shipped with
// module.yaml bumped and main.go still at 0.1.0, and the server marked it
// failed with RESOURCE_MANIFEST_DISAGREES.
func TestTheBinaryAnswersTheManifestsVersion(t *testing.T) {
	manifest, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatal(err)
	}

	found := regexp.MustCompile(`(?m)^version: (\S+)$`).FindSubmatch(manifest)
	if found == nil {
		t.Fatal("module.yaml has no version line")
	}

	if string(release) != string(found[1]) {
		t.Fatalf("the binary answers %s and module.yaml declares %s", release, found[1])
	}
}
