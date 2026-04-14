package ldparser

import (
	"bytes"
	"fmt"
	"os"

	"github.com/mail/go-ldparser/internal/ldx"
)

// LDX represents the .ldx index file structure for race telemetry logs.
type LDX struct {
	Locale        string
	DefaultLocale string
	Version       string
	Layers        LDXLayers
}

// LDXLayers contains the detail entries.
type LDXLayers struct {
	Details LDXDetails
}

// LDXDetails holds both string and numeric metadata entries.
type LDXDetails struct {
	Strings  []LDXString
	Numerics []LDXNumeric
}

// LDXString is a single string key-value metadata entry.
type LDXString struct {
	ID    string
	Value string
}

// LDXNumeric is a numeric metadata entry (commonly used for setup data).
type LDXNumeric struct {
	ID    string
	Value string
	Unit  string
	DPS   string // decimal places
}

// ParseLDXFile reads and parses an .ldx file.
func ParseLDXFile(path string) (*LDX, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	xldx, err := ldx.ReadXML(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return fromInternalXML(xldx), nil
}

// GenerateLDX creates an LDX index file from a parsed File and its detected laps.
func GenerateLDX(f *File, laps []Lap) *LDX {
	ldx := &LDX{
		Locale:        "English_United States.1252",
		DefaultLocale: "C",
		Version:       "1.6",
	}

	totalLaps := len(laps)
	var fastestTime float64
	var fastestLap int

	for i, lap := range laps {
		if lap.LapTime > 0 && (fastestTime == 0 || lap.LapTime < fastestTime) {
			fastestTime = lap.LapTime
			fastestLap = i
		}
	}

	ldx.Layers.Details.Strings = []LDXString{
		{ID: "Driver", Value: f.Header.Driver},
		{ID: "Vehicle", Value: f.Header.VehicleID},
		{ID: "Venue", Value: f.Header.Venue},
		{ID: "Total Laps", Value: fmt.Sprintf("%d", totalLaps)},
		{ID: "Fastest Time", Value: formatLapTime(fastestTime)},
		{ID: "Fastest Lap", Value: fmt.Sprintf("%d", fastestLap)},
	}

	return ldx
}

// WriteLDX writes an LDX struct to disk.
func WriteLDX(l *LDX, path string) error {
	xldx := toInternalXML(l)
	var buf bytes.Buffer
	if err := ldx.WriteXML(&buf, xldx); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func formatLapTime(seconds float64) string {
	if seconds <= 0 {
		return "0:00.000"
	}
	mins := int(seconds) / 60
	secs := seconds - float64(mins*60)
	return fmt.Sprintf("%d:%06.3f", mins, secs)
}

// ---------------------------------------------------------------------------
// Conversion functions between public and internal XML types
// ---------------------------------------------------------------------------

func toInternalXML(l *LDX) *ldx.XMLFile {
	x := &ldx.XMLFile{
		Locale:        l.Locale,
		DefaultLocale: l.DefaultLocale,
		Version:       l.Version,
	}
	for _, s := range l.Layers.Details.Strings {
		x.Layers.Details.Strings = append(x.Layers.Details.Strings, ldx.XMLString{
			ID:    s.ID,
			Value: s.Value,
		})
	}
	for _, n := range l.Layers.Details.Numerics {
		x.Layers.Details.Numerics = append(x.Layers.Details.Numerics, ldx.XMLNumeric{
			ID:    n.ID,
			Value: n.Value,
			Unit:  n.Unit,
			DPS:   n.DPS,
		})
	}
	return x
}

func fromInternalXML(x *ldx.XMLFile) *LDX {
	l := &LDX{
		Locale:        x.Locale,
		DefaultLocale: x.DefaultLocale,
		Version:       x.Version,
	}
	for _, s := range x.Layers.Details.Strings {
		l.Layers.Details.Strings = append(l.Layers.Details.Strings, LDXString{
			ID:    s.ID,
			Value: s.Value,
		})
	}
	for _, n := range x.Layers.Details.Numerics {
		l.Layers.Details.Numerics = append(l.Layers.Details.Numerics, LDXNumeric{
			ID:    n.ID,
			Value: n.Value,
			Unit:  n.Unit,
			DPS:   n.DPS,
		})
	}
	return l
}
