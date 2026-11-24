package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LightStatus is what the lights last did, for the status route.
type LightStatus struct {
	Mood    Mood      `json:"mood"`
	Printer string    `json:"printer,omitempty"`
	SentAt  time.Time `json:"sent_at,omitzero"`
	Error   string    `json:"error,omitempty"`
}

// Lights sends a scene when the mood changes. It never repeats a scene that
// went through, so a person who changes the lights with the remote keeps their
// choice until the printer does something new.
type Lights struct {
	Tasmota Tasmota
	Scenes  map[string]string
	Retry   time.Duration
	Log     *slog.Logger

	mu      sync.Mutex
	pending *LightStatus
	status  LightStatus
	wake    chan struct{}
}

func NewLights(tasmota Tasmota, scenes map[string]string, log *slog.Logger) *Lights {
	return &Lights{Tasmota: tasmota, Scenes: scenes, Retry: 15 * time.Second, Log: log, wake: make(chan struct{}, 1)}
}

// Show asks for a mood. Only the newest request is kept, so a burst of
// changes sends the scene the printer settled on.
func (l *Lights) Show(mood Mood, printer string) {
	l.mu.Lock()
	l.pending = &LightStatus{Mood: mood, Printer: printer}
	l.mu.Unlock()

	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *Lights) Status() LightStatus {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.status
}

// Run sends scenes until ctx ends.
func (l *Lights) Run(ctx context.Context) {
	var shown Mood

	retry := time.NewTimer(0)
	<-retry.C

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-retry.C:
		}

		l.mu.Lock()
		want := l.pending
		l.mu.Unlock()

		if want == nil || (want.Mood == shown && l.Status().Error == "") {
			continue
		}

		err := l.Tasmota.Run(ctx, l.Scenes[string(want.Mood)])

		l.mu.Lock()
		l.status = LightStatus{Mood: want.Mood, Printer: want.Printer, SentAt: time.Now()}
		if err != nil {
			l.status.Error = err.Error()
		}
		l.mu.Unlock()

		if err != nil {
			l.Log.Warn("the lights did not change", "mood", want.Mood, "error", err.Error())
			retry.Reset(l.Retry)

			continue
		}

		l.Log.Info("the lights show a new mood", "mood", want.Mood, "printer", want.Printer)
		shown = want.Mood
	}
}
