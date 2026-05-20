// Package mmc5983ma is a thin convenience wrapper over the generic iio
// package for the MEMSIC MMC5983MA 3-axis magnetometer. It assumes the
// kernel "mmc5983ma" driver from github.com/westphae/mmc5983ma-mod (or any
// compatible driver that exports magn_x/y/z and temp channels with the
// standard IIO naming).
//
// The kernel driver returns magn in Gauss and temp in m°C; this package
// converts magn to µT and surfaces temp in °C so callers see consistent SI
// units. Calibration-bias compensation (the chip itself has no offset
// registers — the kernel maintains a software shadow) is applied inside the
// driver before exposure, so values returned by Read and Stream are already
// offset-corrected.
package mmc5983ma

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/westphae/go-iio"
)

// Channel names exposed by the kernel driver.
const (
	chanMagX = "magn_x"
	chanMagY = "magn_y"
	chanMagZ = "magn_z"
	chanTemp = "temp"
)

// gaussToMicroTesla converts the kernel's Gauss/LSB scaled magnetometer
// readings to µT (1 G = 100 µT).
const gaussToMicroTesla = 100.0

// Sample is one decoded reading.
type Sample struct {
	Time  time.Time
	MagX  float64 // µT
	MagY  float64 // µT
	MagZ  float64 // µT
	TempC float64 // °C
}

type config struct {
	path        string
	sampHz      int // 1/10/20/50/100/200/1000 (0 = leave kernel default)
	bandwidthHz int // 100/200/400/800       (0 = leave kernel default)
	periodicSet int // 0/1/25/75/100/250/500/1000/2000 (-1 sentinel = unset)
}

// Option configures Open.
type Option func(*config)

// WithPath opens a specific IIO device path instead of looking up by name.
func WithPath(p string) Option { return func(c *config) { c.path = p } }

// WithSamplingFrequencyHz writes in_magn_sampling_frequency to set the
// continuous-mode output data rate. Valid values: 1, 10, 20, 50, 100, 200,
// 1000 (Hz). The driver auto-bumps the measurement bandwidth when the
// requested ODR demands it (200 → BW≥200, 1000 → BW=800).
func WithSamplingFrequencyHz(hz int) Option { return func(c *config) { c.sampHz = hz } }

// WithBandwidthHz writes in_magn_filter_low_pass_3db_frequency to set the
// per-measurement bandwidth (decimation-filter length). Valid values:
// 100, 200, 400, 800 (Hz). Higher BW means a shorter measurement window —
// less time-domain averaging, more noise, faster max ODR.
func WithBandwidthHz(hz int) Option { return func(c *config) { c.bandwidthHz = hz } }

// WithPeriodicSet writes in_magn_calibration_period_samples to control the
// chip's periodic-SET feature. Valid values: 0 (disabled), 1, 25, 75, 100,
// 250, 500, 1000, 2000 (samples between auto-SET pulses). The Auto_SR
// feature is always on in the kernel driver, so periodic SET is a
// supplementary thermal-recalibration knob — leave at 0 unless you have a
// reason to bias the bridge polarity at a fixed cadence.
func WithPeriodicSet(samples int) Option {
	return func(c *config) {
		// Use a sentinel offset so that "0" (a valid request to disable)
		// is distinguishable from "unset" — we store the user's value
		// directly and treat -1 as "unset" elsewhere.
		c.periodicSet = samples + 1
	}
}

// MMC5983MA is an opened sensor.
type MMC5983MA struct {
	dev *iio.Device
	cfg config
}

// Open finds the MMC5983MA by kernel name (or path), applies any options,
// and returns a ready-to-read handle. Close it when done.
func Open(opts ...Option) (*MMC5983MA, error) {
	cfg := config{periodicSet: 0} // 0 means "unset" given the +1 shift in WithPeriodicSet
	for _, o := range opts {
		o(&cfg)
	}

	var dev *iio.Device
	var err error
	if cfg.path != "" {
		dev, err = iio.OpenPath(cfg.path)
	} else {
		dev, err = iio.Open("mmc5983ma")
	}
	if err != nil {
		return nil, fmt.Errorf("mmc5983ma: %w", err)
	}

	if cfg.sampHz > 0 {
		if err := setNearestChannelAttrInt(dev, "magn", "sampling_frequency", cfg.sampHz); err != nil {
			dev.Close()
			return nil, err
		}
	}
	if cfg.bandwidthHz > 0 {
		if err := setNearestChannelAttrInt(dev, "magn", "filter_low_pass_3db_frequency", cfg.bandwidthHz); err != nil {
			dev.Close()
			return nil, err
		}
	}
	if cfg.periodicSet > 0 {
		// Stored as user_value + 1; subtract to get back the request.
		req := cfg.periodicSet - 1
		if err := setNearestDeviceAttrInt(dev, "in_magn_calibration_period_samples", req); err != nil {
			dev.Close()
			return nil, err
		}
	}

	// iio.Open caches kernel-default scale into each channel's buffered
	// decode metadata; in_magn_scale is fixed on this chip (1/16384 G/LSB)
	// but ReloadScale is cheap and keeps the pattern aligned with the
	// icm20948 wrapper for future-proofing.
	if err := dev.ReloadScale(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("mmc5983ma: reload scale: %w", err)
	}

	return &MMC5983MA{dev: dev, cfg: cfg}, nil
}

// setNearestChannelAttrInt reads <channel>_<attr>_available, picks the
// integer value nearest hz, and writes it back. The kernel driver enforces
// exact membership in the _available list, so an exact string match
// matters — snapping protects callers from typos like "98" when only "100"
// is offered.
func setNearestChannelAttrInt(dev *iio.Device, channel, attr string, want int) error {
	avail, err := dev.ChannelAttr(channel, attr+"_available")
	if err != nil {
		return fmt.Errorf("mmc5983ma: read %s %s available: %w", channel, attr, err)
	}
	best, err := nearestInt(avail, want)
	if err != nil {
		return fmt.Errorf("mmc5983ma: parse %s %s available %q: %w", channel, attr, avail, err)
	}
	if err := dev.SetChannelAttr(channel, attr, best); err != nil {
		return fmt.Errorf("mmc5983ma: set %s %s %s: %w", channel, attr, best, err)
	}
	return nil
}

// setNearestDeviceAttrInt is the device-level analogue used for custom
// attrs (in_magn_calibration_period_samples lives at the device root, not
// under a channel, because the kernel registers it via IIO_DEVICE_ATTR).
func setNearestDeviceAttrInt(dev *iio.Device, attrName string, want int) error {
	avail, err := dev.Attr(attrName + "_available")
	if err != nil {
		return fmt.Errorf("mmc5983ma: read %s_available: %w", attrName, err)
	}
	best, err := nearestInt(avail, want)
	if err != nil {
		return fmt.Errorf("mmc5983ma: parse %s_available %q: %w", attrName, avail, err)
	}
	if err := dev.SetAttr(attrName, best); err != nil {
		return fmt.Errorf("mmc5983ma: set %s %s: %w", attrName, best, err)
	}
	return nil
}

// nearestInt picks the integer-valued option from a whitespace-separated
// list nearest the requested value. Returns the chosen string verbatim so
// it can be written back without losing whatever formatting the kernel
// expects.
func nearestInt(avail string, want int) (string, error) {
	fields := strings.Fields(avail)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty available list")
	}
	var bestStr string
	bestDiff := math.MaxInt
	for _, f := range fields {
		v, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		d := v - want
		if d < 0 {
			d = -d
		}
		if d < bestDiff {
			bestDiff = d
			bestStr = f
		}
	}
	if bestStr == "" {
		return "", fmt.Errorf("no parseable integer in %q", avail)
	}
	return bestStr, nil
}

// Device returns the underlying *iio.Device for callers that need raw attr
// access or buffered captures with custom options.
func (m *MMC5983MA) Device() *iio.Device { return m.dev }

// Close releases the device.
func (m *MMC5983MA) Close() error { return m.dev.Close() }

// Read returns a single polled sample. Each call performs four sysfs reads
// (mag X/Y/Z + temp). Intended for low-rate use; for streaming at hundreds
// of Hz, use Stream.
func (m *MMC5983MA) Read() (Sample, error) {
	var s Sample
	pairs := []struct {
		name string
		dst  *float64
	}{
		{chanMagX, &s.MagX}, {chanMagY, &s.MagY}, {chanMagZ, &s.MagZ},
		{chanTemp, &s.TempC},
	}
	for _, p := range pairs {
		v, err := m.dev.ReadFloat(p.name)
		if err != nil {
			return Sample{}, fmt.Errorf("mmc5983ma: read %s: %w", p.name, err)
		}
		*p.dst = v
	}
	s.MagX *= gaussToMicroTesla
	s.MagY *= gaussToMicroTesla
	s.MagZ *= gaussToMicroTesla
	s.Time = time.Now()
	return s, nil
}

// --- chip-specific helpers ------------------------------------------------

// SetPulse fires a SET pulse through the on-chip coils (~500 ns of high
// current), forcing the AMR bridge into the SET polarity. This is the
// recovery step after exposure to a field strong enough to flip the
// magnetic domains (~10 G); subsequent measurements report sensor + offset.
// Cheap (writes a single sysfs byte); the kernel handles the 1 ms settling
// wait inline.
func (m *MMC5983MA) SetPulse() error {
	if err := m.dev.SetAttr("degauss", "1"); err != nil {
		return fmt.Errorf("mmc5983ma: degauss SET: %w", err)
	}
	return nil
}

// ResetPulse fires a RESET pulse — same mechanism as SetPulse but in the
// opposite polarity. Used as the second half of a manual offset-removal
// cycle if you'd rather drive the measurements yourself; for the canned
// auto-null, use AutoNullCalibBias.
func (m *MMC5983MA) ResetPulse() error {
	if err := m.dev.SetAttr("degauss", "2"); err != nil {
		return fmt.Errorf("mmc5983ma: degauss RESET: %w", err)
	}
	return nil
}

// AutoNullCalibBias runs the datasheet's offset-removal recipe: SET pulse
// → one measurement → RESET pulse → one measurement → store
// (set+reset)/2 as the chip's bias on each axis. After this call the
// driver's software calibbias holds the residual bridge offset; subsequent
// raw reads return the actual field with offset subtracted. Continuous
// mode is briefly disabled for the duration (sub-30 ms).
//
// Note: only meaningful in a stable field (typically near-zero, e.g.
// inside a Helmholtz coil or shielded enclosure). In an arbitrary
// environment this still cancels the bridge offset but the measurements
// taken during the cycle will themselves carry the ambient field.
func (m *MMC5983MA) AutoNullCalibBias() error {
	if err := m.dev.SetAttr("degauss", "3"); err != nil {
		return fmt.Errorf("mmc5983ma: degauss auto-null: %w", err)
	}
	return nil
}

// CalibBias returns the driver's software calibration offsets for each
// axis (in raw 18-bit signed LSB), as the kernel last set or accepted them.
// These are subtracted from every raw measurement before being exposed via
// sysfs or the buffered scan.
func (m *MMC5983MA) CalibBias() (x, y, z int, err error) {
	x, err = m.readChanInt("magn_x", "calibbias")
	if err != nil {
		return
	}
	y, err = m.readChanInt("magn_y", "calibbias")
	if err != nil {
		return
	}
	z, err = m.readChanInt("magn_z", "calibbias")
	return
}

// SetCalibBias writes per-axis software offsets (in raw 18-bit signed LSB).
// Persistence is for the life of the bind — values are lost on rmmod or
// unbind.
func (m *MMC5983MA) SetCalibBias(x, y, z int) error {
	if err := m.dev.SetChannelAttr("magn_x", "calibbias", strconv.Itoa(x)); err != nil {
		return fmt.Errorf("mmc5983ma: set magn_x calibbias: %w", err)
	}
	if err := m.dev.SetChannelAttr("magn_y", "calibbias", strconv.Itoa(y)); err != nil {
		return fmt.Errorf("mmc5983ma: set magn_y calibbias: %w", err)
	}
	if err := m.dev.SetChannelAttr("magn_z", "calibbias", strconv.Itoa(z)); err != nil {
		return fmt.Errorf("mmc5983ma: set magn_z calibbias: %w", err)
	}
	return nil
}

// RunSelftest exercises the chip's built-in test coil to confirm the AMR
// bridge is responsive. Writing positive=true selects St_enp (current
// through the coil in the positive direction); false selects St_enm. The
// kernel driver runs a baseline measurement, enables the coil, takes a
// second measurement, computes the delta, and disables the coil — all
// inside the sysfs write. Read the result with SelftestDelta.
//
// A working chip shows a strong response (thousands of LSB) on at least
// one axis; a wedged or unmagnetized bridge shows near-zero delta.
func (m *MMC5983MA) RunSelftest(positive bool) error {
	v := "2"
	if positive {
		v = "1"
	}
	if err := m.dev.SetAttr("in_magn_test", v); err != nil {
		return fmt.Errorf("mmc5983ma: run selftest: %w", err)
	}
	return nil
}

// ClearSelftest writes 0 to in_magn_test, clearing both the test-coil
// enable and the cached delta vector.
func (m *MMC5983MA) ClearSelftest() error {
	if err := m.dev.SetAttr("in_magn_test", "0"); err != nil {
		return fmt.Errorf("mmc5983ma: clear selftest: %w", err)
	}
	return nil
}

// SelftestDelta returns the (X, Y, Z) delta from the most recent
// RunSelftest call, in raw 18-bit signed LSB. Returns (0, 0, 0) if no
// self-test has been run since the last ClearSelftest or since module
// load.
func (m *MMC5983MA) SelftestDelta() (x, y, z int, err error) {
	s, err := m.dev.Attr("in_magn_test")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("mmc5983ma: read selftest: %w", err)
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d %d %d", &x, &y, &z); err != nil {
		return 0, 0, 0, fmt.Errorf("mmc5983ma: parse selftest %q: %w", s, err)
	}
	return x, y, z, nil
}

func (m *MMC5983MA) readChanInt(channel, attr string) (int, error) {
	s, err := m.dev.ChannelAttr(channel, attr)
	if err != nil {
		return 0, fmt.Errorf("mmc5983ma: read %s %s: %w", channel, attr, err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("mmc5983ma: parse %s %s %q: %w", channel, attr, s, err)
	}
	return v, nil
}

// --- streaming ------------------------------------------------------------

// StreamOptions configures Stream.
type StreamOptions struct {
	// FrequencyHz drives the hrtimer trigger that paces the buffered
	// capture. Required (>0). The library creates the trigger via
	// configfs — this needs CAP_SYS_ADMIN (root) unless an out-of-band
	// trigger already exists.
	FrequencyHz int

	// TriggerName names the hrtimer trigger created in configfs. Defaults
	// to "goiio-mmc5983ma" if zero.
	TriggerName string

	// BufferLength is the kernel ring depth in samples (defaults to 16).
	BufferLength int

	// ChannelBuffer is the size of the Go channel returned by Stream
	// (defaults to BufferLength).
	ChannelBuffer int
}

// Stream starts a background goroutine that captures samples at the given
// frequency and pushes them onto the returned channel. Cancel ctx (or
// call Close on the MMC5983MA) to stop. The returned channel is closed
// when the stream ends.
//
// The MMC5983MA driver runs the chip in continuous mode at its own ODR
// (default 100 Hz, settable via WithSamplingFrequencyHz). At trigger rates
// above the chip's ODR consecutive samples will repeat the latest mag
// values until the next chip conversion lands; at rates below the ODR
// samples are dropped between triggers.
//
// Temperature is not included in the buffered scan layout (the chip can
// only do mag OR temp at a time, so a buffered temp would have to pause
// mag — not worth it). Sample.TempC is zero on every buffered sample;
// poll Read for temperature.
func (m *MMC5983MA) Stream(ctx context.Context, opts StreamOptions) (<-chan Sample, error) {
	if opts.FrequencyHz <= 0 {
		return nil, fmt.Errorf("mmc5983ma: Stream: FrequencyHz must be > 0")
	}
	if opts.TriggerName == "" {
		opts.TriggerName = "goiio-mmc5983ma"
	}
	if opts.BufferLength <= 0 {
		opts.BufferLength = 16
	}
	if opts.ChannelBuffer <= 0 {
		opts.ChannelBuffer = opts.BufferLength
	}

	trig, err := iio.EnsureHRTimer(opts.TriggerName, opts.FrequencyHz)
	if err != nil {
		return nil, fmt.Errorf("mmc5983ma: %w", err)
	}

	buf, err := m.dev.Buffer(iio.BufferOptions{
		Channels: []string{chanMagX, chanMagY, chanMagZ, "timestamp"},
		Length:   opts.BufferLength,
		Trigger:  trig.Name(),
	})
	if err != nil {
		trig.Remove()
		return nil, fmt.Errorf("mmc5983ma: %w", err)
	}

	out := make(chan Sample, opts.ChannelBuffer)
	go func() {
		defer close(out)
		defer trig.Remove()
		defer buf.Close()
		recs := make([]iio.Record, opts.BufferLength)
		for {
			n, err := buf.Read(ctx, recs)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("mmc5983ma: stream stopped: %s", err)
				}
				return
			}
			for k := 0; k < n; k++ {
				r := recs[k]
				s := Sample{
					Time: r.Time,
					MagX: r.Values[chanMagX] * gaussToMicroTesla,
					MagY: r.Values[chanMagY] * gaussToMicroTesla,
					MagZ: r.Values[chanMagZ] * gaussToMicroTesla,
				}
				select {
				case out <- s:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
