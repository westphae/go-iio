// polled does a single-shot read of a BMP280's temperature and pressure
// channels via the kernel IIO interface. Useful as a smoke test that the
// device tree overlay is loaded and the sysfs backend works.
//
//	go run ./examples/polled
package main

import (
	"fmt"
	"log"

	"github.com/westphae/go-iio"
)

func main() {
	dev, err := iio.Open("bmp280")
	if err != nil {
		log.Fatalf("open bmp280: %v", err)
	}
	defer dev.Close()

	temp, err := dev.ReadFloat("temp")
	if err != nil {
		log.Fatalf("read temp: %v", err)
	}
	press, err := dev.ReadFloat("pressure")
	if err != nil {
		log.Fatalf("read pressure: %v", err)
	}

	fmt.Printf("device:   %s\n", dev.Name())
	fmt.Printf("temp:     %.3f °C\n", temp)
	fmt.Printf("pressure: %.3f kPa\n", press)
}
