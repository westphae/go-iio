// Package iio is a Go library for reading sensors exposed by the Linux
// Industrial I/O (IIO) subsystem.
//
// Devices brought up via a device tree overlay (or a manual
// /sys/bus/i2c/devices/i2c-N/new_device write) appear under
// /sys/bus/iio/devices/iio:deviceN. This package gives you typed polled reads
// of those devices' channels, plus a buffered-capture API that decodes
// /dev/iio:deviceN streams into per-channel float64 samples with timestamps.
//
// The package is backend-agnostic: by default it uses the pure-Go sysfs
// backend (registered automatically), but a libiio/cgo backend can be added
// later without changing application code.
package iio

import (
	"fmt"

	"github.com/westphae/go-iio/backend"
	// Register the default sysfs backend at import time.
	_ "github.com/westphae/go-iio/backend/sysfs"
)

// DeviceInfo identifies one IIO device returned by Discover.
type DeviceInfo struct {
	Name string
	Path string // backend-specific identifier
}

// Discover returns every IIO device the default backend can see.
func Discover() ([]DeviceInfo, error) {
	b := backend.Default()
	if b == nil {
		return nil, fmt.Errorf("iio: no backend registered")
	}
	locs, err := b.Discover()
	if err != nil {
		return nil, err
	}
	out := make([]DeviceInfo, len(locs))
	for i, l := range locs {
		out[i] = DeviceInfo{Name: l.Name, Path: l.Addr}
	}
	return out, nil
}

// Open returns the first IIO device whose kernel-reported name matches the
// given string. Substring matches are tolerated as a fallback to handle minor
// driver naming variations.
func Open(name string) (*Device, error) {
	b := backend.Default()
	if b == nil {
		return nil, fmt.Errorf("iio: no backend registered")
	}
	h, err := b.OpenByName(name)
	if err != nil {
		return nil, err
	}
	return newDevice(h)
}

// OpenPath opens the device at the given backend-specific address (for the
// sysfs backend, an absolute path under /sys/bus/iio/devices).
func OpenPath(path string) (*Device, error) {
	b := backend.Default()
	if b == nil {
		return nil, fmt.Errorf("iio: no backend registered")
	}
	h, err := b.OpenByLocator(backend.DeviceLocator{Addr: path})
	if err != nil {
		return nil, err
	}
	return newDevice(h)
}
