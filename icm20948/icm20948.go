// Package icm20948 is a thin convenience wrapper over the generic iio package
// for the InvenSense / TDK ICM-20948 9-axis IMU. It assumes the kernel
// "icm20948" driver from github.com/westphae/icm20948-mod (or any compatible
// driver that exports accel_x/y/z, anglvel_x/y/z, magn_x/y/z and temp channels
// with the standard IIO naming).
//
// The kernel driver returns accel in m/s², anglvel in rad/s, magn in Gauss,
// and temp in m°C; this package converts magn to µT and returns temp in °C so
// callers see consistent SI units across all axes.
package icm20948

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/westphae/go-iio"
)

// Channel names exposed by the kernel driver.
const (
	chanAccelX = "accel_x"
	chanAccelY = "accel_y"
	chanAccelZ = "accel_z"
	chanGyroX  = "anglvel_x"
	chanGyroY  = "anglvel_y"
	chanGyroZ  = "anglvel_z"
	chanMagX   = "magn_x"
	chanMagY   = "magn_y"
	chanMagZ   = "magn_z"
	chanTemp   = "temp"
)

// gaussToMicroTesla converts the kernel's Gauss/LSB scaled magnetometer
// readings to µT (1 G = 100 µT).
const gaussToMicroTesla = 100.0

// Sample is one decoded reading.
type Sample struct {
	Time   time.Time
	AccelX float64 // m/s²
	AccelY float64
	AccelZ float64
	GyroX  float64 // rad/s
	GyroY  float64
	GyroZ  float64
	MagX   float64 // µT
	MagY   float64
	MagZ   float64
	TempC  float64 // °C
}

type config struct {
	path         string
	accelScaleG  int // 2/4/8/16
	gyroScaleDps int // 250/500/1000/2000
}

// Option configures Open.
type Option func(*config)

// WithPath opens a specific IIO device path instead of looking up by name.
func WithPath(p string) Option { return func(c *config) { c.path = p } }

// WithAccelScale writes in_accel_scale to select an accelerometer full-scale
// range. Valid values: 2, 4, 8, 16 (G). The kernel converts these to the
// exact INT_PLUS_NANO string in its scale lookup table.
func WithAccelScale(g int) Option { return func(c *config) { c.accelScaleG = g } }

// WithGyroScale writes in_anglvel_scale to select a gyroscope full-scale
// range. Valid values: 250, 500, 1000, 2000 (dps).
func WithGyroScale(dps int) Option { return func(c *config) { c.gyroScaleDps = dps } }

// ICM20948 is an opened sensor.
type ICM20948 struct {
	dev *iio.Device
	cfg config
}

// accelScaleStrings maps the user-facing full-scale range (in G) to the exact
// SI string the kernel driver expects in in_accel_scale. Values come straight
// from icm20948_accel_scale_lookup in the kernel driver source.
var accelScaleStrings = map[int]string{
	2:  "0.000598550",
	4:  "0.001197100",
	8:  "0.002394201",
	16: "0.004788403",
}

// gyroScaleStrings maps the user-facing full-scale range (in dps) to the exact
// SI string the kernel driver expects in in_anglvel_scale. Values come from
// icm20948_anglvel_scale_lookup in the kernel driver source.
var gyroScaleStrings = map[int]string{
	250:  "0.000133158",
	500:  "0.000266316",
	1000: "0.000532632",
	2000: "0.001065264",
}

// Open finds the ICM-20948 by kernel name (or path), applies any options, and
// returns a ready-to-read handle. Close it when done.
func Open(opts ...Option) (*ICM20948, error) {
	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}

	var dev *iio.Device
	var err error
	if cfg.path != "" {
		dev, err = iio.OpenPath(cfg.path)
	} else {
		dev, err = iio.Open("icm20948")
	}
	if err != nil {
		return nil, fmt.Errorf("icm20948: %w", err)
	}

	if cfg.accelScaleG != 0 {
		s, ok := accelScaleStrings[cfg.accelScaleG]
		if !ok {
			dev.Close()
			return nil, fmt.Errorf("icm20948: unsupported accel scale %d G (want 2/4/8/16)", cfg.accelScaleG)
		}
		if err := dev.SetChannelAttr("accel", "scale", s); err != nil {
			dev.Close()
			return nil, fmt.Errorf("icm20948: set accel scale: %w", err)
		}
	}
	if cfg.gyroScaleDps != 0 {
		s, ok := gyroScaleStrings[cfg.gyroScaleDps]
		if !ok {
			dev.Close()
			return nil, fmt.Errorf("icm20948: unsupported gyro scale %d dps (want 250/500/1000/2000)", cfg.gyroScaleDps)
		}
		if err := dev.SetChannelAttr("anglvel", "scale", s); err != nil {
			dev.Close()
			return nil, fmt.Errorf("icm20948: set anglvel scale: %w", err)
		}
	}

	// iio.Open caches the kernel-default scale into each channel's buffered
	// decode metadata; the writes above changed the chip register and the
	// kernel's reported in_*_scale, but the cached values are now stale.
	// Re-bind so buffered Stream samples decode with the right factor.
	if err := dev.ReloadScale(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("icm20948: reload scale: %w", err)
	}

	return &ICM20948{dev: dev, cfg: cfg}, nil
}

// Device returns the underlying *iio.Device for callers that need raw attr
// access or buffered captures with custom options.
func (i *ICM20948) Device() *iio.Device { return i.dev }

// Close releases the device.
func (i *ICM20948) Close() error { return i.dev.Close() }

// Overrange returns the kernel's sticky AK09916 magnetometer overflow flag
// (in_magn_overrange). The icm20948-mod kernel driver latches ST2.HOFL on
// every buffered sample where any mag axis saturated the chip's ±4912 µT
// range; the flag stays set until cleared via ClearOverrange. The IIO scan
// elements still carry the (clipped) µT values regardless — Overrange is
// the only handle into whether the chip itself flagged saturation.
func (i *ICM20948) Overrange() (bool, error) {
	s, err := i.dev.Attr("in_magn_overrange")
	if err != nil {
		return false, fmt.Errorf("icm20948: read in_magn_overrange: %w", err)
	}
	return strings.TrimSpace(s) == "1", nil
}

// ClearOverrange clears the sticky overflow flag by writing 0 to
// in_magn_overrange. Subsequent reads return false until the next HOFL
// event re-latches it. Idempotent — clearing an already-clear flag is a
// no-op as far as the chip is concerned, but the sysfs write still costs
// a syscall; prefer test-and-clear over unconditional clearing.
func (i *ICM20948) ClearOverrange() error {
	if err := i.dev.SetAttr("in_magn_overrange", "0"); err != nil {
		return fmt.Errorf("icm20948: clear in_magn_overrange: %w", err)
	}
	return nil
}

// Read returns a single polled sample. Each call performs ten sysfs reads
// (accel/gyro/mag axes plus temp). Intended for low-rate use; for streaming
// at hundreds of Hz, use Stream.
func (i *ICM20948) Read() (Sample, error) {
	var s Sample
	pairs := []struct {
		name string
		dst  *float64
	}{
		{chanAccelX, &s.AccelX}, {chanAccelY, &s.AccelY}, {chanAccelZ, &s.AccelZ},
		{chanGyroX, &s.GyroX}, {chanGyroY, &s.GyroY}, {chanGyroZ, &s.GyroZ},
		{chanMagX, &s.MagX}, {chanMagY, &s.MagY}, {chanMagZ, &s.MagZ},
		{chanTemp, &s.TempC},
	}
	for _, p := range pairs {
		v, err := i.dev.ReadFloat(p.name)
		if err != nil {
			return Sample{}, fmt.Errorf("icm20948: read %s: %w", p.name, err)
		}
		*p.dst = v
	}
	s.MagX *= gaussToMicroTesla
	s.MagY *= gaussToMicroTesla
	s.MagZ *= gaussToMicroTesla
	s.Time = time.Now()
	return s, nil
}

// StreamOptions configures Stream.
type StreamOptions struct {
	// FrequencyHz drives the hrtimer trigger that paces the buffered
	// capture. Required (>0). The library creates the trigger via configfs
	// — this needs CAP_SYS_ADMIN (root) unless an out-of-band trigger
	// already exists.
	FrequencyHz int

	// TriggerName names the hrtimer trigger created in configfs. Defaults
	// to "goiio-icm20948" if zero.
	TriggerName string

	// BufferLength is the kernel ring depth in samples (defaults to 16).
	BufferLength int

	// ChannelBuffer is the size of the Go channel returned by Stream
	// (defaults to BufferLength).
	ChannelBuffer int
}

// Stream starts a background goroutine that captures samples at the given
// frequency and pushes them onto the returned channel. Cancel ctx (or call
// Close on the ICM20948) to stop. The returned channel is closed when the
// stream ends.
//
// Note: the AK09916 magnetometer inside the ICM-20948 runs at a fixed 100 Hz
// in the kernel driver, regardless of FrequencyHz. At higher trigger rates,
// consecutive samples may carry identical magn values until the next mag
// conversion completes.
func (i *ICM20948) Stream(ctx context.Context, opts StreamOptions) (<-chan Sample, error) {
	if opts.FrequencyHz <= 0 {
		return nil, fmt.Errorf("icm20948: Stream: FrequencyHz must be > 0")
	}
	if opts.TriggerName == "" {
		opts.TriggerName = "goiio-icm20948"
	}
	if opts.BufferLength <= 0 {
		opts.BufferLength = 16
	}
	if opts.ChannelBuffer <= 0 {
		opts.ChannelBuffer = opts.BufferLength
	}

	trig, err := iio.EnsureHRTimer(opts.TriggerName, opts.FrequencyHz)
	if err != nil {
		return nil, fmt.Errorf("icm20948: %w", err)
	}

	buf, err := i.dev.Buffer(iio.BufferOptions{
		Channels: []string{
			chanAccelX, chanAccelY, chanAccelZ,
			chanGyroX, chanGyroY, chanGyroZ,
			chanMagX, chanMagY, chanMagZ,
			chanTemp, "timestamp",
		},
		Length:  opts.BufferLength,
		Trigger: trig.Name(),
	})
	if err != nil {
		trig.Remove()
		return nil, fmt.Errorf("icm20948: %w", err)
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
					log.Printf("icm20948: stream stopped: %s", err)
				}
				return
			}
			for k := 0; k < n; k++ {
				r := recs[k]
				s := Sample{
					Time:   r.Time,
					AccelX: r.Values[chanAccelX],
					AccelY: r.Values[chanAccelY],
					AccelZ: r.Values[chanAccelZ],
					GyroX:  r.Values[chanGyroX],
					GyroY:  r.Values[chanGyroY],
					GyroZ:  r.Values[chanGyroZ],
					MagX:   r.Values[chanMagX] * gaussToMicroTesla,
					MagY:   r.Values[chanMagY] * gaussToMicroTesla,
					MagZ:   r.Values[chanMagZ] * gaussToMicroTesla,
					TempC:  r.Values[chanTemp],
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
