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

For BMP280 specifically there's a convenience wrapper:

```go
import "github.com/westphae/go-iio/bmp280"

dev, _ := bmp280.Open(bmp280.WithOversampling(2, 16))
ch, _ := dev.Stream(ctx, bmp280.StreamOptions{FrequencyHz: 10})
for s := range ch {
    fmt.Println(s.TempC, s.PressKPa)
}
```

The `icm20948` subpackage wraps the InvenSense ICM-20948 9-axis IMU with the
same shape (`Open / Read / Stream`, full-scale knobs via `WithAccelScale` /
`WithGyroScale`). It expects the kernel `icm20948` driver — either the
out-of-tree `github.com/westphae/icm20948-mod` or the mainline
`inv_icm20948` once it lands in your kernel.

See `examples/` for runnable programs, and `CLAUDE.md` for the design notes.

## Status

v0: pure-Go sysfs backend, BMP280 polled and buffered captures, ICM-20948
convenience wrapper. A `backend/libiio` (cgo) slot is reserved for remote
sensors over `iiod` but is not yet implemented.
