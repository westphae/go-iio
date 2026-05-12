package iio

import (
	"encoding/binary"
	"testing"
)

func TestParseTypeString(t *testing.T) {
	cases := []struct {
		in   string
		want ScanChannel
	}{
		{"le:s32/32>>0", ScanChannel{Signed: true, RealBits: 32, StorageBits: 32}},
		{"le:u32/32>>0", ScanChannel{Signed: false, RealBits: 32, StorageBits: 32}},
		{"le:s64/64>>0", ScanChannel{Signed: true, RealBits: 64, StorageBits: 64}},
		{"be:u16/16>>4", ScanChannel{Signed: false, BigEndian: true, RealBits: 16, StorageBits: 16, Shift: 4}},
		{"le:s16/16>>0", ScanChannel{Signed: true, RealBits: 16, StorageBits: 16}},
		{"le:u32/32>>", ScanChannel{Signed: false, RealBits: 32, StorageBits: 32}},
	}
	for _, c := range cases {
		got, err := ParseTypeString(c.in)
		if err != nil {
			t.Errorf("ParseTypeString(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseTypeString(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseTypeStringErrors(t *testing.T) {
	for _, s := range []string{"", "le", "le:", "le:x32/32", "be:s32/notnum", "be:s32"} {
		if _, err := ParseTypeString(s); err == nil {
			t.Errorf("ParseTypeString(%q) expected error, got nil", s)
		}
	}
}

// TestBuildLayoutBMP280 covers the real BMP280 case: pressure idx 0 (u32),
// temp idx 1 (s32), timestamp idx 2 (s64). The kernel aligns the 8-byte
// timestamp to an 8-byte boundary, which here lands at offset 8 with no
// padding (4+4=8).
func TestBuildLayoutBMP280(t *testing.T) {
	in := []ScanChannel{
		{Name: "pressure", Index: 0, RealBits: 32, StorageBits: 32},
		{Name: "temp", Index: 1, Signed: true, RealBits: 32, StorageBits: 32},
		{Name: "timestamp", Index: 2, Signed: true, RealBits: 64, StorageBits: 64},
	}
	l := BuildLayout(in)
	if l.FrameBytes != 16 {
		t.Errorf("FrameBytes = %d, want 16", l.FrameBytes)
	}
	got := map[string]int{}
	for _, c := range l.Channels {
		got[c.Name] = c.ByteOffset
	}
	for name, want := range map[string]int{"pressure": 0, "temp": 4, "timestamp": 8} {
		if got[name] != want {
			t.Errorf("offset[%s] = %d, want %d", name, got[name], want)
		}
	}
}

// TestBuildLayoutWithPadding exercises the alignment rule: if only pressure
// (u32, idx 0) and timestamp (s64, idx 2) are enabled, the timestamp must
// still start at an 8-byte boundary, which means 4 bytes of padding after
// pressure. Frame total is padded to 16 (the largest storage size).
func TestBuildLayoutWithPadding(t *testing.T) {
	in := []ScanChannel{
		{Name: "pressure", Index: 0, RealBits: 32, StorageBits: 32},
		{Name: "timestamp", Index: 2, Signed: true, RealBits: 64, StorageBits: 64},
	}
	l := BuildLayout(in)
	if l.FrameBytes != 16 {
		t.Fatalf("FrameBytes = %d, want 16", l.FrameBytes)
	}
	for _, c := range l.Channels {
		switch c.Name {
		case "pressure":
			if c.ByteOffset != 0 {
				t.Errorf("pressure offset = %d, want 0", c.ByteOffset)
			}
		case "timestamp":
			if c.ByteOffset != 8 {
				t.Errorf("timestamp offset = %d, want 8 (8-byte alignment)", c.ByteOffset)
			}
		}
	}
}

// TestReadScanBMP280Frame decodes the exact 16-byte frame we observed on the
// live device:
//
//	pressure u32 LE at 0:  bd c6 8b 01 = 0x018BC6BD = 25,937,597
//	temp     s32 LE at 4:  4a 0f 00 00 = 3914
//	ts       s64 LE at 8:  d3 3c a9 19 15 d1 ae 18 = 0x18AED11519A93CD3
func TestReadScanBMP280Frame(t *testing.T) {
	frame := []byte{
		0xbd, 0xc6, 0x8b, 0x01,
		0x4a, 0x0f, 0x00, 0x00,
		0xd3, 0x3c, 0xa9, 0x19, 0x15, 0xd1, 0xae, 0x18,
	}
	press := ScanChannel{Name: "pressure", Index: 0, RealBits: 32, StorageBits: 32}
	temp := ScanChannel{Name: "temp", Index: 1, Signed: true, RealBits: 32, StorageBits: 32}
	ts := ScanChannel{Name: "timestamp", Index: 2, Signed: true, RealBits: 64, StorageBits: 64}
	l := BuildLayout([]ScanChannel{press, temp, ts})
	want := map[string]int64{
		"pressure":  25937597,
		"temp":      3914,
		"timestamp": int64(binary.LittleEndian.Uint64(frame[8:16])),
	}
	for _, c := range l.Channels {
		got := readScan(frame, c)
		if got != want[c.Name] {
			t.Errorf("readScan(%s) = %d, want %d", c.Name, got, want[c.Name])
		}
	}
}
