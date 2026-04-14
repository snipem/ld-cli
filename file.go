// Package ldparser reads and writes .ld race telemetry log files.
// .ld is the de facto standard format for sim racing telemetry logging across iRacing, ACC, LMU, and other platforms.
//
// The binary format is documented in the reference implementation at github.com/gotzl/ldparser.
// All multi-byte values are little-endian.
package ldparser

import (
	"time"
)

// Header sizes in bytes (must match the Python struct sizes exactly).
const (
	headSize    = 1762
	eventSize   = 1154
	venueSize   = 1100
	vehicleSize = 260
	chanSize    = 124
)

// LD file marker.
const ldMarker uint32 = 0x40

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// File represents a fully parsed .ld telemetry file.
type File struct {
	Header   Header
	Channels []Channel
}

// Header holds the top-level metadata of an .ld file.
type Header struct {
	MetaPtr      uint32
	DataPtr      uint32
	EventPtr     uint32
	Driver       string
	VehicleID    string
	Venue        string
	DateTime     time.Time
	ShortComment string
	Event        *Event
	NumChannels  uint32

	DeviceSerial  uint32
	DeviceType    string
	DeviceVersion uint16
}

// Event stores session/event metadata.
type Event struct {
	Name     string
	Session  string
	Comment  string
	VenuePtr uint16
	Venue    *Venue
}

// Venue stores track/location information.
type Venue struct {
	Name       string
	VehiclePtr uint16
	Vehicle    *Vehicle
}

// Vehicle stores car metadata.
type Vehicle struct {
	ID       string
	LongName string // offset 64: full car name used by LMU/rFactor2 (e.g. "BMW GT3 Custom Team 2025 #397")
	Weight   uint32
	Type     string
	Comment  string
}

// DataKind describes the underlying sample type.
type DataKind int

const (
	KindUnknown DataKind = iota
	KindInt16
	KindInt32
	KindFloat16
	KindFloat32
	KindFloat64
)

func (k DataKind) byteSize() int {
	switch k {
	case KindInt16, KindFloat16:
		return 2
	case KindInt32, KindFloat32:
		return 4
	case KindFloat64:
		return 8
	}
	return 0
}

// Channel holds the metadata and converted sample data for one telemetry channel.
type Channel struct {
	MetaPtr     uint32
	PrevMetaPtr uint32
	NextMetaPtr uint32
	DataPtr     uint32
	DataLen     uint32 // number of samples
	Kind        DataKind
	Freq        uint16
	Shift       int16
	Mul         int16
	Scale       int16
	Dec         int16
	Name        string
	ShortName   string
	Unit        string

	// Data holds the converted float64 samples (populated after Parse).
	Data []float64

	// raw dtype codes from the file, useful for writing back
	dtypeA uint16
	dtype  uint16
}

// Duration returns the time span of this channel based on sample count and frequency.
func (c *Channel) Duration() time.Duration {
	if c.Freq == 0 {
		return 0
	}
	return time.Duration(float64(c.DataLen)/float64(c.Freq)*1e9) * time.Nanosecond
}

// TimeAt returns the timestamp (relative to start) of sample index i.
func (c *Channel) TimeAt(i int) float64 {
	if c.Freq == 0 {
		return 0
	}
	return float64(i) / float64(c.Freq)
}

// ---------------------------------------------------------------------------
// Lap detection
// ---------------------------------------------------------------------------

// Lap represents a single lap extracted from telemetry data.
type Lap struct {
	Number    int
	StartIdx  int     // sample index in the beacon/lap channel
	EndIdx    int     // sample index (exclusive)
	StartTime float64 // seconds from session start
	EndTime   float64 // seconds from session start
	LapTime   float64 // seconds
}
