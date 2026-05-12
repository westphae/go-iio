package iio_test

import (
	"context"
	"fmt"
	"log"

	"github.com/westphae/go-iio"
)

// Example shows the typical polled use of the library: open a sensor by its
// kernel-reported name and read its channels in SI units.
func Example() {
	dev, err := iio.Open("bmp280")
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()

	temp, _ := dev.ReadFloat("temp")     // °C
	press, _ := dev.ReadFloat("pressure") // kPa
	fmt.Printf("%.2f °C, %.3f kPa\n", temp, press)
}

// ExampleParseTypeString decodes the three channel type strings exposed by
// the mainline BMP280 IIO driver. The format is documented in the kernel ABI
// (Documentation/ABI/testing/sysfs-bus-iio): "[endian]:[s|u][realbits]/[storagebits][>>shift]".
func ExampleParseTypeString() {
	for _, s := range []string{"le:u32/32>>0", "le:s32/32>>0", "le:s64/64>>0"} {
		ch, _ := iio.ParseTypeString(s)
		fmt.Printf("%-14s signed=%v bits=%d storage=%d\n", s, ch.Signed, ch.RealBits, ch.StorageBits)
	}
	// Output:
	// le:u32/32>>0   signed=false bits=32 storage=32
	// le:s32/32>>0   signed=true bits=32 storage=32
	// le:s64/64>>0   signed=true bits=64 storage=64
}

// ExampleBuildLayout shows how a set of enabled channels lays out in the
// per-sample byte frame the kernel produces on /dev/iio:deviceN. Channels
// are ordered by scan_elements/in_<name>_index, and each is naturally
// aligned (a channel of N bytes is aligned to N bytes) — which is why the
// timestamp here lands at offset 8 with no padding (pressure ends at 4,
// then temp ends at 8, and the s64 timestamp aligns there for free).
func ExampleBuildLayout() {
	layout := iio.BuildLayout([]iio.ScanChannel{
		{Name: "pressure", Index: 0, RealBits: 32, StorageBits: 32},
		{Name: "temp", Index: 1, Signed: true, RealBits: 32, StorageBits: 32},
		{Name: "timestamp", Index: 2, Signed: true, RealBits: 64, StorageBits: 64},
	})
	fmt.Printf("frame=%d bytes\n", layout.FrameBytes)
	for _, c := range layout.Channels {
		fmt.Printf("  %-9s offset=%d\n", c.Name, c.ByteOffset)
	}
	// Output:
	// frame=16 bytes
	//   pressure  offset=0
	//   temp      offset=4
	//   timestamp offset=8
}

// ExampleDevice_Buffer illustrates a buffered capture. It is shown without
// an Output block because it requires real hardware and an iio-trig-hrtimer
// trigger created via configfs (typically root-only).
func ExampleDevice_Buffer() {
	dev, _ := iio.Open("bmp280")
	defer dev.Close()

	trig, _ := iio.EnsureHRTimer("example-trig", 10) // 10 Hz
	defer trig.Remove()

	buf, _ := dev.Buffer(iio.BufferOptions{
		Channels: []string{"pressure", "temp", "timestamp"},
		Length:   16,
		Trigger:  trig.Name(),
	})
	defer buf.Close()

	recs := make([]iio.Record, 8)
	ctx := context.Background()
	for {
		n, err := buf.Read(ctx, recs)
		if err != nil {
			return
		}
		for i := 0; i < n; i++ {
			fmt.Println(recs[i].Time, recs[i].Values["temp"], recs[i].Values["pressure"])
		}
	}
}
