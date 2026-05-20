# go-iio

[![Go Reference](https://pkg.go.dev/badge/github.com/westphae/go-iio.svg)](https://pkg.go.dev/github.com/westphae/go-iio)
[![CI](https://github.com/westphae/go-iio/actions/workflows/ci.yml/badge.svg)](https://github.com/westphae/go-iio/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/westphae/go-iio)](https://goreportcard.com/report/github.com/westphae/go-iio)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A small, idiomatic Go library for reading Linux Industrial I/O (IIO) sensors
— the ones that show up under `/sys/bus/iio/devices/` after a device tree
overlay loads. Polled and buffered captures, dynamic channel discovery, no
cgo.

```go
import "github.com/westphae/go-iio"

dev, _ := iio.Open("bmp280")
defer dev.Close()

t, _ := dev.ReadFloat("temp")     // °C
p, _ := dev.ReadFloat("pressure") // kPa
```

Buffered capture at a fixed rate (BMP280 has no hardware data-ready, so the
library drives it from a kernel hrtimer trigger):

```go
trig, _ := iio.EnsureHRTimer("mytrig", 10) // 10 Hz; needs CAP_SYS_ADMIN
defer trig.Remove()

buf, _ := dev.Buffer(iio.BufferOptions{
    Channels: []string{"pressure", "temp", "timestamp"},
    Length:   16,
    Trigger:  trig.Name(),
})
defer buf.Close()

recs := make([]iio.Record, 16)
for {
    n, _ := buf.Read(ctx, recs)
    for i := 0; i < n; i++ { /* recs[i].Time, recs[i].Values["temp"], ... */ }
}
```

See `examples/` for runnable programs, and `CLAUDE.md` for the design notes.

## Sensor wrappers

The generic `iio` API works for any kernel-supported sensor, but the
`bmp280`, `icm20948`, and `mmc5983ma` subpackages bundle the channel names,
scale writes, trigger setup, and decode logic specific to those chips so
the calling code is just `Open / Read / Stream`. All three wrappers expose
`Device()` for the underlying `*iio.Device` when you need an attribute the
wrapper doesn't cover.

### BMP280

Bosch BMP280 pressure/temperature sensor. Wraps the mainline `bmp280`
kernel driver. Samples are `°C` and `kPa`.

```go
import "github.com/westphae/go-iio/bmp280"

dev, err := bmp280.Open(
    bmp280.WithOversampling(2, 16), // temp x2, pressure x16
)
if err != nil { /* ... */ }
defer dev.Close()

// Polled — one sample per call, two sysfs reads each:
s, _ := dev.Read()
fmt.Println(s.TempC, s.PressKPa)
```

Buffered capture (the BMP280 has no hardware data-ready, so the wrapper
creates an hrtimer trigger via configfs — needs `CAP_SYS_ADMIN`, and
`iio-trig-hrtimer` must be loaded):

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

ch, err := dev.Stream(ctx, bmp280.StreamOptions{
    FrequencyHz:  10,                // required, > 0
    TriggerName:  "",                // optional, defaults to "goiio-bmp280"
    BufferLength: 16,                // kernel ring depth in samples
})
if err != nil { /* ... */ }
for s := range ch {
    fmt.Printf("%s  %.2f °C  %.3f kPa\n", s.Time.Format(time.RFC3339Nano), s.TempC, s.PressKPa)
}
```

Options:

- `WithPath(p)` — open a specific sysfs path instead of looking up by name
  (useful if multiple BMP280s are present).
- `WithOversampling(temp, pressure)` — writes `in_temp_oversampling_ratio`
  and `in_pressure_oversampling_ratio`. Valid: `1, 2, 4, 8, 16`.

### ICM-20948

InvenSense / TDK ICM-20948 9-axis IMU (3-axis accel + 3-axis gyro + AK09916
magnetometer + die temperature). Targets the out-of-tree
`github.com/westphae/icm20948-mod` driver or the mainline `inv_icm20948`
driver — both expose the same standard IIO channel naming. Samples are SI:
m/s² (accel), rad/s (gyro), µT (mag — converted from the kernel's Gauss),
°C (temp).

```go
import "github.com/westphae/go-iio/icm20948"

dev, err := icm20948.Open(
    icm20948.WithAccelScale(4),        // ±4 G
    icm20948.WithGyroScale(500),       // ±500 dps
    icm20948.WithAccelDLPFHz(50),      // snap to nearest available cutoff
    icm20948.WithGyroDLPFHz(50),
)
if err != nil { /* ... */ }
defer dev.Close()

// Polled — one Sample per call, ten sysfs reads:
s, _ := dev.Read()
fmt.Printf("a=(%.2f,%.2f,%.2f) g=(%.3f,%.3f,%.3f) m=(%.1f,%.1f,%.1f) T=%.1f\n",
    s.AccelX, s.AccelY, s.AccelZ,
    s.GyroX, s.GyroY, s.GyroZ,
    s.MagX, s.MagY, s.MagZ,
    s.TempC,
)
```

Buffered capture (same hrtimer mechanics as BMP280):

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

ch, err := dev.Stream(ctx, icm20948.StreamOptions{
    FrequencyHz:  100, // accel/gyro pace; AK09916 mag is fixed at 100 Hz in the driver
    BufferLength: 32,
})
if err != nil { /* ... */ }
for s := range ch {
    // s.Time, s.AccelX/Y/Z, s.GyroX/Y/Z, s.MagX/Y/Z, s.TempC
}
```

Options:

- `WithPath(p)` — open a specific sysfs path instead of looking up by name.
- `WithAccelScale(g)` — `2 / 4 / 8 / 16`. Writes `in_accel_scale` with the
  exact SI string the driver expects.
- `WithGyroScale(dps)` — `250 / 500 / 1000 / 2000`. Writes `in_anglvel_scale`.
- `WithAccelDLPFHz(hz)` / `WithGyroDLPFHz(hz)` — on-chip digital low-pass
  cutoff. The driver only accepts values from
  `in_<ch>_filter_low_pass_3db_frequency_available`; the wrapper snaps to
  the nearest available value.

Magnetometer overflow: the AK09916 saturates at ±4912 µT. The wrapper
exposes the sticky `in_magn_overrange` flag so callers can detect a clipped
axis without scanning every sample:

```go
if over, _ := dev.Overrange(); over {
    // at least one sample since the last clear hit the chip's range
    _ = dev.ClearOverrange()
}
```

Note on rates: the magnetometer's internal conversion runs at a fixed
100 Hz in the kernel driver, so at trigger rates above 100 Hz consecutive
samples will repeat the same mag values until the next conversion lands.

### MMC5983MA

MEMSIC MMC5983MA 3-axis AMR magnetometer (±8 G, 18-bit, on-chip temperature
sensor). Targets the out-of-tree `github.com/westphae/mmc5983ma-mod`
kernel driver. Samples are µT (mag — converted from the kernel's Gauss)
and °C (temp).

```go
import "github.com/westphae/go-iio/mmc5983ma"

dev, err := mmc5983ma.Open(
    mmc5983ma.WithSamplingFrequencyHz(100),  // continuous-mode ODR
    mmc5983ma.WithBandwidthHz(200),          // measurement bandwidth
)
if err != nil { /* ... */ }
defer dev.Close()

// Polled — one Sample per call, four sysfs reads (mag x/y/z + temp):
s, _ := dev.Read()
fmt.Printf("mag: %.1f %.1f %.1f µT  %.2f °C\n",
    s.MagX, s.MagY, s.MagZ, s.TempC)
```

Buffered capture (same hrtimer mechanics as BMP280/ICM-20948):

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

ch, err := dev.Stream(ctx, mmc5983ma.StreamOptions{
    FrequencyHz:  100,
    BufferLength: 32,
})
if err != nil { /* ... */ }
for s := range ch {
    // s.Time, s.MagX/Y/Z — TempC is not in the buffered scan; poll Read for it.
}
```

Options:

- `WithPath(p)` — open a specific sysfs path instead of looking up by name.
- `WithSamplingFrequencyHz(hz)` — `1 / 10 / 20 / 50 / 100 / 200 / 1000`.
  The driver auto-bumps `BandwidthHz` if required by the chip's
  ODR-vs-BW constraints (200 Hz → BW≥200, 1000 Hz → BW=800).
- `WithBandwidthHz(hz)` — `100 / 200 / 400 / 800`. Higher BW = shorter
  measurement window = noisier readings but faster max ODR.
- `WithPeriodicSet(samples)` — supplementary periodic-SET cadence on top
  of Auto SR (which is always on). `0` disables. Useful only in extreme
  thermal environments; leave at the default otherwise.

AMR-specific helpers:

```go
// Recover after a strong-field event:
_ = dev.SetPulse()        // 500 ns SET pulse through the chip's coil

// Null the bridge offset (best in a known-stable field — Helmholtz coil
// or magnetic shield). Updates the driver's software calibbias so
// subsequent Read/Stream values come back with offset cancelled.
_ = dev.AutoNullCalibBias()
x, y, z, _ := dev.CalibBias()  // inspect the new offsets

// Confirm the chip is responsive (St_enp / St_enm coils):
_ = dev.RunSelftest(true)            // positive coil
dx, dy, dz, _ := dev.SelftestDelta() // delta vs baseline in raw LSB
_ = dev.ClearSelftest()
```

Note on temperature: the chip can only do mag OR temp at a time, so the
buffered scan layout includes the three mag axes plus timestamp but
*not* temperature. `Sample.TempC` is zero in `Stream` samples; use
`Read()` (which briefly pauses the mag stream for a one-shot temp
measurement) when you need temperature.

## Status

v0: pure-Go sysfs backend, BMP280 polled and buffered captures, ICM-20948
and MMC5983MA convenience wrappers. A `backend/libiio` (cgo) slot is
reserved for remote sensors over `iiod` but is not yet implemented.
