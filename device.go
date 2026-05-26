package iio

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/westphae/go-iio/backend"
)

// Device represents one open IIO device.
type Device struct {
	h    backend.DeviceHandle
	chs  []*Channel
	byCh map[string]*Channel
}

// Channel is one data channel of a device. Channels are discovered when the
// device is opened and reflect the static metadata (type, index, scale,
// offset) at that point. Call ReloadScale to pick up scale/offset changes
// induced by changing oversampling or sampling frequency.
type Channel struct {
	dev    *Device
	name   string
	hasRaw bool // in_<name>_raw exists
	hasIn  bool // in_<name>_input exists (kernel-scaled)
	scale  float64
	offset float64
	hasSc  bool
	hasOff bool

	// Scan-element metadata; populated when scan_elements/in_<name>_type
	// exists. Used by buffer decoding.
	scan    ScanChannel
	hasScan bool
}

func newDevice(h backend.DeviceHandle) (*Device, error) {
	d := &Device{h: h, byCh: map[string]*Channel{}}
	// If a previous run left the buffer enabled (segfault, SIGKILL, etc.) the
	// kernel will refuse subsequent writes to scale / oversampling / sampling
	// frequency attrs with EBUSY. Force-disable it before we configure.
	// Ignored if the device doesn't expose buffer/enable.
	_ = h.WriteAttr(backend.AttrLocator{Name: "buffer/enable"}, "0")
	attrs, err := h.ListAttrs()
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("iio: list attrs: %w", err)
	}
	for _, a := range attrs {
		if a.Channel == "" {
			continue
		}
		ch := d.byCh[a.Channel]
		if ch == nil {
			ch = &Channel{dev: d, name: a.Channel}
			d.byCh[a.Channel] = ch
			d.chs = append(d.chs, ch)
		}
		switch {
		case a.Name == "raw":
			ch.hasRaw = true
		case a.Name == "input":
			ch.hasIn = true
		case a.Name == "scale":
			if v, ok := readFloat(h, a); ok {
				ch.scale = v
				ch.hasSc = true
			}
		case a.Name == "offset":
			if v, ok := readFloat(h, a); ok {
				ch.offset = v
				ch.hasOff = true
			}
		case strings.HasPrefix(a.Name, "scan_elements/"):
			suffix := strings.TrimPrefix(a.Name, "scan_elements/")
			switch suffix {
			case "type":
				s, err := h.ReadAttr(a)
				if err == nil {
					sc, perr := ParseTypeString(s)
					if perr == nil {
						sc.Name = a.Channel
						sc.Index = ch.scan.Index // preserve if already read
						ch.scan = sc
						ch.hasScan = true
					}
				}
			case "index":
				if s, err := h.ReadAttr(a); err == nil {
					if i, perr := strconv.Atoi(s); perr == nil {
						ch.scan.Index = i
					}
				}
			}
		}
	}
	// Some drivers (notably the ICM-20948) mark IIO_CHAN_INFO_SCALE /
	// IIO_CHAN_INFO_OFFSET as info_mask_shared_by_type, so the sysfs file is
	// in_<type>_scale (e.g. in_accel_scale) — one file shared across all axes
	// of that channel type. Inherit the type-level scale/offset onto each
	// per-axis channel that doesn't carry its own, so buffered decode applies
	// the right factor.
	for _, ch := range d.chs {
		if ch.hasSc && ch.hasOff {
			continue
		}
		parent, ok := d.parentChannel(ch.name)
		if !ok {
			continue
		}
		if !ch.hasSc && parent.hasSc {
			ch.scale = parent.scale
			ch.hasSc = true
		}
		if !ch.hasOff && parent.hasOff {
			ch.offset = parent.offset
			ch.hasOff = true
		}
	}
	// Bind scale/offset into each channel's scan element so buffered decode
	// picks them up. readAttr ordering isn't guaranteed, and the inheritance
	// above may have just populated them.
	for _, ch := range d.chs {
		if ch.hasScan && ch.hasSc {
			ch.scan.Scale = ch.scale
		}
		if ch.hasScan && ch.hasOff {
			ch.scan.Offset = ch.offset
		}
	}
	return d, nil
}

// parentChannel returns the type-level parent of a modified channel name —
// e.g. "accel_x" → "accel", "anglvel_y" → "anglvel". Returns ok=false if
// `name` has no trailing _<modifier> or the parent channel is absent.
func (d *Device) parentChannel(name string) (*Channel, bool) {
	i := strings.LastIndexByte(name, '_')
	if i <= 0 {
		return nil, false
	}
	parent, ok := d.byCh[name[:i]]
	return parent, ok
}

// ReloadScale re-reads in_<channel>_scale and in_<channel>_offset for every
// known channel and rebinds the new values into the scan-element metadata
// used by buffered decode. Call this after writing scale/offset/oversampling/
// sampling-frequency attributes that the kernel cross-couples (e.g. ICM-20948
// scale writes change the chip's full-scale register and rescale every
// subsequent raw sample). Without this, buffered samples would be decoded
// with stale scale and report incorrect SI values.
func (d *Device) ReloadScale() error {
	for _, ch := range d.chs {
		if v, ok := readFloat(d.h, backend.AttrLocator{Channel: ch.name, Name: "scale"}); ok {
			ch.scale = v
			ch.hasSc = true
		}
		if v, ok := readFloat(d.h, backend.AttrLocator{Channel: ch.name, Name: "offset"}); ok {
			ch.offset = v
			ch.hasOff = true
		}
	}
	// Re-run the per-axis inheritance from the type-level parent (e.g.
	// "accel_x" inherits from "accel"), since the parent's scale/offset may
	// have moved without the child's per-axis sysfs file changing.
	for _, ch := range d.chs {
		parent, ok := d.parentChannel(ch.name)
		if !ok {
			continue
		}
		if parent.hasSc {
			ch.scale = parent.scale
			ch.hasSc = true
		}
		if parent.hasOff {
			ch.offset = parent.offset
			ch.hasOff = true
		}
	}
	for _, ch := range d.chs {
		if ch.hasScan && ch.hasSc {
			ch.scan.Scale = ch.scale
		}
		if ch.hasScan && ch.hasOff {
			ch.scan.Offset = ch.offset
		}
	}
	return nil
}

// Name returns the kernel-reported device name (e.g. "bmp280").
func (d *Device) Name() string { return d.h.Name() }

// Channels returns all channels discovered on Open, in stable order.
func (d *Device) Channels() []*Channel { return d.chs }

// Channel returns the channel with the given name (e.g. "temp", "pressure",
// "accel_x"). ok is false if no such channel exists.
func (d *Device) Channel(name string) (*Channel, bool) {
	ch, ok := d.byCh[name]
	return ch, ok
}

// Close releases the underlying handle.
func (d *Device) Close() error { return d.h.Close() }

// Attr reads a device-level attribute (e.g. "current_timestamp_clock",
// "buffer/enable", "trigger/current_trigger").
func (d *Device) Attr(name string) (string, error) {
	return d.h.ReadAttr(backend.AttrLocator{Name: name})
}

// SetAttr writes a device-level attribute.
func (d *Device) SetAttr(name, value string) error {
	return d.h.WriteAttr(backend.AttrLocator{Name: name}, value)
}

// ChannelAttr reads a channel attribute by suffix (e.g. "raw", "scale",
// "oversampling_ratio").
func (d *Device) ChannelAttr(channel, attr string) (string, error) {
	return d.h.ReadAttr(backend.AttrLocator{Channel: channel, Name: attr})
}

// SetChannelAttr writes a channel attribute.
func (d *Device) SetChannelAttr(channel, attr, value string) error {
	return d.h.WriteAttr(backend.AttrLocator{Channel: channel, Name: attr}, value)
}

// ReadFloat returns the current value of a channel in its natural SI unit.
//
// Preference order:
//  1. If in_<channel>_input exists, read it directly (kernel does the math).
//     For some channels (notably BMP280 temp) the kernel reports m°C rather
//     than °C; ReadFloat applies the conventional divisor (see unit table).
//  2. Otherwise, read in_<channel>_raw and apply scale and offset.
func (d *Device) ReadFloat(channel string) (float64, error) {
	ch, ok := d.byCh[channel]
	if !ok {
		return 0, fmt.Errorf("iio: unknown channel %q", channel)
	}
	if ch.hasIn {
		s, err := d.h.ReadAttr(backend.AttrLocator{Channel: channel, Name: "input"})
		if err != nil {
			return 0, err
		}
		v, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return 0, fmt.Errorf("iio: parse %s_input %q: %w", channel, s, perr)
		}
		return convertInput(channel, v), nil
	}
	if !ch.hasRaw {
		return 0, fmt.Errorf("iio: channel %q has neither _input nor _raw", channel)
	}
	s, err := d.h.ReadAttr(backend.AttrLocator{Channel: channel, Name: "raw"})
	if err != nil {
		return 0, err
	}
	raw, perr := strconv.ParseFloat(s, 64)
	if perr != nil {
		return 0, fmt.Errorf("iio: parse %s_raw %q: %w", channel, s, perr)
	}
	// IIO ABI: value = (raw + offset) × scale (offset before scale).
	out := raw
	if ch.hasOff {
		out += ch.offset
	}
	if ch.hasSc {
		out *= ch.scale
	}
	return out, nil
}

// convertInput normalizes channels whose in_*_input is reported by the kernel
// in non-SI units. The IIO ABI does not enforce a single convention here —
// the kernel docs note that temperature `_input` is conventionally in
// millidegrees Celsius — so we divide. Pressure is already kPa on BMP280.
func convertInput(channel string, v float64) float64 {
	switch channel {
	case "temp":
		return v / 1000.0
	default:
		return v
	}
}

// Name returns the channel name (e.g. "temp").
func (c *Channel) Name() string { return c.name }

// Scale returns the in_<channel>_scale value, or 0 if absent.
func (c *Channel) Scale() float64 { return c.scale }

// Offset returns the in_<channel>_offset value, or 0 if absent.
func (c *Channel) Offset() float64 { return c.offset }

// HasInput reports whether in_<channel>_input is exposed (preferred over raw).
func (c *Channel) HasInput() bool { return c.hasIn }

// HasRaw reports whether in_<channel>_raw is exposed.
func (c *Channel) HasRaw() bool { return c.hasRaw }

// Scan returns the channel's parsed scan_elements metadata. The second return
// value is false if the channel has no scan_elements entry (i.e. it can't be
// used in a buffered capture).
func (c *Channel) Scan() (ScanChannel, bool) {
	if !c.hasScan {
		return ScanChannel{}, false
	}
	return c.scan, true
}

func readFloat(h backend.DeviceHandle, a backend.AttrLocator) (float64, bool) {
	s, err := h.ReadAttr(a)
	if err != nil {
		return 0, false
	}
	v, perr := strconv.ParseFloat(s, 64)
	if perr != nil {
		return 0, false
	}
	return v, true
}
