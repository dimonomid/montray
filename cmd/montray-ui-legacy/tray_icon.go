package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"

	"github.com/dimonomid/montray"
)

var (
	trayIcons *iconCombiner

	iconFlashMtx  sync.Mutex
	iconFlashStop chan struct{}
	// iconFlashIcon is the non-transparent half of the active flash cycle.
	// Keeping it lets applyIcon distinguish a real visual change from an
	// incident update that only changes details or counts.
	iconFlashIcon []byte
)

const iconFlashInterval = 500 * time.Millisecond

// iconCombiner supplies the base tray icons, renders initialization progress,
// and composes a snoozed overlay when needed. Its cache and lock are private so
// callers only need to ask for the icon that represents a trayState.
type iconCombiner struct {
	icons       map[overallState][]byte
	transparent []byte

	// composedIcons avoids decoding and encoding PNGs for repeated snapshots
	// with the same alerting, initialization, and snoozed states. It is
	// populated lazily because most state combinations are never needed.
	composedIconsMtx sync.Mutex
	composedIcons    map[trayIconKey][]byte
}

// trayIconKey identifies a cached initialization-sector and snoozed-overlay
// composition. Server counts matter only when alerting is unknown.
type trayIconKey struct {
	alerting           overallState
	snoozed            overallState
	hasSnoozed         bool
	unknownServerCount int
	serverCount        int
}

type overallState int

const (
	overallStateUnknown overallState = iota
	overallStateOK
	overallStateInternalError
	overallStateWarning
	overallStateError
)

func (s overallState) String() string {
	switch s {
	case overallStateUnknown:
		return "unknown"
	case overallStateOK:
		return "ok"
	case overallStateInternalError:
		return "internal error"
	case overallStateWarning:
		return "warning"
	case overallStateError:
		return "error"
	default:
		return fmt.Sprintf("invalid state %d", s)
	}
}

func loadTrayIcons() {
	trayIcons = newIconCombiner()
}

func newIconCombiner() *iconCombiner {
	return &iconCombiner{
		icons: map[overallState][]byte{
			overallStateUnknown:       mustEmbeddedAsset("assets/status_gray.png"),
			overallStateOK:            mustEmbeddedAsset("assets/status_green.png"),
			overallStateInternalError: mustEmbeddedAsset("assets/status_magenta.png"),
			overallStateWarning:       mustEmbeddedAsset("assets/status_yellow.png"),
			overallStateError:         mustEmbeddedAsset("assets/status_red.png"),
		},
		transparent:   mustEmbeddedAsset("assets/status_transparent.png"),
		composedIcons: make(map[trayIconKey][]byte),
	}
}

func getOverallStateFromItems(items []montray.ItemWContext) overallState {
	ret := overallStateOK

	for _, item := range items {
		cur := getOverallStateFromItem(item)
		if ret < cur {
			ret = cur
		}
	}

	return ret
}

func getOverallStateFromItem(item montray.ItemWContext) overallState {
	if item.State == montray.ItemStateOK {
		return overallStateOK
	}

	if strings.HasPrefix(string(item.Key), "internal.") {
		return overallStateInternalError
	}

	if item.State == montray.ItemStateWarning {
		return overallStateWarning
	}

	return overallStateError
}

func applyIcon(state trayState) {
	applyIconWithSetter(state, systray.SetIcon)
}

func applyIconWithSetter(state trayState, setIcon func([]byte)) {
	icon := trayIcons.Icon(state)
	flashing := state.Alerting != overallStateOK && state.Alerting != overallStateUnknown

	iconFlashMtx.Lock()
	// Reapplying the same flashing icon must not recreate its ticker. Doing so
	// would immediately show the solid icon and restart the 500 ms interval,
	// making frequent incident updates visibly interrupt the flash cadence.
	if flashing && iconFlashStop != nil && bytes.Equal(iconFlashIcon, icon) {
		iconFlashMtx.Unlock()
		return
	}

	// A change between solid and flashing states, or between two different
	// flashing icons, replaces the previous cycle.
	if iconFlashStop != nil {
		close(iconFlashStop)
		iconFlashStop = nil
		iconFlashIcon = nil
	}

	if !flashing {
		setIcon(icon)
		iconFlashMtx.Unlock()
		return
	}

	stop := make(chan struct{})
	iconFlashStop = stop
	iconFlashIcon = icon
	setIcon(icon)
	iconFlashMtx.Unlock()

	go flashIcon(icon, trayIcons.transparent, stop, setIcon)
}

// trayStatusTitle summarizes active and snoozed incidents in the tray menu.
func trayStatusTitle(state trayState) string {
	incidentLabel := "incidents"
	if state.AlertingCount == 1 {
		incidentLabel = "incident"
	}
	title := fmt.Sprintf("Status: %d %s", state.AlertingCount, incidentLabel)
	if state.SnoozedCount > 0 {
		title += fmt.Sprintf(" + %d snoozed", state.SnoozedCount)
	}
	return title
}

// Icon returns the tray icon representing state. Solid icons without a
// snoozed overlay are returned directly; generated combinations are cached.
func (c *iconCombiner) Icon(state trayState) []byte {
	if state.Alerting != overallStateUnknown && state.Snoozed == nil {
		return c.iconForState(state.Alerting)
	}

	key := trayIconKey{alerting: state.Alerting}
	if state.Alerting == overallStateUnknown {
		key.unknownServerCount = state.UnknownServerCount
		key.serverCount = state.ServerCount
	}
	if state.Snoozed != nil {
		key.hasSnoozed = true
		key.snoozed = *state.Snoozed
	}
	c.composedIconsMtx.Lock()
	defer c.composedIconsMtx.Unlock()
	if icon, exists := c.composedIcons[key]; exists {
		return icon
	}

	icon := c.iconForAlertingState(state)
	if state.Snoozed != nil {
		icon = composeIcon(icon, c.iconForState(*state.Snoozed))
	}
	c.composedIcons[key] = icon
	return icon
}

// iconForAlertingState adds initialization progress to an unknown icon. Green
// starts at 12 o'clock and grows clockwise; the remaining sector is gray.
func (c *iconCombiner) iconForAlertingState(state trayState) []byte {
	if state.Alerting != overallStateUnknown {
		return c.iconForState(state.Alerting)
	}
	return composeInitializationIcon(
		c.iconForState(overallStateOK),
		c.iconForState(overallStateUnknown),
		state.UnknownServerCount,
		state.ServerCount,
	)
}

func (c *iconCombiner) iconForState(state overallState) []byte {
	icon, ok := c.icons[state]
	if !ok {
		panic(fmt.Sprintf("invalid state %d", state))
	}
	return icon
}

// composeInitializationIcon fills the known-server fraction clockwise from 12
// o'clock with green. A small sampling grid antialiases diagonal boundaries.
func composeInitializationIcon(okIcon, unknownIcon []byte, unknownServerCount, serverCount int) []byte {
	if unknownServerCount <= 0 && serverCount > 0 {
		return okIcon
	}
	if serverCount <= 0 || unknownServerCount >= serverCount {
		return unknownIcon
	}

	okImage, err := png.Decode(bytes.NewReader(okIcon))
	if err != nil {
		panic(fmt.Sprintf("decode OK tray icon: %s", err))
	}
	unknownImage, err := png.Decode(bytes.NewReader(unknownIcon))
	if err != nil {
		panic(fmt.Sprintf("decode unknown tray icon: %s", err))
	}
	if okImage.Bounds() != unknownImage.Bounds() {
		panic(fmt.Sprintf("initialization tray icon bounds differ: OK %v, unknown %v", okImage.Bounds(), unknownImage.Bounds()))
	}

	const samplesPerAxis = 4
	const totalSamples = samplesPerAxis * samplesPerAxis
	bounds := okImage.Bounds()
	centerX := float64(bounds.Min.X+bounds.Max.X) / 2
	centerY := float64(bounds.Min.Y+bounds.Max.Y) / 2
	knownAngle := 2 * math.Pi * float64(serverCount-unknownServerCount) / float64(serverCount)
	result := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			unknownSamples := 0
			for sampleY := 0; sampleY < samplesPerAxis; sampleY++ {
				for sampleX := 0; sampleX < samplesPerAxis; sampleX++ {
					xOffset := float64(x) + (float64(sampleX)+0.5)/samplesPerAxis - centerX
					yOffset := float64(y) + (float64(sampleY)+0.5)/samplesPerAxis - centerY
					angle := math.Atan2(xOffset, -yOffset)
					if angle < 0 {
						angle += 2 * math.Pi
					}
					if angle >= knownAngle {
						unknownSamples++
					}
				}
			}

			okColor := color.NRGBAModel.Convert(okImage.At(x, y)).(color.NRGBA)
			unknownColor := color.NRGBAModel.Convert(unknownImage.At(x, y)).(color.NRGBA)
			if okColor.A != unknownColor.A {
				panic(fmt.Sprintf("initialization tray icon alpha differs at (%d, %d)", x, y))
			}
			knownSamples := totalSamples - unknownSamples
			result.SetNRGBA(x, y, color.NRGBA{
				R: uint8((int(unknownColor.R)*unknownSamples + int(okColor.R)*knownSamples) / totalSamples),
				G: uint8((int(unknownColor.G)*unknownSamples + int(okColor.G)*knownSamples) / totalSamples),
				B: uint8((int(unknownColor.B)*unknownSamples + int(okColor.B)*knownSamples) / totalSamples),
				A: okColor.A,
			})
		}
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, result); err != nil {
		panic(fmt.Sprintf("encode initialization tray icon: %s", err))
	}
	return encoded.Bytes()
}

// composeIcon overlays a state icon, sized to 30% of the main icon, in its
// lower-right corner.
func composeIcon(mainIcon, overlayIcon []byte) []byte {
	base, err := png.Decode(bytes.NewReader(mainIcon))
	if err != nil {
		panic(fmt.Sprintf("decode main tray icon: %s", err))
	}
	overlay, err := png.Decode(bytes.NewReader(overlayIcon))
	if err != nil {
		panic(fmt.Sprintf("decode snoozed tray icon: %s", err))
	}

	bounds := base.Bounds()
	result := image.NewRGBA(bounds)
	draw.Draw(result, bounds, base, bounds.Min, draw.Src)

	size := bounds.Dx() * 30 / 100
	if size == 0 {
		return mainIcon
	}
	overlayBounds := overlay.Bounds()
	scaledOverlay := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			sourceX := overlayBounds.Min.X + x*overlayBounds.Dx()/size
			sourceY := overlayBounds.Min.Y + y*overlayBounds.Dy()/size
			scaledOverlay.Set(x, y, overlay.At(sourceX, sourceY))
		}
	}

	startX := bounds.Max.X - size
	startY := bounds.Max.Y - size
	draw.Draw(result, image.Rect(startX, startY, bounds.Max.X, bounds.Max.Y), scaledOverlay, image.Point{}, draw.Over)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, result); err != nil {
		panic(fmt.Sprintf("encode composed tray icon: %s", err))
	}
	return encoded.Bytes()
}

// flashIcon alternates a non-OK icon with a transparent icon until its state
// is replaced by another call to applyIcon.
func flashIcon(icon, transparentIcon []byte, stop <-chan struct{}, setIcon func([]byte)) {
	flashIconAtInterval(icon, transparentIcon, stop, setIcon, iconFlashInterval)
}

func flashIconAtInterval(icon, transparentIcon []byte, stop <-chan struct{}, setIcon func([]byte), interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// applyIcon has already displayed the solid icon, so the first tick must
	// hide it. Starting this as false would redundantly display it again on the
	// first tick and delay the first visible flash transition to two intervals.
	showIcon := true
	for {
		select {
		case <-ticker.C:
			showIcon = !showIcon

			iconFlashMtx.Lock()
			if iconFlashStop != stop {
				iconFlashMtx.Unlock()
				return
			}
			if showIcon {
				setIcon(icon)
			} else {
				setIcon(transparentIcon)
			}
			iconFlashMtx.Unlock()

		case <-stop:
			return
		}
	}
}
