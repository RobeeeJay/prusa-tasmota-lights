package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// replyLimit bounds how much of a Tasmota answer is read. A command answer is a
// few hundred bytes; a status dump is never asked for.
const replyLimit = 16 << 10

var errRefused = errors.New("tasmota refused the command")

// Tasmota sends commands to one Tasmota device over its HTTP command endpoint.
type Tasmota struct {
	Base   string
	Client *http.Client
}

// Commands splits a scene into the commands it runs, in order.
func Commands(scene string) []string {
	var out []string

	for part := range strings.SplitSeq(scene, ";") {
		if command := strings.TrimSpace(part); command != "" {
			out = append(out, command)
		}
	}

	return out
}

// Run sends a scene one command per request. Backlog would take one request,
// but it answers {} even for a command Tasmota does not know, so a typo in a
// scene would fail silently.
func (t Tasmota) Run(ctx context.Context, scene string) error {
	for _, command := range Commands(scene) {
		if err := t.send(ctx, command); err != nil {
			return err
		}
	}

	return nil
}

func (t Tasmota) send(ctx context.Context, command string) error {
	address := strings.TrimRight(t.Base, "/") + "/cm?cmnd=" + url.QueryEscape(command)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("%q: %w", command, err)
	}

	response, err := t.Client.Do(request)
	if err != nil {
		return fmt.Errorf("%q: %w", command, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, replyLimit))
	if err != nil {
		return fmt.Errorf("%q: read the answer: %w", command, err)
	}

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%q: %w: HTTP %d", command, errRefused, response.StatusCode)
	}

	return refusal(command, body)
}

// refusal reads the ways Tasmota says no while answering 200: an unknown
// command, a malformed one, and a web password it was not given.
func refusal(command string, body []byte) error {
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(body, &reply); err != nil {
		return fmt.Errorf("%q: %w: the answer is not JSON: %.80q", command, errRefused, body)
	}

	if warning, ok := reply["WARNING"]; ok {
		return fmt.Errorf("%q: %w: %s", command, errRefused, warning)
	}

	var said string
	if json.Unmarshal(reply["Command"], &said) == nil && (said == "Unknown" || said == "Error") {
		return fmt.Errorf("%q: %w: Tasmota answered %q", command, errRefused, said)
	}

	return nil
}

// newClient is the HTTP client the module talks to Tasmota with. A Tasmota on
// weak WiFi answers in well under a second; anything slower is gone.
func newClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }
