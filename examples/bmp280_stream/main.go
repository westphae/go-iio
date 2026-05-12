// bmp280_stream demonstrates the bmp280.Stream convenience API: it captures
// samples at a fixed rate using a kernel hrtimer trigger and prints them.
//
//	sudo go run ./examples/bmp280_stream -hz 10 -n 20
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/westphae/go-iio/bmp280"
)

func main() {
	hz := flag.Int("hz", 10, "sample rate (Hz)")
	n := flag.Int("n", 10, "number of samples")
	osT := flag.Int("os-temp", 0, "temperature oversampling (1,2,4,8,16 — 0 = leave unchanged)")
	osP := flag.Int("os-press", 0, "pressure oversampling (1,2,4,8,16 — 0 = leave unchanged)")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var opts []bmp280.Option
	if *osT > 0 || *osP > 0 {
		opts = append(opts, bmp280.WithOversampling(*osT, *osP))
	}
	dev, err := bmp280.Open(opts...)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer dev.Close()

	ch, err := dev.Stream(ctx, bmp280.StreamOptions{FrequencyHz: *hz})
	if err != nil {
		log.Fatalf("stream: %v", err)
	}

	got := 0
	for s := range ch {
		fmt.Printf("[%s] temp=%.3f °C  pressure=%.3f kPa\n",
			s.Time.Format("15:04:05.000"), s.TempC, s.PressKPa)
		got++
		if got >= *n {
			cancel()
		}
	}
}
