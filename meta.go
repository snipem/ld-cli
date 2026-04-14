package ldparser

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseMetaFile opens path and calls ParseMeta. Only the file header and
// the two or three channels needed for lap detection are read from disk —
// all other channel sample data is skipped.
//
// The returned File has every channel's metadata (Name, Unit, Freq, …) but
// Data is nil for channels that are not used for lap detection.
// Call DetectLaps() on the result exactly as you would after ParseFile.
func ParseMetaFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseMeta(f)
}

// ParseMeta reads only the bytes it needs from r to build the file header,
// full channel catalogue (metadata only), and the lap-detection channels.
// r must support seeking (os.File, bytes.Reader, etc.).
//
// Typical I/O for a 45 MB / 245-channel file:
//   - Full parse:  45 MB read
//   - ParseMeta:   ~35 KB read  (header + event chain + channel metas + 2 channels)
func ParseMeta(r io.ReadSeeker) (*File, error) {
	// ── 1. Header (1762 bytes at offset 0) ──────────────────────────────────
	var hdrBuf [headSize]byte
	if _, err := io.ReadFull(r, hdrBuf[:]); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	b := hdrBuf[:]
	le := binary.LittleEndian

	if le.Uint32(b[0:4]) != ldMarker {
		return nil, fmt.Errorf("bad ld marker: 0x%x", le.Uint32(b[0:4]))
	}

	var hdr Header
	hdr.MetaPtr = le.Uint32(b[8:12])
	hdr.DataPtr = le.Uint32(b[12:16])
	hdr.EventPtr = le.Uint32(b[36:40])
	hdr.DeviceSerial = le.Uint32(b[70:74])
	hdr.DeviceType = decodeString(b[74:82])
	hdr.DeviceVersion = le.Uint16(b[82:84])
	hdr.NumChannels = le.Uint32(b[86:90])
	hdr.DateTime = parseDateTime(decodeString(b[94:110]), decodeString(b[126:142]))
	hdr.Driver = decodeString(b[158:222])
	hdr.VehicleID = decodeString(b[222:286])
	hdr.Venue = decodeString(b[350:414])
	hdr.ShortComment = decodeString(b[1572:1636])

	// ── 2. Event → Venue → Vehicle chain ────────────────────────────────────
	if hdr.EventPtr > 0 {
		evt, err := readEvent(r, int64(hdr.EventPtr))
		if err == nil {
			hdr.Event = &evt
		}
	}

	// ── 3. Channel metadata linked list (no sample data) ────────────────────
	channels, lapIdxs, err := readChannelMetas(r, hdr.MetaPtr)
	if err != nil {
		return nil, fmt.Errorf("channel metas: %w", err)
	}

	// ── 4. Sample data for lap-detection channels only ───────────────────────
	for _, i := range lapIdxs {
		ch := &channels[i]
		if ch.Kind == KindUnknown || ch.DataLen == 0 {
			continue
		}
		bsize := ch.Kind.byteSize()
		raw := make([]byte, int(ch.DataLen)*bsize)
		if _, err := r.Seek(int64(ch.DataPtr), io.SeekStart); err != nil {
			continue // best-effort; lap detection degrades gracefully
		}
		if _, err := io.ReadFull(r, raw); err != nil {
			continue
		}
		ch.Data = readSamples(raw, *ch)
	}

	return &File{Header: hdr, Channels: channels}, nil
}

// readEvent seeks to offset and reads the event block plus its venue/vehicle chain.
func readEvent(r io.ReadSeeker, offset int64) (Event, error) {
	var e Event
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return e, err
	}
	var buf [eventSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return e, err
	}
	b := buf[:]
	e.Name = decodeString(b[0:64])
	e.Session = decodeString(b[64:128])
	e.Comment = decodeString(b[128:1152])
	e.VenuePtr = binary.LittleEndian.Uint16(b[1152:1154])

	if e.VenuePtr > 0 {
		v, err := readVenue(r, int64(e.VenuePtr))
		if err == nil {
			e.Venue = &v
		}
	}
	return e, nil
}

func readVenue(r io.ReadSeeker, offset int64) (Venue, error) {
	var v Venue
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return v, err
	}
	var buf [venueSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return v, err
	}
	b := buf[:]
	v.Name = decodeString(b[0:64])
	v.VehiclePtr = binary.LittleEndian.Uint16(b[1098:1100])

	if v.VehiclePtr > 0 {
		veh, err := readVehicle(r, int64(v.VehiclePtr))
		if err == nil {
			v.Vehicle = &veh
		}
	}
	return v, nil
}

func readVehicle(r io.ReadSeeker, offset int64) (Vehicle, error) {
	var v Vehicle
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return v, err
	}
	var buf [vehicleSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return v, err
	}
	b := buf[:]
	v.ID = decodeString(b[0:64])
	v.LongName = decodeString(b[64:192])
	v.Weight = binary.LittleEndian.Uint32(b[192:196])
	v.Type = decodeString(b[196:228])
	v.Comment = decodeString(b[228:260])
	return v, nil
}

// lapChannelNames are the channel names ParseMeta will load sample data for.
var lapChannelNames = []string{
	"lap number", "lapnumber",
	"lap time", "laptime",
	"lap distance", "lapdistance", "lap_distance",
}

func isLapChannel(name string) bool {
	lower := strings.ToLower(name)
	for _, n := range lapChannelNames {
		if lower == n {
			return true
		}
	}
	return false
}

// readChannelMetas walks the linked list starting at metaPtr, reading only
// the 124-byte metadata block for each channel. Returns the slice of channels
// and the indices of those needed for lap detection.
func readChannelMetas(r io.ReadSeeker, metaPtr uint32) ([]Channel, []int, error) {
	var channels []Channel
	var lapIdxs []int
	var buf [chanSize]byte
	le := binary.LittleEndian

	ptr := metaPtr
	for ptr > 0 {
		if _, err := r.Seek(int64(ptr), io.SeekStart); err != nil {
			return channels, lapIdxs, err
		}
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return channels, lapIdxs, err
		}
		b := buf[:]

		var ch Channel
		ch.MetaPtr = ptr
		ch.PrevMetaPtr = le.Uint32(b[0:4])
		ch.NextMetaPtr = le.Uint32(b[4:8])
		ch.DataPtr = le.Uint32(b[8:12])
		ch.DataLen = le.Uint32(b[12:16])
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

		if isLapChannel(ch.Name) {
			lapIdxs = append(lapIdxs, len(channels))
		}
		channels = append(channels, ch)
		ptr = ch.NextMetaPtr
	}
	return channels, lapIdxs, nil
}
