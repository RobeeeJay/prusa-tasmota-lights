// Command tasmota-lights is a Prusa Connect module that sets the scene on a
// Tasmota light from what a printer is doing: warming up, waiting for filament,
// printing, finished, and the rest.
//
// The host starts it, speaks the plugin wire protocol over stdin and stdout,
// and copies stderr into its own log.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

const (
	name    = "tasmota-lights"
	release = extension.Version("0.2.0")

	// pageSize is how many devices one device.list call asks for.
	pageSize = 100
	// resubscribeAfter is the pause before following the fleet again after the
	// subscription ended, so a host that keeps refusing is not asked in a loop.
	resubscribeAfter = 5 * time.Second
)

//go:embed openapi.json
var openAPI []byte

// followed is every event type a mood is decided from.
var followed = []string{
	event.TypeDeviceTelemetrySampled,
	event.TypeDeviceIssuesChanged,
	event.TypeDeviceArrived,
	event.TypeDeviceDeparted,
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	line, err := wire.Handshake{
		Protocol: wire.ProtocolMajor,
		MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor,
		Address:  wire.StdioAddress,
	}.Line()
	if err != nil {
		return err
	}

	if _, err = os.Stdout.WriteString(line); err != nil {
		return err
	}

	serves := wirerpc.Methods{}

	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{Role: wirerpc.RoleChild, Methods: serves})
	if err != nil {
		return err
	}

	m := &module{log: slog.New(slog.NewTextHandler(os.Stderr, nil)), configured: make(chan Settings, 1)}

	serves[wirerpc.MethodInfo] = func(context.Context, wirerpc.Call) (any, error) {
		return routesapi.Describe(wirerpc.Info{
			Name: name, Version: release, Contract: wirerpc.Contract,
			Serves: []wirerpc.Method{wirerpc.MethodConfigure},
		}, openAPI)
	}
	serves[wirerpc.MethodShutdown] = func(context.Context, wirerpc.Call) (any, error) {
		stop()

		return nil, nil
	}
	serves[wirerpc.MethodConfigure] = wirerpc.Configure(func(_ context.Context, settings Settings) error {
		if err := settings.check(); err != nil {
			return err
		}

		select {
		case m.configured <- settings:
		default:
		}

		return nil
	})
	serves[routesapi.MethodServe] = routesapi.Serve(m.routes())

	go m.watch(ctx, conn)

	return conn.Serve(ctx)
}

type module struct {
	log        *slog.Logger
	configured chan Settings

	mu     sync.Mutex
	fleet  *Fleet
	lights *Lights
}

// watch waits for the settings, then follows the fleet for as long as the
// module runs.
func (m *module) watch(ctx context.Context, conn *wirerpc.Conn) {
	var settings Settings

	select {
	case settings = <-m.configured:
	case <-ctx.Done():
		return
	}

	fleet := NewFleet(settings)
	lights := NewLights(Tasmota{Base: settings.TasmotaURL, Client: newClient()}, settings.Scenes, m.log)

	m.mu.Lock()
	m.fleet, m.lights = fleet, lights
	m.mu.Unlock()

	go lights.Run(ctx)

	devices := deviceapi.NewClient(conn)

	for ctx.Err() == nil {
		if err := follow(ctx, conn, devices, fleet, lights); err != nil && ctx.Err() == nil {
			m.log.Warn("following the printers stopped, and starts again shortly", "error", err.Error())
		}

		select {
		case <-time.After(resubscribeAfter):
		case <-ctx.Done():
		}
	}
}

// follow reads every printer's record, then keeps the moods current from the
// event stream. The records are read again on the first window and whenever
// the host says continuity is lost, so nothing that happened between the read
// and the subscription is missed.
func follow(ctx context.Context, conn *wirerpc.Conn, devices *deviceapi.Client, fleet *Fleet, lights *Lights) error {
	if err := seed(ctx, devices, fleet); err != nil {
		return err
	}

	lights.Show(fleet.Mood())

	first := true

	return busapi.Subscribe(ctx, conn, busapi.SubscribeParams{Types: followed}, func(window busapi.Window) error {
		if first || window.Resync {
			if err := seed(ctx, devices, fleet); err != nil {
				return err
			}

			first = false
		}

		for _, id := range fleet.Window(window.Telemetry, window.Events) {
			if device, err := devices.Get(ctx, deviceapi.GetParams{ID: id}); err == nil {
				fleet.Seed(device)
			}
		}

		lights.Show(fleet.Mood())

		return nil
	})
}

func seed(ctx context.Context, devices *deviceapi.Client, fleet *Fleet) error {
	for offset := 0; ; offset += pageSize {
		page, err := devices.List(ctx, deviceapi.ListParams{Limit: pageSize, Offset: offset})
		if err != nil {
			return fmt.Errorf("list the printers: %w", err)
		}

		fleet.Seed(page.Items...)

		if len(page.Items) < pageSize || offset+len(page.Items) >= page.Total {
			return nil
		}
	}
}

// routes serves GET /status, which says what the module sees and what the
// lights last did. The host takes view_resource on the instance before it.
func (m *module) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		fleet, lights := m.fleet, m.lights
		m.mu.Unlock()

		status := map[string]any{"configured": fleet != nil, "printers": []PrinterStatus{}}
		if fleet != nil {
			status["printers"] = fleet.Status()
			status["lights"] = lights.Status()
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})

	return mux
}

// stdio is the channel the host handed this process.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }
