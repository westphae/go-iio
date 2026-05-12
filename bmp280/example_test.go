package bmp280_test

import (
	"context"
	"fmt"

	"github.com/westphae/go-iio/bmp280"
)

// Example shows the polled use of the BMP280 convenience wrapper.
func Example() {
	dev, _ := bmp280.Open(bmp280.WithOversampling(2, 16))
	defer dev.Close()

	s, _ := dev.Read()
	fmt.Printf("%.2f °C, %.3f kPa\n", s.TempC, s.PressKPa)
}

// ExampleBMP280_Stream illustrates streaming captures via an hrtimer
// trigger. It is shown without an Output block because it requires the
// kernel iio-trig-hrtimer module and root to create the configfs entry.
func ExampleBMP280_Stream() {
	dev, _ := bmp280.Open()
	defer dev.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := dev.Stream(ctx, bmp280.StreamOptions{FrequencyHz: 10})
	for s := range ch {
		fmt.Printf("%v  %.2f °C  %.3f kPa\n", s.Time, s.TempC, s.PressKPa)
	}
}
