package icm45686_test

import (
	"context"
	"fmt"

	"github.com/westphae/go-iio/icm45686"
)

// Example shows polled use of the ICM-45686 convenience wrapper.
func Example() {
	dev, _ := icm45686.Open(icm45686.WithAccelScale(16), icm45686.WithGyroScale(2000))
	defer dev.Close()

	s, _ := dev.Read()
	fmt.Printf("accel: %.2f %.2f %.2f m/s²  gyro: %.4f %.4f %.4f rad/s  %.2f °C\n",
		s.AccelX, s.AccelY, s.AccelZ,
		s.GyroX, s.GyroY, s.GyroZ,
		s.TempC)
}

// ExampleICM45686_Stream illustrates streaming captures via an hrtimer
// trigger. It is shown without an Output block because it requires the
// kernel iio-trig-hrtimer module and root to create the configfs entry.
func ExampleICM45686_Stream() {
	dev, _ := icm45686.Open(icm45686.WithAccelScale(32), icm45686.WithGyroScale(4000))
	defer dev.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := dev.Stream(ctx, icm45686.StreamOptions{FrequencyHz: 200})
	for s := range ch {
		fmt.Printf("%v  accel: %.2f %.2f %.2f  gyro: %.4f %.4f %.4f  %.2f °C\n",
			s.Time, s.AccelX, s.AccelY, s.AccelZ,
			s.GyroX, s.GyroY, s.GyroZ,
			s.TempC)
	}
}
