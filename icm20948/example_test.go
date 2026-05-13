package icm20948_test

import (
	"context"
	"fmt"

	"github.com/westphae/go-iio/icm20948"
)

// Example shows polled use of the ICM-20948 convenience wrapper.
func Example() {
	dev, _ := icm20948.Open(icm20948.WithAccelScale(4), icm20948.WithGyroScale(500))
	defer dev.Close()

	s, _ := dev.Read()
	fmt.Printf("accel: %.2f %.2f %.2f m/s²  gyro: %.4f %.4f %.4f rad/s  mag: %.1f %.1f %.1f µT  %.2f °C\n",
		s.AccelX, s.AccelY, s.AccelZ,
		s.GyroX, s.GyroY, s.GyroZ,
		s.MagX, s.MagY, s.MagZ,
		s.TempC)
}

// ExampleICM20948_Stream illustrates streaming captures via an hrtimer
// trigger. It is shown without an Output block because it requires the
// kernel iio-trig-hrtimer module and root to create the configfs entry.
func ExampleICM20948_Stream() {
	dev, _ := icm20948.Open()
	defer dev.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := dev.Stream(ctx, icm20948.StreamOptions{FrequencyHz: 100})
	for s := range ch {
		fmt.Printf("%v  accel: %.2f %.2f %.2f  gyro: %.4f %.4f %.4f  mag: %.1f %.1f %.1f  %.2f °C\n",
			s.Time, s.AccelX, s.AccelY, s.AccelZ,
			s.GyroX, s.GyroY, s.GyroZ,
			s.MagX, s.MagY, s.MagZ,
			s.TempC)
	}
}
