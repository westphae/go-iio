# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`github.com/westphae/go-iio` is a Go library for reading sensors exposed by the
Linux Industrial I/O (IIO) subsystem — i.e. sensors brought up via device
tree overlays so the kernel owns the I²C/SPI bus and presents data under
`/sys/bus/iio/devices/`. The eventual goal is to replace the userspace I²C
bit-banging drivers in `../goflying/sensors/` (BMP280, ICM-20948, MPU-9250).
The goflying-side adapters that convert this package's `Sample`/`Record`
types to `sensors.BMPData` / `sensors.IMUData` live in goflying, not here —
`go-iio` does not import goflying.

## Build & test

- Floor: Go 1.22. Module: `github.com/westphae/go-iio`. Pure Go, zero runtime
  deps; no cgo.
- `go build ./...` — compile every package.
- `go test ./...` — parser/layout/decode tests; all run anywhere (they do not
  touch `/sys`).
- `go vet ./...` — clean.
- Live-hardware exercises (need the Pi and a BMP280 on iio:device0):
  - `go run ./examples/polled` — single-shot temp/pressure read via sysfs.
  - `sudo go run ./examples/buffered -hz 10 -n 20` — buffered capture via
    `/dev/iio:deviceN` using an hrtimer trigger created in configfs.
  - `sudo go run ./examples/bmp280_stream -hz 10 -n 20` — same flow through
    the `bmp280` convenience wrapper.
  - `go run ./cmd/iio-dump` — dumps every attribute of every visible IIO
    device; useful when bringing up a new sensor.

The buffered captures need CAP_SYS_ADMIN (root) only to `mkdir` the trigger
under `/sys/kernel/config/iio/triggers/hrtimer/`. The kernel module
`iio-trig-hrtimer` must be loaded (`sudo modprobe iio-trig-hrtimer`).

## Architecture

```
iio.Open(name) → *Device → []*Channel
                   │
                   ├── ReadFloat()    (polled, sysfs in_<ch>_input or in_<ch>_raw*scale+offset)
                   └── Buffer(opts)   (buffered, /dev/iio:deviceN)
                          ├── Read()    → []Record (decoded float64 per channel + time)
                          └── ReadRaw() → []byte + ScanLayout (caller decodes)
```

The core package depends only on a narrow `backend.Backend` interface and a
narrow `backend.DeviceHandle` interface — attribute string get/set + bulk
byte stream + Close. The default `backend/sysfs` implementation registers
itself via `init()` (so importing `go-iio` is enough). A future
`backend/libiio` (cgo) drop-in can serve remote sensors over `iiod` without
touching the core. **Do not push parsing into a backend implementation**:
`scan_elements` / type-string / layout / decode logic all lives in the core
(`scan.go`, `buffer.go`) so every backend benefits from the same parser.

### IIO buffered captures: things to remember

- Always read `scan_elements/in_<name>_{en,index,type}` at runtime. Frame
  layout is determined by sorting channels by `index` and walking them with
  natural alignment (a channel of N bytes is aligned to N bytes; `s64`
  timestamps land on 8-byte boundaries). `BuildLayout` in `scan.go` does
  this — don't bypass it with hardcoded offsets.
- The BMP280 driver in mainline kernels (verified on 6.18.29-v8+) does not
  expose a hardware data-ready interrupt, so buffered mode requires an
  hrtimer trigger created via configfs at
  `/sys/kernel/config/iio/triggers/hrtimer/<name>`. `iio.EnsureHRTimer`
  handles the create-and-frequency dance and returns an object whose
  `Remove()` cleans up.
- A short read from `/dev/iio:deviceN` with the wrong buffer size returns
  EINVAL — always size reads in whole multiples of `Layout().FrameBytes`.
- `current_timestamp_clock` defaults to "realtime" on this kernel; `Buffer`
  records it at open time and surfaces it on `*Buffer` so callers know which
  clock the `Record.Time` belongs to. For non-realtime clocks the raw
  nanoseconds appear in `Record.Values["timestamp_ns"]`.

### Coordinate frames / units

This is a sensor library; it does not interpret data. We expose what the
kernel exposes, converted to SI:

- BMP280: °C and kPa. The kernel reports `in_temp_input` in m°C — `ReadFloat`
  and the buffered decoder divide by 1000 so callers always see °C.
- ICM-20948: m/s² (accel), rad/s (anglvel), µT (magn — wrapper converts the
  kernel's Gauss to µT), °C (temp). When the kernel driver only exposes a
  type-level scale/offset (e.g. one `in_accel_scale` shared across x/y/z),
  `newDevice` inherits the type-level values onto each axis so buffered
  decode applies the right factor.
- MMC5983MA: µT (magn — wrapper converts the kernel's Gauss to µT), °C
  (temp). Temperature is *not* in the buffered scan (chip can only do mag
  OR temp at a time; running temp would have to pause the continuous mag
  stream). `Stream` samples have `TempC == 0`; use `Read()` when you need
  temperature.

Coordinate-frame / mount-matrix concerns belong in the *consumer* (e.g.
goflying's AHRS), not here. We pass mount-matrix attributes through as
strings if present.

## Status / deferred

- v0 ships sysfs backend only. A `backend/libiio` slot exists in the layout
  but is not implemented; add it when remote sensors or USB SDRs that need
  `iiod` actually show up.
- ICM-20948 wrapper lives under `icm20948/` and reuses the same
  `Device`/`Buffer` API. It targets the out-of-tree
  `github.com/westphae/icm20948-mod` kernel driver (the mainline
  `inv_icm20948` driver was merged upstream ~Aug 2025 but is not yet enabled
  in the Raspberry Pi OS kernel as of 6.18.29-v8+; both expose the standard
  IIO channel naming, so the wrapper works against either).
- MMC5983MA wrapper lives under `mmc5983ma/` and targets the out-of-tree
  `github.com/westphae/mmc5983ma-mod` kernel driver. Same `Device`/`Buffer`
  API. Adds AMR-specific helpers — `SetPulse`/`ResetPulse` (manual
  degauss), `AutoNullCalibBias` (the chip's SET-measure / RESET-measure /
  store-offset recipe), `CalibBias`/`SetCalibBias` (read/write the kernel
  driver's software offset shadow), and `RunSelftest`/`SelftestDelta`
  (in_magn_test). Buffered scan is mag x/y/z + timestamp only; temperature
  is polled-only because the chip can't do mag and temp simultaneously.
- The BMP280 driver does not expose `in_*_sampling_frequency` — rate is
  controlled by the trigger's `sampling_frequency` plus per-channel
  oversampling (`in_<ch>_oversampling_ratio`).

## Testing IIO without hardware

Most parser/layout/decode logic is exercised by `scan_test.go` with hand-
crafted byte buffers (including the actual 16-byte frame observed on the
live BMP280). To exercise the sysfs backend against a fake tree, use
`backend/sysfs.NewWithRoot(sysfsRoot, devRoot)` — point it at a directory
that mirrors the relevant subset of `/sys/bus/iio/devices`.
