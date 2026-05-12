// buffered captures N samples from a BMP280 in buffered mode using an
// hrtimer trigger created on the fly. Decodes pressure/temp/timestamp out of
// each frame and prints them.
//
//	sudo go run ./examples/buffered -hz 10 -n 20
//
// Needs CAP_SYS_ADMIN (root) to create the trigger via configfs.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/westphae/go-iio"
)

func main() {
	hz := flag.Int("hz", 10, "sampling frequency")
	n := flag.Int("n", 10, "number of records to capture")
	trigName := flag.String("trig", "goiio-bmp280", "hrtimer trigger name to create in configfs")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dev, err := iio.Open("bmp280")
	if err != nil {
		log.Fatalf("open bmp280: %v", err)
	}
	defer dev.Close()

	trig, err := iio.EnsureHRTimer(*trigName, *hz)
	if err != nil {
		log.Fatalf("ensure hrtimer: %v", err)
	}
	defer trig.Remove()

	buf, err := dev.Buffer(iio.BufferOptions{
		Channels: []string{"pressure", "temp", "timestamp"},
		Length:   16,
		Trigger:  trig.Name(),
	})
	if err != nil {
		log.Fatalf("open buffer: %v", err)
	}
	defer buf.Close()

	fmt.Printf("device:      %s\n", dev.Name())
	fmt.Printf("trigger:     %s @ %d Hz\n", trig.Name(), *hz)
	fmt.Printf("layout:      %d bytes/frame, channels=%d, clock=%s\n",
		buf.Layout().FrameBytes, len(buf.Layout().Channels), buf.TimestampClock())
	for _, c := range buf.Layout().Channels {
		fmt.Printf("  %-12s idx=%d offset=%d %s%d/%d scale=%g\n",
			c.Name, c.Index, c.ByteOffset,
			signMark(c.Signed), c.RealBits, c.StorageBits, c.Scale)
	}
	fmt.Println()

	recs := make([]iio.Record, 16)
	got := 0
	for got < *n {
		k, err := buf.Read(ctx, recs)
		if err != nil {
			log.Fatalf("read: %v", err)
		}
		for i := 0; i < k && got < *n; i++ {
			r := recs[i]
			fmt.Printf("[%s] temp=%.3f °C  pressure=%.3f kPa\n",
				r.Time.Format("15:04:05.000"),
				r.Values["temp"], r.Values["pressure"])
			got++
		}
	}
}

func signMark(signed bool) string {
	if signed {
		return "s"
	}
	return "u"
}
