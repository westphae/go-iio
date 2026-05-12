// Package sysfs implements the IIO backend that reads /sys/bus/iio/devices
// and /dev/iio:deviceN directly. It registers itself as the default backend
// at init time so the core package works out of the box.
package sysfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/westphae/go-iio/backend"
)

// DefaultRoot is the standard sysfs path under which IIO devices live on
// Linux. Tests use NewWithRoot to point at a fixture instead.
const DefaultRoot = "/sys/bus/iio/devices"

// Backend is the sysfs implementation of backend.Backend.
type Backend struct {
	sysfsRoot string // typically "/sys/bus/iio/devices"
	devRoot   string // typically "/dev"
}

// New returns a Backend rooted at the standard Linux paths.
func New() *Backend {
	return &Backend{sysfsRoot: DefaultRoot, devRoot: "/dev"}
}

// NewWithRoot returns a Backend rooted at the given sysfs and /dev
// equivalents. Useful for tests against a mocked tree. The devRoot may be
// empty if the caller never opens a buffer stream.
func NewWithRoot(sysfsRoot, devRoot string) *Backend {
	return &Backend{sysfsRoot: sysfsRoot, devRoot: devRoot}
}

// Discover scans sysfsRoot for iio:deviceN entries.
func (b *Backend) Discover() ([]backend.DeviceLocator, error) {
	matches, err := filepath.Glob(filepath.Join(b.sysfsRoot, "iio:device*"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	out := make([]backend.DeviceLocator, 0, len(matches))
	for _, p := range matches {
		name, err := readTrimmed(filepath.Join(p, "name"))
		if err != nil {
			// Devices without a name file are unusual but not fatal.
			name = filepath.Base(p)
		}
		out = append(out, backend.DeviceLocator{Name: name, Addr: p})
	}
	return out, nil
}

// OpenByName returns the first device whose name matches exactly.
func (b *Backend) OpenByName(name string) (backend.DeviceHandle, error) {
	locs, err := b.Discover()
	if err != nil {
		return nil, err
	}
	for _, l := range locs {
		if l.Name == name {
			return b.OpenByLocator(l)
		}
	}
	// Fallback: substring match. Some kernels suffix the bus address.
	for _, l := range locs {
		if strings.Contains(l.Name, name) {
			return b.OpenByLocator(l)
		}
	}
	return nil, fmt.Errorf("sysfs: %w: device named %q", backend.ErrNotFound, name)
}

// OpenByLocator opens a specific sysfs device path.
func (b *Backend) OpenByLocator(loc backend.DeviceLocator) (backend.DeviceHandle, error) {
	if loc.Addr == "" {
		return nil, fmt.Errorf("sysfs: empty locator address")
	}
	info, err := os.Stat(loc.Addr)
	if err != nil {
		return nil, fmt.Errorf("sysfs: stat %s: %w", loc.Addr, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sysfs: %s is not a directory", loc.Addr)
	}
	return &device{
		backend: b,
		path:    loc.Addr,
		name:    loc.Name,
	}, nil
}

// Close is a no-op for sysfs.
func (b *Backend) Close() error { return nil }

func init() {
	backend.Register(New())
}
