package ldparser

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// ParseFile reads and parses an .ld file from disk.
func ParseFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse reads an .ld file from a byte slice.
func Parse(data []byte) (*File, error) {
	if len(data) < headSize {
		return nil, fmt.Errorf("file too small for header: %d bytes", len(data))
	}

	r := bytes.NewReader(data)

	hdr, err := parseHeader(r, data)
	if err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}

	channels, err := parseChannels(data, hdr.MetaPtr)
	if err != nil {
		return nil, fmt.Errorf("parse channels: %w", err)
	}

	return &File{Header: hdr, Channels: channels}, nil
}

func parseHeader(r io.ReadSeeker, raw []byte) (Header, error) {
	var h Header
	var buf [headSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return h, err
	}

	b := buf[:]
	le := binary.LittleEndian

	marker := le.Uint32(b[0:4])
	if marker != ldMarker {
		return h, fmt.Errorf("bad ld marker: 0x%x", marker)
	}
	// skip 4 padding
	h.MetaPtr = le.Uint32(b[8:12])
	h.DataPtr = le.Uint32(b[12:16])
	// skip 20
	h.EventPtr = le.Uint32(b[36:40])
	// skip 24 → offset 64
	// 3x uint16 unknown static
	h.DeviceSerial = le.Uint32(b[70:74])
	h.DeviceType = decodeString(b[74:82])
	h.DeviceVersion = le.Uint16(b[82:84])
	// skip H at 84
	h.NumChannels = le.Uint32(b[86:90])
	// skip 4 → offset 94
	dateStr := decodeString(b[94:110])
	// skip 16 → 126
	timeStr := decodeString(b[126:142])

	h.DateTime = parseDateTime(dateStr, timeStr)

	// skip 16 → 158
	h.Driver = decodeString(b[158:222])
	h.VehicleID = decodeString(b[222:286])
	// skip 64 → 350
	h.Venue = decodeString(b[350:414])
	// skip 64+1024+4+66 → 1572
	h.ShortComment = decodeString(b[1572:1636])

	// parse event chain
	if h.EventPtr > 0 && int(h.EventPtr)+eventSize <= len(raw) {
		evt, err := parseEvent(raw, h.EventPtr)
		if err == nil {
			h.Event = &evt
		}
	}

	return h, nil
}

func parseEvent(data []byte, offset uint32) (Event, error) {
	var e Event
	off := int(offset)
	if off+eventSize > len(data) {
		return e, fmt.Errorf("event out of bounds")
	}
	b := data[off : off+eventSize]
	e.Name = decodeString(b[0:64])
	e.Session = decodeString(b[64:128])
	e.Comment = decodeString(b[128:1152])
	e.VenuePtr = binary.LittleEndian.Uint16(b[1152:1154])

	if e.VenuePtr > 0 {
		v, err := parseVenue(data, uint32(e.VenuePtr))
		if err == nil {
			e.Venue = &v
		}
	}
	return e, nil
}

func parseVenue(data []byte, offset uint32) (Venue, error) {
	var v Venue
	off := int(offset)
	if off+venueSize > len(data) {
		return v, fmt.Errorf("venue out of bounds")
	}
	b := data[off : off+venueSize]
	v.Name = decodeString(b[0:64])
	// 1034 bytes padding
	v.VehiclePtr = binary.LittleEndian.Uint16(b[1098:1100])

	if v.VehiclePtr > 0 {
		veh, err := parseVehicle(data, uint32(v.VehiclePtr))
		if err == nil {
			v.Vehicle = &veh
		}
	}
	return v, nil
}

func parseVehicle(data []byte, offset uint32) (Vehicle, error) {
	var v Vehicle
	off := int(offset)
	if off+vehicleSize > len(data) {
		return v, fmt.Errorf("vehicle out of bounds")
	}
	b := data[off : off+vehicleSize]
	v.ID = decodeString(b[0:64])
	v.LongName = decodeString(b[64:192])
	v.Weight = binary.LittleEndian.Uint32(b[192:196])
	v.Type = decodeString(b[196:228])
	v.Comment = decodeString(b[228:260])
	return v, nil
}

func parseChannels(data []byte, metaPtr uint32) ([]Channel, error) {
	var channels []Channel
	ptr := metaPtr
	for ptr > 0 {
		ch, err := parseChannel(data, ptr)
		if err != nil {
			return channels, fmt.Errorf("channel at 0x%x: %w", ptr, err)
		}
		channels = append(channels, ch)
		ptr = ch.NextMetaPtr
	}
	return channels, nil
}

func parseChannel(data []byte, metaPtr uint32) (Channel, error) {
	var ch Channel
	off := int(metaPtr)
	if off+chanSize > len(data) {
		return ch, fmt.Errorf("channel meta out of bounds at %d", off)
	}
	b := data[off : off+chanSize]
	le := binary.LittleEndian

	ch.MetaPtr = metaPtr
	ch.PrevMetaPtr = le.Uint32(b[0:4])
	ch.NextMetaPtr = le.Uint32(b[4:8])
	ch.DataPtr = le.Uint32(b[8:12])
	ch.DataLen = le.Uint32(b[12:16])
	// skip uint16 counter at 16
	ch.dtypeA = le.Uint16(b[18:20])
	ch.dtype = le.Uint16(b[20:22])
	ch.Freq = le.Uint16(b[22:24])
	ch.Shift = int16(le.Uint16(b[24:26]))
	ch.Mul = int16(le.Uint16(b[26:28]))
	ch.Scale = int16(le.Uint16(b[28:30]))
	ch.Dec = int16(le.Uint16(b[30:32]))
	ch.Name = decodeString(b[32:64])
	ch.ShortName = decodeString(b[64:72])
	ch.Unit = decodeString(b[72:84])

	ch.Kind = resolveKind(ch.dtypeA, ch.dtype)

	// Read sample data
	if ch.Kind != KindUnknown && ch.DataLen > 0 {
		bsize := ch.Kind.byteSize()
		start := int(ch.DataPtr)
		end := start + int(ch.DataLen)*bsize
		if end > len(data) {
			return ch, fmt.Errorf("channel %q data out of bounds", ch.Name)
		}
		ch.Data = readSamples(data[start:end], ch)
	}

	return ch, nil
}

func resolveKind(dtypeA, dtype uint16) DataKind {
	switch {
	case dtypeA == 0x07:
		switch dtype {
		case 2:
			return KindFloat16
		case 4:
			return KindFloat32
		}
	case dtypeA == 0 || dtypeA == 0x03 || dtypeA == 0x05:
		switch dtype {
		case 2:
			return KindInt16
		case 4:
			return KindInt32
		}
	case dtypeA == 0x08 && dtype == 0x08:
		return KindFloat64
	}
	return KindUnknown
}

func readSamples(raw []byte, ch Channel) []float64 {
	n := int(ch.DataLen)
	out := make([]float64, n)
	le := binary.LittleEndian

	scale := float64(ch.Scale)
	dec := float64(ch.Dec)
	shift := float64(ch.Shift)
	mul := float64(ch.Mul)
	pow10 := math.Pow(10.0, -dec)

	for i := 0; i < n; i++ {
		var v float64
		switch ch.Kind {
		case KindInt16:
			v = float64(int16(le.Uint16(raw[i*2 : i*2+2])))
		case KindInt32:
			v = float64(int32(le.Uint32(raw[i*4 : i*4+4])))
		case KindFloat16:
			v = float64fromFloat16(le.Uint16(raw[i*2 : i*2+2]))
		case KindFloat32:
			v = float64(math.Float32frombits(le.Uint32(raw[i*4 : i*4+4])))
		case KindFloat64:
			v = math.Float64frombits(le.Uint64(raw[i*8 : i*8+8]))
		}
		// Apply conversion: raw/scale * 10^(-dec) * mul + shift
		out[i] = v/scale*pow10*mul + shift
	}
	return out
}

// float16 (IEEE 754 half precision) to float64.
func float64fromFloat16(bits uint16) float64 {
	sign := uint32(bits>>15) & 1
	exp := uint32(bits>>10) & 0x1f
	frac := uint32(bits) & 0x3ff

	switch {
	case exp == 0:
		if frac == 0 {
			return math.Float64frombits(uint64(sign) << 63)
		}
		// subnormal
		e := float64(-14)
		m := float64(frac) / 1024.0
		v := math.Ldexp(m, int(e))
		if sign == 1 {
			v = -v
		}
		return v
	case exp == 0x1f:
		if frac == 0 {
			if sign == 1 {
				return math.Inf(-1)
			}
			return math.Inf(1)
		}
		return math.NaN()
	default:
		// convert to float32 bits and use standard conversion
		f32bits := (sign << 31) | ((exp-15+127)<<23) | (frac << 13)
		return float64(math.Float32frombits(f32bits))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func decodeString(b []byte) string {
	// Trim null bytes and whitespace
	n := bytes.IndexByte(b, 0)
	if n >= 0 {
		b = b[:n]
	}
	return strings.TrimSpace(string(b))
}

func parseDateTime(dateStr, timeStr string) time.Time {
	combined := dateStr + " " + timeStr
	layouts := []string{
		"02/01/2006 15:04:05",
		"02/01/2006 15:04",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, combined); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// Channel lookup helpers
// ---------------------------------------------------------------------------

// ChannelByName returns the first channel matching the given name (case-insensitive).
func (f *File) ChannelByName(name string) *Channel {
	lower := strings.ToLower(name)
	for i := range f.Channels {
		if strings.ToLower(f.Channels[i].Name) == lower {
			return &f.Channels[i]
		}
	}
	return nil
}

// ChannelNames returns a list of all channel names.
func (f *File) ChannelNames() []string {
	names := make([]string, len(f.Channels))
	for i, c := range f.Channels {
		names[i] = c.Name
	}
	return names
}

// ---------------------------------------------------------------------------
// Lap detection
// ---------------------------------------------------------------------------

// DetectLaps finds lap boundaries using multiple strategies:
//  1. "Lap Number" + "Lap Time" channels (most reliable, standard across formats)
//  2. "Lap Distance" resets (fallback)
//
// The returned laps include the outlap (lap 0) and inlap (last segment).
// LapTime is taken from the "Lap Time" channel when available, otherwise
// computed from wall-clock sample timestamps.
func (f *File) DetectLaps() []Lap {
	// Strategy 1: Use "Lap Number" channel — the gold standard.
	lapNumCh := f.findChannel("Lap Number", "LapNumber", "lap number")
	if lapNumCh != nil && lapNumCh.Freq > 0 && len(lapNumCh.Data) > 0 {
		lapTimeCh := f.findChannel("Lap Time", "LapTime", "lap time")
		return lapsFromLapNumber(lapNumCh, lapTimeCh)
	}

	// Strategy 2: Lap distance resets.
	lapDistCh := f.findChannel("Lap Distance", "LapDistance", "lapdistance", "Lap_Distance")
	if lapDistCh != nil && lapDistCh.Freq > 0 && len(lapDistCh.Data) > 2 {
		return lapsFromDistanceReset(lapDistCh)
	}

	return nil
}

func (f *File) findChannel(names ...string) *Channel {
	for _, name := range names {
		if ch := f.ChannelByName(name); ch != nil {
			return ch
		}
	}
	return nil
}

func lapsFromLapNumber(lapNumCh, lapTimeCh *Channel) []Lap {
	// Find boundaries where lap number changes.
	type boundary struct {
		idx    int
		lapNum int
	}
	var boundaries []boundary
	boundaries = append(boundaries, boundary{0, int(lapNumCh.Data[0])})
	prev := lapNumCh.Data[0]
	for i := 1; i < len(lapNumCh.Data); i++ {
		if lapNumCh.Data[i] != prev {
			boundaries = append(boundaries, boundary{i, int(lapNumCh.Data[i])})
			prev = lapNumCh.Data[i]
		}
	}
	// Add session end as final boundary.
	boundaries = append(boundaries, boundary{len(lapNumCh.Data), -1})

	laps := make([]Lap, 0, len(boundaries)-1)
	for i := 0; i < len(boundaries)-1; i++ {
		s := boundaries[i]
		e := boundaries[i+1]
		st := lapNumCh.TimeAt(s.idx)
		et := lapNumCh.TimeAt(e.idx)
		wallTime := et - st

		// AC/Telemetrick: "Lap Time" is a running timer that resets at
		// each lap boundary. The lap time = last value - first value
		// within the segment. LMU has no "Lap Time" channel (uses
		// "Last Laptime" instead), so this path only runs for AC.
		lapTime := wallTime
		if lapTimeCh != nil && len(lapTimeCh.Data) > 0 {
			startIdx := min(s.idx, len(lapTimeCh.Data)-1)
			endIdx := min(e.idx-1, len(lapTimeCh.Data)-1)
			if endIdx > startIdx {
				lt := lapTimeCh.Data[endIdx] - lapTimeCh.Data[startIdx]
				if lt > 0 {
					lapTime = lt
				}
			}
		}

		laps = append(laps, Lap{
			Number:    s.lapNum,
			StartIdx:  s.idx,
			EndIdx:    e.idx,
			StartTime: st,
			EndTime:   et,
			LapTime:   lapTime,
		})
	}
	return laps
}

func lapsFromDistanceReset(ch *Channel) []Lap {
	var crossings []int
	crossings = append(crossings, 0)
	for i := 1; i < len(ch.Data); i++ {
		if ch.Data[i] < ch.Data[i-1]*0.5 && ch.Data[i-1] > 100 {
			crossings = append(crossings, i)
		}
	}
	crossings = append(crossings, len(ch.Data))

	laps := make([]Lap, 0, len(crossings)-1)
	for i := 0; i < len(crossings)-1; i++ {
		s, e := crossings[i], crossings[i+1]
		st := ch.TimeAt(s)
		et := ch.TimeAt(e)
		laps = append(laps, Lap{
			Number:    i,
			StartIdx:  s,
			EndIdx:    e,
			StartTime: st,
			EndTime:   et,
			LapTime:   et - st,
		})
	}
	return laps
}

// ---------------------------------------------------------------------------
// String representation
// ---------------------------------------------------------------------------

func (h Header) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Driver:    %s\n", h.Driver)
	fmt.Fprintf(&sb, "Vehicle:   %s\n", h.VehicleID)
	fmt.Fprintf(&sb, "Venue:     %s\n", h.Venue)
	fmt.Fprintf(&sb, "DateTime:  %s\n", h.DateTime.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&sb, "Comment:   %s\n", h.ShortComment)
	if h.Event != nil {
		fmt.Fprintf(&sb, "Event:     %s\n", h.Event.Name)
		fmt.Fprintf(&sb, "Session:   %s\n", h.Event.Session)
		if h.Event.Venue != nil {
			fmt.Fprintf(&sb, "Track:     %s\n", h.Event.Venue.Name)
			if h.Event.Venue.Vehicle != nil {
				fmt.Fprintf(&sb, "Car:       %s\n", h.Event.Venue.Vehicle.ID)
			}
		}
	}
	return sb.String()
}
