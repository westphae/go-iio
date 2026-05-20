// Package icm45686 is a thin convenience wrapper over the generic iio package
// for the InvenSense / TDK ICM-45686 6-axis IMU (3-axis accel + 3-axis gyro +
// temperature). It assumes the kernel "icm45686" driver from
// github.com/westphae/icm45686-mod (which vendors mainline
// drivers/iio/imu/inv_icm45600 verbatim), or any future Pi kernel that ships
// inv_icm45600 directly — both expose the standard IIO channel naming.
//
// Units: the kernel driver returns accel in m/s², anglvel in rad/s, and temp
// in m°C; this package returns temp in °C so callers see consistent SI units.
package icm45686

import (
	"context"
	"fmt"
	"log"
	"math"
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
	chanTemp   = "temp"
)

// Sample is one decoded reading.
type Sample struct {
	Time   time.Time
	AccelX float64 // m/s²
	AccelY float64
	AccelZ float64
	GyroX  float64 // rad/s
	GyroY  float64
	GyroZ  float64
	TempC  float64 // °C
}

type config struct {
	path         string
	accelScaleG  float64 // 2/4/8/16/32; 0 = leave kernel default
	gyroScaleDps float64 // 15.625/31.25/62.5/125/250/500/1000/2000/4000; 0 = default
}

// Option configures Open.
type Option func(*config)

// WithPath opens a specific IIO device path instead of looking up by name.
// Useful when multiple ICM-45686 chips share a Pi or when you need to bypass
// the Open-by-name probe entirely.
func WithPath(p string) Option { return func(c *config) { c.path = p } }

// WithAccelScale writes in_accel_scale to select an accelerometer full-scale
// range. Valid values: 2, 4, 8, 16, 32 (G). The 32 G option is specific to
// the ICM-45686/87/88P/89 variants — the smaller -456xx parts top out at 16 G.
func WithAccelScale(g float64) Option { return func(c *config) { c.accelScaleG = g } }

// WithGyroScale writes in_anglvel_scale to select a gyroscope full-scale
// range. Valid values (dps): 15.625, 31.25, 62.5, 125, 250, 500, 1000, 2000,
// 4000. The 4000 dps option is specific to ICM-45686/87/88P/89.
func WithGyroScale(dps float64) Option { return func(c *config) { c.gyroScaleDps = dps } }

// ICM45686 is an opened sensor.
type ICM45686 struct {
	dev *iio.Device
	cfg config
}

// accelScales lists every accel full-scale range the inv_icm45600 driver
// accepts, in the IIO_VAL_INT_PLUS_NANO string form the sysfs scale attribute
// needs. Values come straight from inv_icm45686_accel_scale in the kernel
// driver source (which we vendor in westphae/icm45686-mod). The 32 G entry is
// the ICM-45686-and-friends extended range; smaller -456xx parts reject it,
// but Open returns the kernel's error in that case so we don't need to encode
// per-variant tables here.
var accelScales = []struct {
	G     float64
	Scale string
}{
	{2, "0.000598550"},
	{4, "0.001197101"},
	{8, "0.002394202"},
	{16, "0.004788403"},
	{32, "0.009576806"},
}

// gyroScales likewise mirrors inv_icm45686_gyro_scale from the kernel driver.
var gyroScales = []struct {
	Dps   float64
	Scale string
}{
	{15.625, "0.000008322"},
	{31.25, "0.000016645"},
	{62.5, "0.000033290"},
	{125, "0.000066579"},
	{250, "0.000133158"},
	{500, "0.000266316"},
	{1000, "0.000532632"},
	{2000, "0.001065264"},
	{4000, "0.002130529"},
}

// Open finds the ICM-45686 by kernel name (or path), applies any options, and
// returns a ready-to-read handle. Close it when done.
func Open(opts ...Option) (*ICM45686, error) {
	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}

	var dev *iio.Device
	var err error
	if cfg.path != "" {
		dev, err = iio.OpenPath(cfg.path)
	} else {
		dev, err = iio.Open("icm45686")
	}
	if err != nil {
		return nil, fmt.Errorf("icm45686: %w", err)
	}

	if cfg.accelScaleG != 0 {
		s, ok := matchAccelScale(cfg.accelScaleG)
		if !ok {
			dev.Close()
			return nil, fmt.Errorf("icm45686: unsupported accel scale %g G (want 2/4/8/16/32)", cfg.accelScaleG)
		}
		if err := dev.SetChannelAttr("accel", "scale", s); err != nil {
			dev.Close()
			return nil, fmt.Errorf("icm45686: set accel scale: %w", err)
		}
	}
	if cfg.gyroScaleDps != 0 {
		s, ok := matchGyroScale(cfg.gyroScaleDps)
		if !ok {
			dev.Close()
			return nil, fmt.Errorf("icm45686: unsupported gyro scale %g dps "+
				"(want 15.625/31.25/62.5/125/250/500/1000/2000/4000)", cfg.gyroScaleDps)
		}
		if err := dev.SetChannelAttr("anglvel", "scale", s); err != nil {
			dev.Close()
			return nil, fmt.Errorf("icm45686: set anglvel scale: %w", err)
		}
	}

	// iio.Open caches the kernel-default scale into each channel's buffered
	// decode metadata; the writes above changed the chip register and the
	// kernel's reported in_*_scale, but the cached values are now stale.
	// Re-bind so buffered Stream samples decode with the right factor.
	if err := dev.ReloadScale(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("icm45686: reload scale: %w", err)
	}

	return &ICM45686{dev: dev, cfg: cfg}, nil
}

// matchAccelScale returns the scale string for an FS requested in G, allowing
// a small tolerance so callers can pass e.g. 32.0 or 32 interchangeably.
func matchAccelScale(g float64) (string, bool) {
	for _, e := range accelScales {
		if math.Abs(e.G-g) < 0.01 {
			return e.Scale, true
		}
	}
	return "", false
}

// matchGyroScale returns the scale string for a gyro FS requested in dps.
// Tolerance is tighter than accel because the small dps options (15.625,
// 31.25, 62.5) are only ~15 dps apart.
func matchGyroScale(dps float64) (string, bool) {
	for _, e := range gyroScales {
		if math.Abs(e.Dps-dps) < 0.01 {
			return e.Scale, true
		}
	}
	return "", false
}

// Device returns the underlying *iio.Device for callers that need raw attr
// access (e.g. sampling_frequency, calibbias) or buffered captures with
// custom options.
func (i *ICM45686) Device() *iio.Device { return i.dev }

// Close releases the device.
func (i *ICM45686) Close() error { return i.dev.Close() }

// Read returns a single polled sample. Each call performs seven sysfs reads
// (accel xyz, gyro xyz, temp). Intended for low-rate use; for streaming
// at hundreds of Hz, use Stream.
func (i *ICM45686) Read() (Sample, error) {
	var s Sample
	pairs := []struct {
		name string
		dst  *float64
	}{
		{chanAccelX, &s.AccelX}, {chanAccelY, &s.AccelY}, {chanAccelZ, &s.AccelZ},
		{chanGyroX, &s.GyroX}, {chanGyroY, &s.GyroY}, {chanGyroZ, &s.GyroZ},
		{chanTemp, &s.TempC},
	}
	for _, p := range pairs {
		v, err := i.dev.ReadFloat(p.name)
		if err != nil {
			return Sample{}, fmt.Errorf("icm45686: read %s: %w", p.name, err)
		}
		*p.dst = v
	}
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
	// to "goiio-icm45686" if zero.
	TriggerName string

	// BufferLength is the kernel ring depth in samples (defaults to 16).
	BufferLength int

	// ChannelBuffer is the size of the Go channel returned by Stream
	// (defaults to BufferLength).
	ChannelBuffer int
}

// Stream starts a background goroutine that captures samples at the given
// frequency and pushes them onto the returned channel. Cancel ctx (or call
// Close on the ICM45686) to stop. The returned channel is closed when the
// stream ends.
//
// If the chip's INT1 pin is wired and declared in DT, the inv_icm45600 driver
// also registers an in-kernel data-ready iio_trigger; you can pass its name
// via TriggerName to use that instead of an hrtimer (in which case the
// FrequencyHz field still has to be set but is ignored when the trigger
// already exists in configfs).
func (i *ICM45686) Stream(ctx context.Context, opts StreamOptions) (<-chan Sample, error) {
	if opts.FrequencyHz <= 0 {
		return nil, fmt.Errorf("icm45686: Stream: FrequencyHz must be > 0")
	}
	if opts.TriggerName == "" {
		opts.TriggerName = "goiio-icm45686"
	}
	if opts.BufferLength <= 0 {
		opts.BufferLength = 16
	}
	if opts.ChannelBuffer <= 0 {
		opts.ChannelBuffer = opts.BufferLength
	}

	trig, err := iio.EnsureHRTimer(opts.TriggerName, opts.FrequencyHz)
	if err != nil {
		return nil, fmt.Errorf("icm45686: %w", err)
	}

	buf, err := i.dev.Buffer(iio.BufferOptions{
		Channels: []string{
			chanAccelX, chanAccelY, chanAccelZ,
			chanGyroX, chanGyroY, chanGyroZ,
			chanTemp, "timestamp",
		},
		Length:  opts.BufferLength,
		Trigger: trig.Name(),
	})
	if err != nil {
		trig.Remove()
		return nil, fmt.Errorf("icm45686: %w", err)
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
					log.Printf("icm45686: stream stopped: %s", err)
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
