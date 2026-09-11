package main

import (
	"testing"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/presentation/wails"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// The payloads the application layer emits, against the event names init()
// registers them under. Adding an event means adding a line here.
var emittedEvents = []struct {
	name    string
	payload any
}{
	{"scan-progress", marmotapp.ScanProgress{}},
	{"cleanup-progress", marmotapp.CleanupProgress{}},
	{marmotapp.LiveUpdateEvent, marmotapp.LiveUpdate{}},
	{marmotapp.StorageSourcesChangedEvent, nil},
}

// An event whose data is not exactly the type its name was registered with is
// cancelled: not delivered, not raised to the window, nothing but a line in the
// application's error handler. A whole deletion once ran with the progress ring
// at 0% because of it, and neither the compiler nor any test said a word.
//
// So the pairing is checked against the real registry rather than against a
// second copy of the table. init() has already run in this binary, so the
// RegisterEvent calls in main.go are the ones being tested; the processor runs
// the same validation the app's emitter does, without needing an app.
func TestRegisteredEventsAcceptTheirViews(t *testing.T) {
	processor := application.NewWailsEventProcessor(func(*application.CustomEvent) {})
	for _, emitted := range emittedEvents {
		event := &application.CustomEvent{Name: emitted.name, Data: wails.EventView(emitted.payload)}
		if err := processor.Emit(event); err != nil {
			t.Errorf("%q would be dropped rather than delivered: %v", emitted.name, err)
		}
	}
}

// The other half: that the check above can fail at all. Emitting the raw
// application payload -- which is what main.go did before EventView -- must be
// refused, or the test above passes on an emitter that accepts anything.
func TestUnconvertedPayloadsAreRefused(t *testing.T) {
	processor := application.NewWailsEventProcessor(func(*application.CustomEvent) {})
	for _, emitted := range emittedEvents {
		if emitted.payload == nil {
			continue
		}
		event := &application.CustomEvent{Name: emitted.name, Data: emitted.payload}
		if err := processor.Emit(event); err == nil {
			t.Errorf("%q accepted an unconverted %T; this test proves nothing", emitted.name, emitted.payload)
		}
	}
}
