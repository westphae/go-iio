package iio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// BufferOptions configures a buffered capture.
type BufferOptions struct {
	// Channels names the channels to enable in the buffer, e.g.
	// {"pressure", "temp", "timestamp"}. Order does not matter — the kernel
	// orders frame bytes by each channel's scan_elements/in_<name>_index,
	// and the library decodes accordingly.
	Channels []string

	// Length is the kernel ring buffer depth in samples. Larger values
	// tolerate longer reader stalls without dropping samples.
	Length int

	// Watermark, if > 0, sets buffer/watermark so a single read(2) blocks
	// until at least this many samples are available.
	Watermark int

	// Trigger, if non-empty, is written to trigger/current_trigger before
	// the buffer is enabled. Leave empty to use whatever trigger is already
	// bound — useful when the caller managed the trigger externally.
	Trigger string
}

// Record is one decoded sample frame.
type Record struct {
	// Time tracks the timestamp channel if it was enabled and the kernel's
	// current_timestamp_clock is "realtime". Otherwise Time is the wall
	// clock at the moment Read returned the record.
	Time time.Time
	// Values maps channel name → scaled SI value, with the channel's
	// scale and offset already applied. For temperature channels reported
	// in millidegrees Celsius, the value is returned in °C.
	Values map[string]float64
}

// Buffer is an open buffered capture on a Device. Use Device.Buffer to
// create one; close it when done.
type Buffer struct {
	dev    *Device
	stream io.ReadCloser
	layout ScanLayout
	opts   BufferOptions

	closeMu sync.Mutex
	closed  bool

	clock string // value of current_timestamp_clock at open time
}

// Buffer configures and enables a buffered capture on the device.
//
// On success the device's scan_elements are enabled, buffer/length /
// buffer/watermark / trigger/current_trigger are written as specified, and
// buffer/enable is set to 1. /dev/iio:deviceN is opened for reading. Call
// Close to disable the buffer and release the file.
func (d *Device) Buffer(opts BufferOptions) (*Buffer, error) {
	if len(opts.Channels) == 0 {
		return nil, fmt.Errorf("iio: Buffer: at least one channel must be requested")
	}

	scans := make([]ScanChannel, 0, len(opts.Channels))
	for _, name := range opts.Channels {
		ch, ok := d.byCh[name]
		if !ok {
			return nil, fmt.Errorf("iio: Buffer: unknown channel %q", name)
		}
		if !ch.hasScan {
			return nil, fmt.Errorf("iio: Buffer: channel %q has no scan_elements entry", name)
		}
		sc := ch.scan
		sc.Name = name
		scans = append(scans, sc)
	}
	layout := BuildLayout(scans)

	// Quiesce buffer before reconfiguring; ignore errors (it may already be off).
	_ = d.SetAttr("buffer/enable", "0")

	// Disable all known scan elements first so a re-Buffer with a different
	// channel set doesn't leave stale ones enabled.
	for _, ch := range d.chs {
		if ch.hasScan {
			_ = d.SetChannelAttr(ch.name, "scan_elements/en", "0")
		}
	}
	for _, name := range opts.Channels {
		if err := d.SetChannelAttr(name, "scan_elements/en", "1"); err != nil {
			return nil, fmt.Errorf("iio: enable scan element %q: %w", name, err)
		}
	}

	if opts.Length > 0 {
		if err := d.SetAttr("buffer/length", fmt.Sprintf("%d", opts.Length)); err != nil {
			return nil, fmt.Errorf("iio: set buffer/length: %w", err)
		}
	}
	if opts.Watermark > 0 {
		// watermark is optional and not all drivers expose it; tolerate ENOENT.
		_ = d.SetAttr("buffer/watermark", fmt.Sprintf("%d", opts.Watermark))
	}
	if opts.Trigger != "" {
		if err := d.SetAttr("trigger/current_trigger", opts.Trigger); err != nil {
			return nil, fmt.Errorf("iio: bind trigger %q: %w", opts.Trigger, err)
		}
	}

	// Record the timestamp clock; the BMP280 driver and most kernels default
	// to "realtime" but the user can override it. We surface this so callers
	// know how to interpret Record.Time.
	clock, _ := d.Attr("current_timestamp_clock")

	stream, err := d.h.OpenBufferStream()
	if err != nil {
		return nil, err
	}

	if err := d.SetAttr("buffer/enable", "1"); err != nil {
		stream.Close()
		return nil, fmt.Errorf("iio: enable buffer: %w", err)
	}

	return &Buffer{
		dev:    d,
		stream: stream,
		layout: layout,
		opts:   opts,
		clock:  clock,
	}, nil
}

// Layout returns the byte layout of the buffer's sample frames.
func (b *Buffer) Layout() ScanLayout { return b.layout }

// TimestampClock returns the value of current_timestamp_clock at open time
// ("realtime", "monotonic", "boottime", ...).
func (b *Buffer) TimestampClock() string { return b.clock }

// Read fills dst with decoded sample records, blocking until at least one
// frame is available. Returns the number of records written to dst.
//
// The reader operates on whole frames: it reads len(dst)*FrameBytes from the
// stream in one syscall (the kernel will return at most as many whole frames
// as are currently buffered). Use ctx to cancel a long wait.
func (b *Buffer) Read(ctx context.Context, dst []Record) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	frame := b.layout.FrameBytes
	if frame == 0 {
		return 0, fmt.Errorf("iio: Buffer.Read: zero frame size (no channels enabled?)")
	}
	raw := make([]byte, len(dst)*frame)
	n, err := readWithCtx(ctx, b.stream, raw)
	if err != nil && n == 0 {
		return 0, err
	}
	frames := n / frame
	if frames == 0 {
		return 0, fmt.Errorf("iio: Buffer.Read: short read (%d bytes, frame=%d)", n, frame)
	}
	for i := 0; i < frames; i++ {
		dst[i] = b.decode(raw[i*frame : (i+1)*frame])
	}
	return frames, nil
}

// ReadRaw fills dst with raw bytes from the buffer and returns the count read
// along with the scan layout needed to decode them. Bytes always come in whole
// multiples of FrameBytes (the kernel enforces this; callers should pass a
// buffer sized accordingly).
func (b *Buffer) ReadRaw(ctx context.Context, dst []byte) (int, ScanLayout, error) {
	if b.layout.FrameBytes > 0 && len(dst)%b.layout.FrameBytes != 0 {
		return 0, b.layout, fmt.Errorf("iio: Buffer.ReadRaw: dst len %d not a multiple of FrameBytes %d", len(dst), b.layout.FrameBytes)
	}
	n, err := readWithCtx(ctx, b.stream, dst)
	return n, b.layout, err
}

// Close disables the buffer and releases the /dev/iio:deviceN file.
//
// Best-effort: writes buffer/enable=0, unbinds the trigger if Buffer set
// one, and closes the stream regardless of individual errors. Channel
// scan_elements are left in their enabled state so the caller can re-open
// cheaply; call Device.SetChannelAttr(ch, "scan_elements/en", "0") to fully
// reset.
func (b *Buffer) Close() error {
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	disableErr := b.dev.SetAttr("buffer/enable", "0")
	if b.opts.Trigger != "" {
		// A zero-byte write is a no-op as far as sysfs trigger/current_trigger
		// is concerned; we have to write at least a newline for the kernel's
		// store handler to interpret the (empty) value as "unbind".
		_ = b.dev.SetAttr("trigger/current_trigger", "\n")
	}
	closeErr := b.stream.Close()
	if disableErr != nil {
		return disableErr
	}
	return closeErr
}

func (b *Buffer) decode(frame []byte) Record {
	rec := Record{Values: make(map[string]float64, len(b.layout.Channels))}
	var tsNanos int64
	haveTS := false
	for _, sc := range b.layout.Channels {
		raw := readScan(frame, sc)
		if sc.Name == "timestamp" {
			tsNanos = raw
			haveTS = true
			continue
		}
		v := float64(raw)
		if sc.Offset != 0 {
			v += sc.Offset
		}
		if sc.Scale != 0 {
			v *= sc.Scale
		}
		if sc.Name == "temp" {
			// IIO convention: in_temp_input is m°C — apply the same divisor
			// when decoding raw*scale so callers get °C either way.
			v /= 1000.0
		}
		rec.Values[sc.Name] = v
	}
	if haveTS && b.clock == "realtime" {
		rec.Time = time.Unix(0, tsNanos)
	} else if haveTS {
		// Non-realtime clock (monotonic, boottime, ...): expose the integer
		// nanoseconds via Values so callers can interpret it themselves.
		rec.Values["timestamp_ns"] = float64(tsNanos)
		rec.Time = time.Now()
	} else {
		rec.Time = time.Now()
	}
	return rec
}

// readWithCtx performs a Read that respects ctx cancellation. For *os.File
// streams (the sysfs backend's /dev/iio:deviceN handle) it uses
// SetReadDeadline so a cancelled context does not leave a stray blocked Read
// on the fd — important when callers time out and immediately retry.
func readWithCtx(ctx context.Context, r io.Reader, buf []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if f, ok := r.(*os.File); ok {
		if deadline, ok := ctx.Deadline(); ok {
			if err := f.SetReadDeadline(deadline); err == nil {
				n, err := f.Read(buf)
				_ = f.SetReadDeadline(time.Time{})
				if err != nil && ctx.Err() != nil &&
					(errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)) {
					return 0, ctx.Err()
				}
				return n, err
			}
		}
	}
	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := r.Read(buf)
		ch <- res{n, err}
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case r := <-ch:
		return r.n, r.err
	}
}
