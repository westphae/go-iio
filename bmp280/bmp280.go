// Package bmp280 is a thin convenience wrapper over the generic iio package
// for the Bosch BMP280 pressure/temperature sensor (kernel driver
// "bmp280"). It does not import goflying; any goflying-style adapters that
// convert this package's Sample to sensors.BMPData live in the goflying repo.
package bmp280

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/westphae/go-iio"
)

// Sample is one decoded reading.
type Sample struct {
	Time     time.Time
	TempC    float64 // °C
	PressKPa float64 // kPa
}

// BMP280 is an opened sensor.
type BMP280 struct {
	dev *iio.Device
	cfg config
}

type config struct {
	path             string // explicit sysfs path; empty means look up by name
	oversampTemp     int
	oversampPressure int
}

// Option configures Open.
type Option func(*config)

// WithPath opens a specific IIO device path instead of looking up by name.
func WithPath(path string) Option { return func(c *config) { c.path = path } }

// WithOversampling writes in_temp_oversampling_ratio and
// in_pressure_oversampling_ratio. Valid values per the BMP280 datasheet:
// 1, 2, 4, 8, 16. The kernel reports the available set in
// in_<channel>_oversampling_ratio_available if present.
func WithOversampling(temp, pressure int) Option {
	return func(c *config) {
		c.oversampTemp = temp
		c.oversampPressure = pressure
	}
}

// Open finds the BMP280 by kernel name (or path), applies any options, and
// returns a ready-to-read handle. Close it when done.
func Open(opts ...Option) (*BMP280, error) {
	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}

	var dev *iio.Device
	var err error
	if cfg.path != "" {
		dev, err = iio.OpenPath(cfg.path)
	} else {
		dev, err = iio.Open("bmp280")
	}
	if err != nil {
		return nil, fmt.Errorf("bmp280: %w", err)
	}

	if cfg.oversampTemp > 0 {
		if err := dev.SetChannelAttr("temp", "oversampling_ratio", strconv.Itoa(cfg.oversampTemp)); err != nil {
			dev.Close()
			return nil, fmt.Errorf("bmp280: set temp oversampling: %w", err)
		}
	}
	if cfg.oversampPressure > 0 {
		if err := dev.SetChannelAttr("pressure", "oversampling_ratio", strconv.Itoa(cfg.oversampPressure)); err != nil {
			dev.Close()
			return nil, fmt.Errorf("bmp280: set pressure oversampling: %w", err)
		}
	}

	return &BMP280{dev: dev, cfg: cfg}, nil
}

// Device returns the underlying *iio.Device for callers that need raw attr
// access or buffered captures with custom options.
func (b *BMP280) Device() *iio.Device { return b.dev }

// Close releases the device.
func (b *BMP280) Close() error { return b.dev.Close() }

// Read returns a single polled sample. Each call performs two sysfs reads
// (in_temp_input and in_pressure_input).
func (b *BMP280) Read() (Sample, error) {
	t, err := b.dev.ReadFloat("temp")
	if err != nil {
		return Sample{}, err
	}
	p, err := b.dev.ReadFloat("pressure")
	if err != nil {
		return Sample{}, err
	}
	return Sample{Time: time.Now(), TempC: t, PressKPa: p}, nil
}

// StreamOptions configures Stream.
type StreamOptions struct {
	// FrequencyHz drives the hrtimer trigger that paces the buffered
	// capture. Required (>0). The library creates the trigger via configfs
	// — this needs CAP_SYS_ADMIN (root) unless an out-of-band trigger
	// already exists.
	FrequencyHz int

	// TriggerName names the hrtimer trigger created in configfs. Defaults
	// to "goiio-bmp280" if zero.
	TriggerName string

	// BufferLength is the kernel ring depth in samples (defaults to 16).
	BufferLength int

	// ChannelBuffer is the size of the Go channel returned by Stream
	// (defaults to BufferLength).
	ChannelBuffer int
}

// Stream starts a background goroutine that captures samples at the given
// frequency and pushes them onto the returned channel. Cancel ctx (or call
// Close on the BMP280) to stop. The returned channel is closed when the
// stream ends.
//
// This wraps iio.Buffer + an hrtimer trigger. Use Device().Buffer(...) for
// finer-grained control (custom triggers, raw reads).
func (b *BMP280) Stream(ctx context.Context, opts StreamOptions) (<-chan Sample, error) {
	if opts.FrequencyHz <= 0 {
		return nil, fmt.Errorf("bmp280: Stream: FrequencyHz must be > 0")
	}
	if opts.TriggerName == "" {
		opts.TriggerName = "goiio-bmp280"
	}
	if opts.BufferLength <= 0 {
		opts.BufferLength = 16
	}
	if opts.ChannelBuffer <= 0 {
		opts.ChannelBuffer = opts.BufferLength
	}

	trig, err := iio.EnsureHRTimer(opts.TriggerName, opts.FrequencyHz)
	if err != nil {
		return nil, fmt.Errorf("bmp280: %w", err)
	}

	buf, err := b.dev.Buffer(iio.BufferOptions{
		Channels: []string{"pressure", "temp", "timestamp"},
		Length:   opts.BufferLength,
		Trigger:  trig.Name(),
	})
	if err != nil {
		trig.Remove()
		return nil, fmt.Errorf("bmp280: %w", err)
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
				return
			}
			for i := 0; i < n; i++ {
				r := recs[i]
				s := Sample{
					Time:     r.Time,
					TempC:    r.Values["temp"],
					PressKPa: r.Values["pressure"],
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
