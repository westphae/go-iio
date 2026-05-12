// Package backend defines the narrow contract the iio core depends on.
//
// The core package (github.com/westphae/go-iio) is responsible for parsing
// scan_elements type strings, computing buffer layouts, and decoding samples.
// A Backend implementation only has to surface string attributes and a bulk
// byte stream — making it straightforward to add a libiio (cgo) backend later
// without touching parsing code.
package backend

import (
	"errors"
	"io"
	"sync"
)

// Backend enumerates and opens IIO devices.
type Backend interface {
	// Discover lists every IIO device the backend can see.
	Discover() ([]DeviceLocator, error)
	// OpenByName opens the first device whose Name matches.
	OpenByName(name string) (DeviceHandle, error)
	// OpenByLocator opens a specific device returned from Discover.
	OpenByLocator(loc DeviceLocator) (DeviceHandle, error)
	// Close releases any resources held by the backend itself
	// (the sysfs backend is stateless and returns nil).
	Close() error
}

// DeviceLocator identifies one IIO device within a backend.
type DeviceLocator struct {
	Name string // kernel-reported name, e.g. "bmp280"
	Addr string // backend-specific identifier; for sysfs this is the device's sysfs path
}

// DeviceHandle is the per-device contract. Intentionally narrow: every higher
// level operation (channel discovery, buffered decode, etc.) is built on top
// of these primitives in the core package.
type DeviceHandle interface {
	Name() string
	Locator() DeviceLocator

	// ListAttrs enumerates every attribute the device exposes — both channel
	// attributes (Channel != "") and device-level attributes (Channel == "").
	ListAttrs() ([]AttrLocator, error)
	ReadAttr(a AttrLocator) (string, error)
	WriteAttr(a AttrLocator, value string) error

	// OpenBufferStream returns a reader over the device's buffered byte stream
	// (sysfs: /dev/iio:deviceN). Callers must Close the returned reader.
	OpenBufferStream() (io.ReadCloser, error)

	Close() error
}

// AttrLocator names an attribute. Channel is the channel name without the
// "in_" prefix or "_<suffix>" tail (e.g. "temp", "pressure"); empty Channel
// means the attribute lives at the device level (e.g. "buffer/enable",
// "trigger/current_trigger", "current_timestamp_clock").
//
// Name is the suffix that distinguishes the attribute — for channel attrs,
// values like "raw", "input", "scale", "offset", "oversampling_ratio",
// "en", "index", "type" (the last three under scan_elements/). For device
// attrs, the full relative path is used (e.g. "buffer/enable").
type AttrLocator struct {
	Channel string
	Name    string
}

// ErrNotFound is returned when a device or attribute does not exist.
var ErrNotFound = errors.New("iio: not found")

var (
	defaultMu sync.RWMutex
	defaultBe Backend
)

// Register sets the default backend used by the core package. Backends
// typically call this from an init() so callers can just import them for
// side effects.
func Register(b Backend) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultBe = b
}

// Default returns the currently registered default backend.
func Default() Backend {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultBe
}
