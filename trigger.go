package iio

import (
	"fmt"
	"os"
	"path/filepath"
)

// HRTrigger is a periodic hrtimer-driven IIO trigger that the library
// created via configfs. Bind one to a device with
// `Device.SetAttr("trigger/current_trigger", trig.Name())`, or pass its name
// in BufferOptions.Trigger.
type HRTrigger struct {
	name    string
	cfgPath string // /sys/kernel/config/iio/triggers/hrtimer/<name>
}

// hrtimerConfigRoot is the configfs path where hrtimer triggers are created.
// It exists once the iio-trig-hrtimer module is loaded. Callers should run
// `sudo modprobe iio-trig-hrtimer` if creation fails with ENOENT.
const hrtimerConfigRoot = "/sys/kernel/config/iio/triggers/hrtimer"

// EnsureHRTimer creates an hrtimer-driven IIO trigger with the given name and
// sampling frequency (Hz). Returns the trigger so the caller can bind it and
// Remove it on shutdown.
//
// Requires CAP_SYS_ADMIN (typically root) because configfs mkdir is
// privileged, and the iio-trig-hrtimer kernel module must be loaded.
func EnsureHRTimer(name string, freqHz int) (*HRTrigger, error) {
	if name == "" {
		return nil, fmt.Errorf("iio: EnsureHRTimer: name is required")
	}
	cfgPath := filepath.Join(hrtimerConfigRoot, name)
	if _, err := os.Stat(hrtimerConfigRoot); err != nil {
		return nil, fmt.Errorf("iio: hrtimer config root %s not present (modprobe iio-trig-hrtimer?): %w", hrtimerConfigRoot, err)
	}
	if err := os.Mkdir(cfgPath, 0); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("iio: create hrtimer %q: %w", name, err)
	}
	trig := &HRTrigger{name: name, cfgPath: cfgPath}
	if freqHz > 0 {
		if err := trig.SetFrequency(freqHz); err != nil {
			trig.Remove()
			return nil, err
		}
	}
	return trig, nil
}

// Name returns the trigger's kernel name (suitable for writing to
// trigger/current_trigger).
func (t *HRTrigger) Name() string { return t.name }

// SetFrequency writes the trigger's sampling_frequency attribute (Hz).
func (t *HRTrigger) SetFrequency(hz int) error {
	dir, err := t.sysfsDir()
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "sampling_frequency")
	if err := os.WriteFile(p, []byte(fmt.Sprintf("%d", hz)), 0); err != nil {
		return fmt.Errorf("iio: write %s: %w", p, err)
	}
	return nil
}

// Remove deletes the trigger from configfs. After this the trigger no longer
// appears under /sys/bus/iio/devices.
func (t *HRTrigger) Remove() error {
	if t.cfgPath == "" {
		return nil
	}
	if err := os.Remove(t.cfgPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("iio: remove hrtimer %s: %w", t.cfgPath, err)
	}
	t.cfgPath = ""
	return nil
}

// sysfsDir finds the /sys/bus/iio/devices/triggerN directory whose `name`
// matches t.name. The kernel assigns the N when the configfs entry is
// created, so we have to look it up.
func (t *HRTrigger) sysfsDir() (string, error) {
	matches, err := filepath.Glob("/sys/bus/iio/devices/trigger*")
	if err != nil {
		return "", err
	}
	for _, m := range matches {
		b, err := os.ReadFile(filepath.Join(m, "name"))
		if err != nil {
			continue
		}
		nm := string(b)
		// Strip trailing newline.
		for len(nm) > 0 && (nm[len(nm)-1] == '\n' || nm[len(nm)-1] == 0) {
			nm = nm[:len(nm)-1]
		}
		if nm == t.name {
			return m, nil
		}
	}
	return "", fmt.Errorf("iio: trigger %q not found in /sys/bus/iio/devices", t.name)
}
