// iio-dump prints every attribute of every IIO device visible to the
// default backend. Intended as a debugging aid when bringing up a new
// sensor — comparable to `iio_info` but produced by this library, so it
// also serves as a parity check on attribute discovery.
//
//	go run ./cmd/iio-dump
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/westphae/go-iio"
)

func main() {
	one := flag.String("name", "", "if non-empty, dump only the device with this name")
	flag.Parse()

	devs, err := iio.Discover()
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	if len(devs) == 0 {
		fmt.Println("(no IIO devices found)")
		return
	}
	for _, info := range devs {
		if *one != "" && info.Name != *one {
			continue
		}
		fmt.Printf("=== %s  (%s) ===\n", info.Name, info.Path)
		dev, err := iio.OpenPath(info.Path)
		if err != nil {
			fmt.Printf("  ! open: %v\n", err)
			continue
		}
		dumpDevice(dev)
		dev.Close()
		fmt.Println()
	}
}

func dumpDevice(dev *iio.Device) {
	fmt.Printf("  channels (%d):\n", len(dev.Channels()))
	for _, c := range dev.Channels() {
		fmt.Printf("    %-14s hasRaw=%v hasInput=%v scale=%g offset=%g",
			c.Name(), c.HasRaw(), c.HasInput(), c.Scale(), c.Offset())
		if sc, ok := c.Scan(); ok {
			fmt.Printf(" scan{idx=%d %s%d/%d>>%d %s}",
				sc.Index, signMark(sc.Signed), sc.RealBits, sc.StorageBits, sc.Shift, endianMark(sc.BigEndian))
		}
		fmt.Println()
	}

	if v, err := dev.Attr("name"); err == nil {
		fmt.Printf("  name attr: %s\n", v)
	}
	if v, err := dev.Attr("current_timestamp_clock"); err == nil {
		fmt.Printf("  current_timestamp_clock: %s\n", v)
	}
	for _, p := range []string{"buffer/enable", "buffer/length", "buffer/watermark", "buffer/data_available"} {
		if v, err := dev.Attr(p); err == nil {
			fmt.Printf("  %-26s %s\n", p, v)
		}
	}
	if v, err := dev.Attr("trigger/current_trigger"); err == nil {
		fmt.Printf("  trigger/current_trigger:   %q\n", v)
	}
}

func signMark(s bool) string {
	if s {
		return "s"
	}
	return "u"
}

func endianMark(be bool) string {
	if be {
		return "be"
	}
	return "le"
}
