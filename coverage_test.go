package ldparser

import (
	"math"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// float64fromFloat16
// ---------------------------------------------------------------------------

func TestFloat64FromFloat16(t *testing.T) {
	tests := []struct {
		name string
		bits uint16
		want float64
		nan  bool
		inf  int // +1, -1, or 0
	}{
		{"positive zero", 0x0000, 0, false, 0},
		{"negative zero", 0x8000, 0, false, 0},
		{"one", 0x3C00, 1.0, false, 0},
		{"two", 0x4000, 2.0, false, 0},
		{"negative one", 0xBC00, -1.0, false, 0},
		{"positive inf", 0x7C00, 0, false, +1},
		{"negative inf", 0xFC00, 0, false, -1},
		{"nan", 0x7E00, 0, true, 0},
		// smallest normal: exp=1, frac=0 → 2^(1-15) * 1.0 = 2^-14
		{"smallest normal", 0x0400, math.Ldexp(1.0, -14), false, 0},
		// smallest denormal: exp=0, frac=1 → 2^-14 * (1/1024)
		{"smallest denormal", 0x0001, math.Ldexp(1.0/1024, -14), false, 0},
		// largest denormal: exp=0, frac=0x3FF → 2^-14 * (1023/1024)
		{"largest denormal", 0x03FF, math.Ldexp(1023.0/1024.0, -14), false, 0},
		// negative denormal
		{"negative denormal", 0x8001, -math.Ldexp(1.0/1024, -14), false, 0},
		// 0.5: exp=14, frac=0 → 2^(14-15) = 0.5
		{"half", 0x3800, 0.5, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := float64fromFloat16(tt.bits)
			if tt.nan {
				if !math.IsNaN(got) {
					t.Errorf("expected NaN, got %v", got)
				}
				return
			}
			if tt.inf != 0 {
				if !math.IsInf(got, tt.inf) {
					t.Errorf("expected Inf(%d), got %v", tt.inf, got)
				}
				return
			}
			if math.Abs(got-tt.want) > 1e-10 {
				t.Errorf("bits=0x%04X: got %v, want %v (diff %e)", tt.bits, got, tt.want, got-tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// resolveKind — cover all branches
// ---------------------------------------------------------------------------

func TestResolveKind(t *testing.T) {
	tests := []struct {
		dtypeA, dtype uint16
		want          DataKind
	}{
		{0x07, 2, KindFloat16},
		{0x07, 4, KindFloat32},
		{0x07, 99, KindUnknown}, // float dtype_a, unknown dtype
		{0x00, 2, KindInt16},
		{0x00, 4, KindInt32},
		{0x03, 2, KindInt16},
		{0x03, 4, KindInt32},
		{0x05, 2, KindInt16},
		{0x05, 4, KindInt32},
		{0x00, 99, KindUnknown}, // int dtype_a, unknown dtype
		{0x08, 0x08, KindFloat64},
		{0x08, 0x04, KindUnknown}, // float64 dtype_a, wrong dtype
		{0xFF, 0xFF, KindUnknown}, // completely unknown
	}
	for _, tt := range tests {
		got := resolveKind(tt.dtypeA, tt.dtype)
		if got != tt.want {
			t.Errorf("resolveKind(0x%02X, 0x%02X) = %v, want %v", tt.dtypeA, tt.dtype, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// DataKind.byteSize
// ---------------------------------------------------------------------------

func TestByteSize(t *testing.T) {
	tests := []struct{ k DataKind; want int }{
		{KindInt16, 2}, {KindFloat16, 2},
		{KindInt32, 4}, {KindFloat32, 4},
		{KindFloat64, 8},
		{KindUnknown, 0},
	}
	for _, tt := range tests {
		if got := tt.k.byteSize(); got != tt.want {
			t.Errorf("byteSize(%v) = %d, want %d", tt.k, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ChannelNames
// ---------------------------------------------------------------------------

func TestChannelNames(t *testing.T) {
	f, err := ParseFile("testdata/ac-tatuusfa1-spa.ld")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	names := f.ChannelNames()
	if len(names) != len(f.Channels) {
		t.Errorf("len(names)=%d, len(channels)=%d", len(names), len(f.Channels))
	}
	for i, n := range names {
		if n != f.Channels[i].Name {
			t.Errorf("names[%d]=%q, want %q", i, n, f.Channels[i].Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Channel.Duration and Channel.TimeAt
// ---------------------------------------------------------------------------

func TestChannelDuration(t *testing.T) {
	f, _ := ParseFile("testdata/ac-tatuusfa1-spa.ld")
	for _, ch := range f.Channels {
		d := ch.Duration()
		if ch.Freq > 0 && d.Seconds() <= 0 {
			t.Errorf("channel %q: unexpected duration %v", ch.Name, d)
		}
	}
	// Zero freq channel
	var zeroCh Channel
	if zeroCh.Duration() != 0 {
		t.Error("expected 0 duration for zero-freq channel")
	}
	if zeroCh.TimeAt(5) != 0 {
		t.Error("expected 0 TimeAt for zero-freq channel")
	}
}

// ---------------------------------------------------------------------------
// Header.String
// ---------------------------------------------------------------------------

func TestHeaderString(t *testing.T) {
	f, err := ParseFile("testdata/ac-tatuusfa1-spa.ld")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := f.Header.String()
	if !strings.Contains(s, "Driver:") {
		t.Errorf("String() missing 'Driver:' field: %q", s)
	}
	if !strings.Contains(s, "Venue:") {
		t.Errorf("String() missing 'Venue:' field: %q", s)
	}
}

// ---------------------------------------------------------------------------
// GenerateLDX, WriteLDX, formatLapTime
// ---------------------------------------------------------------------------

func TestFormatLapTime(t *testing.T) {
	tests := []struct{ s float64; want string }{
		{0, "0:00.000"},
		{-1, "0:00.000"},
		{63.68, "1:03.680"},
		{3661.5, "61:01.500"},
		{59.999, "0:59.999"},
	}
	for _, tt := range tests {
		got := formatLapTime(tt.s)
		if got != tt.want {
			t.Errorf("formatLapTime(%v) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestGenerateAndWriteLDX(t *testing.T) {
	f, err := ParseFile("testdata/ac-tatuusfa1-spa.ld")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	laps := f.DetectLaps()

	ldx := GenerateLDX(f, laps)
	if ldx == nil {
		t.Fatal("GenerateLDX returned nil")
	}
	if ldx.Version == "" {
		t.Error("expected version")
	}
	if len(ldx.Layers.Details.Strings) == 0 {
		t.Error("expected string entries")
	}
	// Check "Total Laps" entry
	found := false
	for _, s := range ldx.Layers.Details.Strings {
		if s.ID == "Total Laps" {
			found = true
		}
	}
	if !found {
		t.Error("missing 'Total Laps' entry")
	}

	// Write to temp file and read back
	tmp, err := os.CreateTemp("", "test_*.ldx")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	if err := WriteLDX(ldx, tmp.Name()); err != nil {
		t.Fatalf("WriteLDX: %v", err)
	}

	ldx2, err := ParseLDXFile(tmp.Name())
	if err != nil {
		t.Fatalf("ParseLDXFile roundtrip: %v", err)
	}
	if ldx2.Version != ldx.Version {
		t.Errorf("version mismatch: %q vs %q", ldx2.Version, ldx.Version)
	}
}

func TestGenerateLDXNoLaps(t *testing.T) {
	f, _ := ParseFile("testdata/ac-tatuusfa1-spa.ld")
	ldx := GenerateLDX(f, nil)
	if ldx == nil {
		t.Fatal("GenerateLDX(nil laps) returned nil")
	}
}

// ---------------------------------------------------------------------------
// lapsFromDistanceReset — synthetic file
// ---------------------------------------------------------------------------

func TestLapsFromDistanceReset(t *testing.T) {
	// Build a synthetic file with a "Lap Distance" channel that resets twice.
	// 3 laps: 0→1000, 0→1000, 0→500
	const freq = 10
	const lapSamples = 100
	data := make([]float64, lapSamples*3)
	for lap := 0; lap < 3; lap++ {
		for i := 0; i < lapSamples; i++ {
			if lap < 2 {
				data[lap*lapSamples+i] = float64(i) * (1000.0 / float64(lapSamples-1))
			} else {
				data[lap*lapSamples+i] = float64(i) * (500.0 / float64(lapSamples-1))
			}
		}
	}

	f := &File{
		Header: Header{NumChannels: 1},
		Channels: []Channel{
			{
				Name:    "Lap Distance",
				Unit:    "m",
				Freq:    freq,
				DataLen: uint32(len(data)),
				Data:    data,
			},
		},
	}

	laps := f.DetectLaps()
	if len(laps) != 3 {
		t.Errorf("expected 3 laps, got %d", len(laps))
	}
	for i, l := range laps {
		if l.Number != i {
			t.Errorf("lap[%d].Number = %d, want %d", i, l.Number, i)
		}
		if l.LapTime <= 0 {
			t.Errorf("lap[%d].LapTime = %v, want > 0", i, l.LapTime)
		}
	}
}

// ---------------------------------------------------------------------------
// DetectLaps — no usable channel
// ---------------------------------------------------------------------------

func TestDetectLapsNoChannel(t *testing.T) {
	f := &File{
		Header:   Header{NumChannels: 1},
		Channels: []Channel{{Name: "Dummy", Freq: 10, DataLen: 100, Data: make([]float64, 100)}},
	}
	laps := f.DetectLaps()
	if laps != nil {
		t.Errorf("expected nil laps for file with no lap channel, got %v", laps)
	}
}

// ---------------------------------------------------------------------------
// ChannelByName edge cases
// ---------------------------------------------------------------------------

func TestChannelByNameNotFound(t *testing.T) {
	f, _ := ParseFile("testdata/ac-tatuusfa1-spa.ld")
	ch := f.ChannelByName("DoesNotExist__xyz")
	if ch != nil {
		t.Error("expected nil for unknown channel name")
	}
}

// ---------------------------------------------------------------------------
// Parse error paths
// ---------------------------------------------------------------------------

func TestParseTooSmall(t *testing.T) {
	_, err := Parse([]byte{0x01, 0x02, 0x03})
	if err == nil {
		t.Error("expected error for too-small input")
	}
}

func TestParseFileNotFound(t *testing.T) {
	_, err := ParseFile("no_such_file_xyz.ld")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestParseLDXFileNotFound(t *testing.T) {
	_, err := ParseLDXFile("no_such_file.ldx")
	if err == nil {
		t.Error("expected error for missing LDX file")
	}
}

// ---------------------------------------------------------------------------
// parseDateTime edge cases
// ---------------------------------------------------------------------------

func TestParseDateTimeFormats(t *testing.T) {
	tests := []struct{ date, time string; zero bool }{
		{"01/01/2024", "12:30:00", false},
		{"01/01/2024", "12:30", false},
		{"invalid", "invalid", true},
	}
	for _, tt := range tests {
		got := parseDateTime(tt.date, tt.time)
		if tt.zero && !got.IsZero() {
			t.Errorf("parseDateTime(%q, %q) = %v, want zero", tt.date, tt.time, got)
		}
		if !tt.zero && got.IsZero() {
			t.Errorf("parseDateTime(%q, %q) = zero, want non-zero", tt.date, tt.time)
		}
	}
}

// ---------------------------------------------------------------------------
// readSamples — exercise float32 and float64 code paths
// ---------------------------------------------------------------------------

func TestReadSamplesFloat32(t *testing.T) {
	// IEEE 754 float32 1.5 in little-endian: 0x3FC00000 → [0x00,0x00,0xC0,0x3F]
	raw := []byte{0x00, 0x00, 0xC0, 0x3F}
	ch := Channel{Kind: KindFloat32, DataLen: 1, Scale: 1, Dec: 0, Shift: 0, Mul: 1}
	got := readSamples(raw, ch)
	if len(got) != 1 || math.Abs(got[0]-1.5) > 0.001 {
		t.Errorf("float32: got %v, want [1.5]", got)
	}
}

func TestReadSamplesFloat64(t *testing.T) {
	// IEEE 754 float64 2.5 in little-endian: 0x4004000000000000 → [0x00,0x00,0x00,0x00,0x00,0x00,0x04,0x40]
	raw := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x40}
	ch := Channel{Kind: KindFloat64, DataLen: 1, Scale: 1, Dec: 0, Shift: 0, Mul: 1}
	got := readSamples(raw, ch)
	if len(got) != 1 || math.Abs(got[0]-2.5) > 0.001 {
		t.Errorf("float64: got %v, want [2.5]", got)
	}
}

func TestReadSamplesFloat16(t *testing.T) {
	// float16 bits for 1.0 = 0x3C00 → little-endian [0x00, 0x3C]
	raw := []byte{0x00, 0x3C}
	ch := Channel{Kind: KindFloat16, DataLen: 1, Scale: 1, Dec: 0, Shift: 0, Mul: 1}
	got := readSamples(raw, ch)
	if len(got) != 1 || math.Abs(got[0]-1.0) > 0.001 {
		t.Errorf("float16: got %v, want [1.0]", got)
	}
}

// ---------------------------------------------------------------------------
// ParseLDXFile — invalid XML error path
// ---------------------------------------------------------------------------

func TestParseLDXFileInvalidXML(t *testing.T) {
	tmp, err := os.CreateTemp("", "bad_*.ldx")
	if err != nil {
		t.Fatal(err)
	}
	tmp.WriteString("<not valid xml <><>")
	tmp.Close()
	defer os.Remove(tmp.Name())

	_, err = ParseLDXFile(tmp.Name())
	if err == nil {
		t.Error("expected error for invalid XML")
	}
}

