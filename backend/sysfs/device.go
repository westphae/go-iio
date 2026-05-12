package sysfs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/westphae/go-iio/backend"
)

type device struct {
	backend *Backend
	path    string // /sys/bus/iio/devices/iio:deviceN
	name    string
}

func (d *device) Name() string { return d.name }

func (d *device) Locator() backend.DeviceLocator {
	return backend.DeviceLocator{Name: d.name, Addr: d.path}
}

// ListAttrs enumerates every readable file under the device directory and
// classifies it as either a channel attribute (Channel != "") or a
// device-level attribute (Channel == "").
//
// Channel attributes follow the IIO ABI naming convention "in_<channel>_<attr>"
// at the top level (e.g. "in_temp_raw", "in_pressure_scale") and the same
// pattern under "scan_elements/" (e.g. "scan_elements/in_temp_en"). For
// scan_elements attrs we keep the "scan_elements/" prefix on the Name so the
// caller can distinguish them.
func (d *device) ListAttrs() ([]backend.AttrLocator, error) {
	var out []backend.AttrLocator

	walk := func(dir, prefix string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			loc := classifyAttr(name)
			if prefix != "" {
				loc.Name = prefix + loc.Name
			}
			out = append(out, loc)
		}
		return nil
	}

	if err := walk(d.path, ""); err != nil {
		return nil, err
	}
	// scan_elements is the only nested dir whose contents are channel attrs.
	if _, err := os.Stat(filepath.Join(d.path, "scan_elements")); err == nil {
		if err := walk(filepath.Join(d.path, "scan_elements"), "scan_elements/"); err != nil {
			return nil, err
		}
	}
	// buffer/, buffer0/, trigger/ are device-level subdirs; surface their files.
	for _, sub := range []string{"buffer", "buffer0", "trigger"} {
		p := filepath.Join(d.path, sub)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				// Files like buffer0/in_temp_en are *channel* attrs even though
				// they live under buffer0/; modern kernels alias them with
				// scan_elements/. Treat them as device-level to avoid double
				// counting — the canonical channel attrs come from
				// scan_elements/.
				out = append(out, backend.AttrLocator{Name: sub + "/" + e.Name()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// knownChannelAttrs is the set of recognized channel attribute suffixes from
// the IIO ABI (Documentation/ABI/testing/sysfs-bus-iio). Listed
// longest-first so that "_oversampling_ratio_available" matches before
// "_oversampling_ratio". This is the only reliable way to split
// "in_pressure_oversampling_ratio" into channel="pressure" /
// attr="oversampling_ratio" — last-underscore heuristics get it wrong.
var knownChannelAttrs = []string{
	"oversampling_ratio_available",
	"sampling_frequency_available",
	"oversampling_ratio",
	"sampling_frequency",
	"integration_time",
	"peak_raw",
	"peak_scale",
	"mean_raw",
	"calibbias",
	"calibscale",
	"input",
	"scale",
	"offset",
	"raw",
	// Suffixes only valid under scan_elements/: en, index, type. Including
	// them here means the top-level walk would also accept them, but the
	// top-level directory has no such files for any kernel-supplied IIO
	// driver, so there's no ambiguity.
	"index",
	"type",
	"en",
}

// classifyAttr splits a filename into a backend.AttrLocator. Files matching
// "in_<channel>_<suffix>" become channel attrs; everything else stays at the
// device level. Channel names themselves can contain underscores (e.g.
// "accel_x"), so we match against the known set of attribute suffixes.
func classifyAttr(filename string) backend.AttrLocator {
	if !strings.HasPrefix(filename, "in_") {
		return backend.AttrLocator{Name: filename}
	}
	rest := strings.TrimPrefix(filename, "in_")
	for _, suf := range knownChannelAttrs {
		if strings.HasSuffix(rest, "_"+suf) {
			return backend.AttrLocator{
				Channel: strings.TrimSuffix(rest, "_"+suf),
				Name:    suf,
			}
		}
	}
	return backend.AttrLocator{Name: filename}
}

// attrPath turns an AttrLocator back into the sysfs path under d.path.
func (d *device) attrPath(a backend.AttrLocator) string {
	if a.Channel == "" {
		// Device-level: Name may already contain "subdir/file" (e.g.
		// "buffer/enable", "scan_elements/in_temp_en").
		return filepath.Join(d.path, a.Name)
	}
	// Channel attr. If Name has a "scan_elements/" prefix the caller passed
	// it explicitly; otherwise it's a top-level in_<channel>_<name> file.
	if strings.HasPrefix(a.Name, "scan_elements/") {
		suffix := strings.TrimPrefix(a.Name, "scan_elements/")
		return filepath.Join(d.path, "scan_elements", "in_"+a.Channel+"_"+suffix)
	}
	return filepath.Join(d.path, "in_"+a.Channel+"_"+a.Name)
}

func (d *device) ReadAttr(a backend.AttrLocator) (string, error) {
	return readTrimmed(d.attrPath(a))
}

func (d *device) WriteAttr(a backend.AttrLocator, value string) error {
	p := d.attrPath(a)
	if err := os.WriteFile(p, []byte(value), 0); err != nil {
		return fmt.Errorf("sysfs: write %s=%q: %w", p, value, err)
	}
	return nil
}

func (d *device) OpenBufferStream() (io.ReadCloser, error) {
	// /dev/iio:deviceN matches the basename of the sysfs path.
	dev := filepath.Join(d.backend.devRoot, filepath.Base(d.path))
	f, err := os.OpenFile(dev, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("sysfs: open %s: %w", dev, err)
	}
	return f, nil
}

func (d *device) Close() error { return nil }
