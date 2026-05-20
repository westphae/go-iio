package mmc5983ma_test

import (
	"context"
	"fmt"

	"github.com/westphae/go-iio/mmc5983ma"
)

// Example shows polled use of the MMC5983MA convenience wrapper.
func Example() {
	dev, _ := mmc5983ma.Open(
		mmc5983ma.WithSamplingFrequencyHz(100),
		mmc5983ma.WithBandwidthHz(200),
	)
	defer dev.Close()

	s, _ := dev.Read()
	fmt.Printf("mag: %.2f %.2f %.2f µT  %.2f °C\n",
		s.MagX, s.MagY, s.MagZ, s.TempC)
}

// ExampleMMC5983MA_Stream illustrates streaming captures via an hrtimer
// trigger. It is shown without an Output block because it requires the
// kernel iio-trig-hrtimer module and root to create the configfs entry.
func ExampleMMC5983MA_Stream() {
	dev, _ := mmc5983ma.Open()
	defer dev.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := dev.Stream(ctx, mmc5983ma.StreamOptions{FrequencyHz: 100})
	for s := range ch {
		fmt.Printf("%v  mag: %.1f %.1f %.1f µT\n",
			s.Time, s.MagX, s.MagY, s.MagZ)
	}
}

// ExampleMMC5983MA_AutoNullCalibBias shows the SET/measure / RESET/measure
// auto-null cycle that populates the driver's software calibration bias.
// Run this in a known-stable field (Helmholtz coil, magnetic shield) for
// best results.
func ExampleMMC5983MA_AutoNullCalibBias() {
	dev, _ := mmc5983ma.Open()
	defer dev.Close()

	_ = dev.AutoNullCalibBias()
	x, y, z, _ := dev.CalibBias()
	fmt.Printf("new calibbias: x=%d y=%d z=%d LSB\n", x, y, z)
}
