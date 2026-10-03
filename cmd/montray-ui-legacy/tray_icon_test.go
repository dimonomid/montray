package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/benbjohnson/clock"

	"github.com/dimonomid/montray"
	"github.com/dimonomid/montray/wsclient"
)

func TestComposeIconRespectsOverlayTransparency(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			base.Set(x, y, color.RGBA{B: 255, A: 255})
		}
	}

	overlay := image.NewRGBA(image.Rect(0, 0, 4, 4))
	// The source pixel mapped to the lower-right output pixel is half-opaque
	// red. Other mapped pixels stay transparent.
	overlay.Set(0, 0, color.RGBA{R: 255, A: 128})

	result, err := png.Decode(bytes.NewReader(composeIcon(encodePNG(t, base), encodePNG(t, overlay))))
	if err != nil {
		t.Fatal(err)
	}

	if got := color.RGBAModel.Convert(result.At(2, 2)).(color.RGBA); got != (color.RGBA{B: 255, A: 255}) {
		t.Fatalf("transparent overlay pixel erased the main icon: %#v", got)
	}
	got := color.RGBAModel.Convert(result.At(3, 3)).(color.RGBA)
	if got.A != 255 || got.R == 0 || got.B == 0 {
		t.Fatalf("semi-transparent overlay was not alpha-composited: %#v", got)
	}
}

func TestIconCombinerCachesComposedIcons(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 4, 4))
	base.Set(0, 0, color.RGBA{A: 255})
	icon := encodePNG(t, base)
	combiner := &iconCombiner{
		icons: map[overallState][]byte{
			overallStateWarning: icon,
			overallStateError:   icon,
		},
		composedIcons: make(map[trayIconKey][]byte),
	}
	snoozed := overallStateError
	state := trayState{Alerting: overallStateWarning, Snoozed: &snoozed}

	combiner.Icon(state)
	combiner.Icon(state)

	if got := len(combiner.composedIcons); got != 1 {
		t.Fatalf("got %d cached composed icons, want 1", got)
	}
}

func TestUnknownTrayIconIsGrayscaleWithOKIconAlpha(t *testing.T) {
	okIcon, err := png.Decode(bytes.NewReader(mustEmbeddedAsset("assets/status_green.png")))
	if err != nil {
		t.Fatal(err)
	}
	unknownIcon, err := png.Decode(bytes.NewReader(mustEmbeddedAsset("assets/status_gray.png")))
	if err != nil {
		t.Fatal(err)
	}
	if okIcon.Bounds() != unknownIcon.Bounds() {
		t.Fatalf("unknown icon bounds = %v, want %v", unknownIcon.Bounds(), okIcon.Bounds())
	}

	for y := okIcon.Bounds().Min.Y; y < okIcon.Bounds().Max.Y; y++ {
		for x := okIcon.Bounds().Min.X; x < okIcon.Bounds().Max.X; x++ {
			_, _, _, okAlpha := okIcon.At(x, y).RGBA()
			red, green, blue, unknownAlpha := unknownIcon.At(x, y).RGBA()
			if unknownAlpha != okAlpha {
				t.Fatalf("unknown icon alpha at (%d, %d) = %d, want %d", x, y, unknownAlpha, okAlpha)
			}
			if red != green || green != blue {
				t.Fatalf("unknown icon pixel at (%d, %d) is not gray", x, y)
			}
		}
	}
}

func TestUnknownTrayIconShowsServerInitializationProgress(t *testing.T) {
	combiner := newIconCombiner()
	okIcon, err := png.Decode(bytes.NewReader(combiner.iconForState(overallStateOK)))
	if err != nil {
		t.Fatal(err)
	}
	unknownIcon, err := png.Decode(bytes.NewReader(combiner.iconForState(overallStateUnknown)))
	if err != nil {
		t.Fatal(err)
	}

	if got := combiner.Icon(trayState{Alerting: overallStateUnknown, ServerCount: 4}); !bytes.Equal(got, combiner.iconForState(overallStateOK)) {
		t.Fatal("zero unknown servers did not produce the OK icon")
	}
	if got := combiner.Icon(trayState{Alerting: overallStateUnknown, UnknownServerCount: 4, ServerCount: 4}); !bytes.Equal(got, combiner.iconForState(overallStateUnknown)) {
		t.Fatal("all unknown servers did not produce the unknown icon")
	}

	points := map[string]image.Point{
		"top right":    image.Pt(25, 10),
		"bottom right": image.Pt(30, 25),
		"bottom left":  image.Pt(15, 30),
		"top left":     image.Pt(10, 15),
	}
	tests := []struct {
		name       string
		unknown    int
		grayPoints map[string]bool
	}{
		{name: "one quarter unknown", unknown: 1, grayPoints: map[string]bool{"top left": true}},
		{name: "three quarters unknown", unknown: 3, grayPoints: map[string]bool{
			"bottom right": true, "bottom left": true, "top left": true,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := png.Decode(bytes.NewReader(combiner.Icon(trayState{
				Alerting:           overallStateUnknown,
				UnknownServerCount: test.unknown,
				ServerCount:        4,
			})))
			if err != nil {
				t.Fatal(err)
			}
			for name, point := range points {
				wantImage := okIcon
				if test.grayPoints[name] {
					wantImage = unknownIcon
				}
				gotColor := color.NRGBAModel.Convert(got.At(point.X, point.Y)).(color.NRGBA)
				wantColor := color.NRGBAModel.Convert(wantImage.At(point.X, point.Y)).(color.NRGBA)
				if gotColor != wantColor {
					t.Errorf("%s pixel = %#v, want %#v", name, gotColor, wantColor)
				}
			}
		})
	}
}

func TestTrayStatusTitle(t *testing.T) {
	if got, want := trayStatusTitle(trayState{}), "Status: 0 incidents"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := trayStatusTitle(trayState{AlertingCount: 1}), "Status: 1 incident"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := trayStatusTitle(trayState{AlertingCount: 2, SnoozedCount: 1}), "Status: 2 incidents + 1 snoozed"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestApplyIconPreservesUnchangedFlashCycle verifies that incident updates
// which leave the rendered warning icon unchanged do not stop and recreate the
// flash ticker, resetting its visible/transparent cadence.
func TestApplyIconPreservesUnchangedFlashCycle(t *testing.T) {
	originalTrayIcons := trayIcons
	defer func() {
		iconFlashMtx.Lock()
		if iconFlashStop != nil {
			close(iconFlashStop)
			iconFlashStop = nil
			iconFlashIcon = nil
		}
		iconFlashMtx.Unlock()
		trayIcons = originalTrayIcons
	}()

	iconFlashMtx.Lock()
	if iconFlashStop != nil {
		close(iconFlashStop)
	}
	iconFlashStop = nil
	iconFlashIcon = nil
	iconFlashMtx.Unlock()

	warningIcon := []byte("warning")
	trayIcons = &iconCombiner{
		icons: map[overallState][]byte{
			overallStateOK:      []byte("ok"),
			overallStateWarning: warningIcon,
		},
		transparent:   []byte("transparent"),
		composedIcons: make(map[trayIconKey][]byte),
	}
	setIcon := func([]byte) {}

	applyIconWithSetter(trayState{Alerting: overallStateWarning, AlertingCount: 1}, setIcon)
	iconFlashMtx.Lock()
	firstStop := iconFlashStop
	iconFlashMtx.Unlock()
	if firstStop == nil {
		t.Fatal("warning icon did not start flashing")
	}

	applyIconWithSetter(trayState{Alerting: overallStateWarning, AlertingCount: 2}, setIcon)
	iconFlashMtx.Lock()
	secondStop := iconFlashStop
	iconFlashMtx.Unlock()

	if secondStop != firstStop {
		t.Fatal("unchanged flashing icon restarted its flash cycle")
	}
	select {
	case <-firstStop:
		t.Fatal("unchanged flashing icon stopped its existing flash cycle")
	default:
	}
}

// TestFlashIconFirstTickHidesIcon verifies that the solid icon displayed by
// applyIcon is followed by the transparent phase after one interval, rather
// than being displayed redundantly for a second interval.
func TestFlashIconFirstTickHidesIcon(t *testing.T) {
	icon := []byte("warning")
	transparentIcon := []byte("transparent")
	stop := make(chan struct{})
	changes := make(chan []byte, 1)
	done := make(chan struct{})

	iconFlashMtx.Lock()
	iconFlashStop = stop
	iconFlashMtx.Unlock()
	defer func() {
		close(stop)
		<-done
		iconFlashMtx.Lock()
		iconFlashStop = nil
		iconFlashIcon = nil
		iconFlashMtx.Unlock()
	}()

	go func() {
		flashIconAtInterval(icon, transparentIcon, stop, func(got []byte) {
			select {
			case changes <- got:
			default:
			}
		}, time.Millisecond)
		close(done)
	}()

	select {
	case got := <-changes:
		if !bytes.Equal(got, transparentIcon) {
			t.Fatalf("first flash transition = %q, want transparent icon", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first flash transition did not occur")
	}
}

func TestOnIncidentUpdateDerivesTrayState(t *testing.T) {
	var received []trayState
	core := &montrayLegacyCore{
		statusWebserver: newStatusWebserver(statusWebserverParams{Logger: watchTestLogger}),
		onIconState: func(state trayState) {
			received = append(received, state)
		},
	}

	core.onIncidentUpdate(incidentSnapshot{
		Alerting: []montray.ItemWContext{{
			Item: montray.Item{State: montray.ItemStateWarning},
		}},
		Snoozed: []snoozedIncident{{
			ItemWContext: montray.ItemWContext{Item: montray.Item{State: montray.ItemStateError}},
			SnoozedUntil: time.Now().Add(time.Hour),
		}},
	})
	if len(received) != 1 {
		t.Fatalf("got %d tray updates, want 1", len(received))
	}
	if received[0].Alerting != overallStateWarning || received[0].AlertingCount != 1 || received[0].Snoozed == nil || *received[0].Snoozed != overallStateError || received[0].SnoozedCount != 1 {
		t.Fatalf("unexpected tray state: %#v", received[0])
	}

	core.onIncidentUpdate(incidentSnapshot{})
	if len(received) != 2 || received[1].Alerting != overallStateOK || received[1].AlertingCount != 0 || received[1].Snoozed != nil || received[1].SnoozedCount != 0 {
		t.Fatalf("unexpected empty tray state: %#v", received)
	}
}

func TestOverallStateRemainsUnknownUntilEveryServerHasAnOutcome(t *testing.T) {
	var received []trayState
	core := &montrayLegacyCore{
		incidentState: &incidentState{
			snoozes: &snoozeState{snoozed: make(map[string]snoozeEntry)},
			clock:   clock.New(),
		},
		statusWebserver: newStatusWebserver(statusWebserverParams{Logger: watchTestLogger}),
		onIconState: func(state trayState) {
			received = append(received, state)
		},
		serverStatuses: map[string]serverStatus{
			"first":  {ID: "first"},
			"second": {ID: "second"},
			"third":  {ID: "third"},
		},
		serverIDs: []string{"first", "second", "third"},
	}
	core.incidentState.OnUpdate = core.onIncidentUpdate

	core.onConnectionEvent("first", wsclient.ConnectionEvent{
		EventKind: wsclient.EventKindConnected,
		Time:      time.Now(),
	})
	state := received[len(received)-1]
	if state.Alerting != overallStateUnknown || state.UnknownServerCount != 2 || state.ServerCount != 3 {
		t.Fatalf("state after first server connects = %#v, want unknown with 2/3 servers pending", state)
	}

	core.onConnectionEvent("second", wsclient.ConnectionEvent{
		EventKind: wsclient.EventKindDisconnected,
		Time:      time.Now(),
	})
	if !core.serverStatuses["second"].hasConnectedOrFailed {
		t.Fatal("failed connection was not recorded as an observed outcome")
	}

	connectionIncident := &montray.ItemWContext{
		Item: montray.Item{Key: "internal.connection.second", State: montray.ItemStateError},
	}
	core.incidentState.Update([]*montray.ItemWContext{connectionIncident})
	state = received[len(received)-1]
	if state.Alerting != overallStateInternalError || state.UnknownServerCount != 1 || state.ServerCount != 3 {
		t.Fatalf("state with a connection failure = %#v, want internal error with 1/3 servers pending", state)
	}

	core.onConnectionEvent("second", wsclient.ConnectionEvent{
		EventKind: wsclient.EventKindConnected,
		Time:      time.Now(),
	})
	core.incidentState.Update(nil)
	state = received[len(received)-1]
	if state.Alerting != overallStateUnknown || state.UnknownServerCount != 1 || state.ServerCount != 3 {
		t.Fatalf("state after second server reconnects = %#v, want unknown with 1/3 servers pending", state)
	}

	core.onConnectionEvent("third", wsclient.ConnectionEvent{
		EventKind: wsclient.EventKindConnected,
		Time:      time.Now(),
	})
	state = received[len(received)-1]
	if state.Alerting != overallStateOK || state.UnknownServerCount != 0 || state.ServerCount != 3 {
		t.Fatalf("state after every server responds = %#v, want OK with 0/3 servers pending", state)
	}
}

func encodePNG(t *testing.T, source image.Image) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
