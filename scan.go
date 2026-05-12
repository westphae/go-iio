package iio

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ScanChannel is the parsed form of one entry in /sys/bus/iio/devices/
// iio:deviceN/scan_elements. It carries everything needed to decode the
// channel's bytes out of a buffered sample frame.
type ScanChannel struct {
	Name        string
	Signed      bool
	BigEndian   bool
	RealBits    int // significant data bits
	StorageBits int // total bits stored in memory (multiple of 8)
	Shift       int // right-shift after reading, per the IIO ABI
	Index       int // sort key for placing the channel within a frame
	ByteOffset  int // computed by BuildLayout
	Scale       float64
	Offset      float64
}

// ParseTypeString parses an IIO scan_elements/in_<name>_type string.
//
// Grammar: "[endian]:[s|u][realbits]/[storagebits][>>shift]"
// Examples:
//
//	"le:s32/32>>0"
//	"be:u16/16>>4"
//	"le:u32/32>>"    (trailing >> with no shift means 0)
func ParseTypeString(s string) (ScanChannel, error) {
	var ch ScanChannel
	s = strings.TrimSpace(s)
	colon := strings.IndexByte(s, ':')
	if colon < 0 {
		return ch, fmt.Errorf("iio: bad type string %q: missing ':'", s)
	}
	endian := s[:colon]
	switch endian {
	case "le":
		ch.BigEndian = false
	case "be":
		ch.BigEndian = true
	default:
		return ch, fmt.Errorf("iio: bad type string %q: endian %q", s, endian)
	}

	rest := s[colon+1:]
	if len(rest) == 0 {
		return ch, fmt.Errorf("iio: bad type string %q: empty body", s)
	}
	switch rest[0] {
	case 's', 'S':
		ch.Signed = true
	case 'u', 'U':
		ch.Signed = false
	default:
		return ch, fmt.Errorf("iio: bad type string %q: sign %q", s, rest[:1])
	}
	rest = rest[1:]

	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return ch, fmt.Errorf("iio: bad type string %q: missing '/'", s)
	}
	realStr := rest[:slash]
	storageRest := rest[slash+1:]

	n, err := strconv.Atoi(realStr)
	if err != nil {
		return ch, fmt.Errorf("iio: bad type string %q: realbits: %w", s, err)
	}
	ch.RealBits = n

	storeStr := storageRest
	shiftStr := ""
	if i := strings.Index(storageRest, ">>"); i >= 0 {
		storeStr = storageRest[:i]
		shiftStr = storageRest[i+2:]
	}
	n, err = strconv.Atoi(storeStr)
	if err != nil {
		return ch, fmt.Errorf("iio: bad type string %q: storagebits: %w", s, err)
	}
	ch.StorageBits = n
	if shiftStr != "" {
		n, err = strconv.Atoi(shiftStr)
		if err != nil {
			return ch, fmt.Errorf("iio: bad type string %q: shift: %w", s, err)
		}
		ch.Shift = n
	}

	if ch.StorageBits%8 != 0 || ch.StorageBits == 0 {
		return ch, fmt.Errorf("iio: bad type string %q: storagebits %d", s, ch.StorageBits)
	}
	return ch, nil
}

// BuildLayout takes a set of ScanChannels (one per enabled channel), sorts
// them by Index, and assigns ByteOffset to each honoring the IIO ABI's
// "natural alignment" rule: a channel of N bytes is aligned to N bytes. The
// returned ScanLayout's FrameBytes is the per-sample size including final
// padding to the largest channel's alignment.
//
// The Linux kernel's industrialio-buffer applies the same rule when laying
// out the ring; reproducing it here is what lets us decode frames without
// hardcoded knowledge per sensor.
func BuildLayout(chans []ScanChannel) ScanLayout {
	out := make([]ScanChannel, len(chans))
	copy(out, chans)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })

	maxAlign := 1
	offset := 0
	for i := range out {
		size := out[i].StorageBits / 8
		if size > maxAlign {
			maxAlign = size
		}
		if size > 0 {
			if r := offset % size; r != 0 {
				offset += size - r
			}
		}
		out[i].ByteOffset = offset
		offset += size
	}
	// Pad the frame to the largest alignment.
	if maxAlign > 0 {
		if r := offset % maxAlign; r != 0 {
			offset += maxAlign - r
		}
	}
	return ScanLayout{FrameBytes: offset, Channels: out}
}

// ScanLayout describes the byte layout of one sample frame in the buffered
// stream of a device, including final padding.
type ScanLayout struct {
	FrameBytes int
	Channels   []ScanChannel
}

// readScan extracts one channel's value from a sample frame as int64. Both
// signed and unsigned variants are returned in int64; the caller knows the
// channel's signedness from the ScanChannel.
func readScan(frame []byte, sc ScanChannel) int64 {
	off := sc.ByteOffset
	size := sc.StorageBits / 8
	if off+size > len(frame) {
		return 0
	}
	b := frame[off : off+size]
	var u uint64
	if sc.BigEndian {
		for i := 0; i < size; i++ {
			u = (u << 8) | uint64(b[i])
		}
	} else {
		switch size {
		case 8:
			u = binary.LittleEndian.Uint64(b)
		case 4:
			u = uint64(binary.LittleEndian.Uint32(b))
		case 2:
			u = uint64(binary.LittleEndian.Uint16(b))
		case 1:
			u = uint64(b[0])
		default:
			for i := size - 1; i >= 0; i-- {
				u = (u << 8) | uint64(b[i])
			}
		}
	}
	// Apply right-shift before sign-extending; per the IIO ABI the shift
	// strips alignment padding within storagebits, and sign extension is
	// based on the resulting realbits.
	u >>= sc.Shift
	bits := sc.RealBits
	if bits <= 0 || bits > 64 {
		bits = sc.StorageBits - sc.Shift
	}
	if sc.Signed {
		signBit := uint64(1) << (uint(bits) - 1)
		if u&signBit != 0 {
			// extend
			u |= ^((uint64(1) << uint(bits)) - 1)
		}
		return int64(u)
	}
	return int64(u)
}
