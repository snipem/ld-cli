package ldparser

import (
	"strings"
	"testing"
)

const acTestFile = "testdata/ac-tatuusfa1-spa.ld"
const lmuTestFile = "testdata/lmu-bmwgt3-spa-q1.ld"
const gt7TestFile = "testdata/gt7-alsace-mini.ld"

func TestParseACFile(t *testing.T) {
	f, err := ParseFile(acTestFile)
	if err != nil {
		t.Fatalf("Failed to parse %s: %v", acTestFile, err)
	}

	if f.Header.NumChannels == 0 {
		t.Errorf("Expected non-zero channels in header")
	}

	if len(f.Channels) == 0 {
		t.Errorf("Expected parsed channels")
	}

	// Driver field should be sanitized (zeroed)
	if f.Header.Driver != "" {
		t.Errorf("Driver field not empty: %q", f.Header.Driver)
	}

	t.Logf("Venue: %s, Channels: %d", f.Header.Venue, len(f.Channels))
}

func TestDetectLaps(t *testing.T) {
	f, err := ParseFile(acTestFile)
	if err != nil {
		t.Fatalf("Failed to parse %s: %v", acTestFile, err)
	}

	laps := f.DetectLaps()
	if len(laps) == 0 {
		t.Errorf("Expected at least one lap")
	}

	for _, lap := range laps {
		if lap.LapTime <= 0 {
			t.Errorf("Lap %d has invalid lap time: %f", lap.Number, lap.LapTime)
		}
	}
	t.Logf("Detected %d laps", len(laps))
}

func TestACLapTimesReasonable(t *testing.T) {
	f, err := ParseFile(acTestFile)
	if err != nil {
		t.Fatalf("Failed to parse %s: %v", acTestFile, err)
	}

	laps := f.DetectLaps()
	if len(laps) < 2 {
		t.Fatalf("Expected at least 2 laps, got %d", len(laps))
	}

	// Lap 1 should be a full hot lap (~2:20 at Spa in a Tatuus FA1)
	lap1 := laps[1]
	wallTime := lap1.EndTime - lap1.StartTime
	if lap1.LapTime < 60 || lap1.LapTime > 300 {
		t.Errorf("Lap 1 time %.3fs outside reasonable range (60-300s)", lap1.LapTime)
	}
	// Lap time should be within 5% of wall time
	deviation := (lap1.LapTime - wallTime) / wallTime
	if deviation < -0.05 || deviation > 0.05 {
		t.Errorf("Lap 1 time %.3fs deviates %.1f%% from wall time %.3fs", lap1.LapTime, deviation*100, wallTime)
	}
}

func TestParseLMUFile(t *testing.T) {
	f, err := ParseFile(lmuTestFile)
	if err != nil {
		t.Fatalf("Failed to parse %s: %v", lmuTestFile, err)
	}

	if len(f.Channels) == 0 {
		t.Fatal("Expected parsed channels")
	}
	if f.Header.Driver != "" {
		t.Errorf("Driver field not empty: %q", f.Header.Driver)
	}

	laps := f.DetectLaps()
	if len(laps) == 0 {
		t.Fatal("Expected at least one lap")
	}
	for _, lap := range laps {
		wallTime := lap.EndTime - lap.StartTime
		if lap.LapTime <= 0 {
			t.Errorf("Lap %d has invalid lap time: %f", lap.Number, lap.LapTime)
		}
		// LMU has no Lap Time channel, so lap time should equal wall time
		if lap.LapTime != wallTime {
			t.Errorf("Lap %d: lap_time=%.3f != wall_time=%.3f", lap.Number, lap.LapTime, wallTime)
		}
	}
	t.Logf("Detected %d laps, venue: %s", len(laps), f.Header.Venue)
}

func TestACHasBrakeChannel(t *testing.T) {
	f, err := ParseFile(acTestFile)
	if err != nil {
		t.Fatalf("Failed to parse %s: %v", acTestFile, err)
	}

	ch := f.ChannelByName("Brake Pos")
	if ch == nil {
		t.Fatal("Expected 'Brake Pos' channel in AC file")
	}
	if len(ch.Data) == 0 {
		t.Fatal("Brake Pos channel has no data")
	}

	// Verify brake data has actual braking (max > 0)
	maxBrake := 0.0
	for _, v := range ch.Data {
		if v > maxBrake {
			maxBrake = v
		}
	}
	if maxBrake <= 0 {
		t.Errorf("Brake Pos channel has no braking data (max=%.2f)", maxBrake)
	}
	t.Logf("Brake Pos: %d samples, max=%.1f", len(ch.Data), maxBrake)
}


// ---------------------------------------------------------------------------
// ParseMeta correctness tests
// ---------------------------------------------------------------------------

// TestParseMetaMatchesFull verifies that ParseMetaFile produces the same
// header fields and lap list as a full ParseFile.
func TestParseMetaMatchesFull(t *testing.T) {
	files := []string{acTestFile, lmuTestFile, gt7TestFile}
	for _, path := range files {
		t.Run(path, func(t *testing.T) {
			full, err := ParseFile(path)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			meta, err := ParseMetaFile(path)
			if err != nil {
				t.Fatalf("ParseMetaFile: %v", err)
			}

			// Header fields
			if full.Header.Driver != meta.Header.Driver {
				t.Errorf("Driver: full=%q meta=%q", full.Header.Driver, meta.Header.Driver)
			}
			if full.Header.VehicleID != meta.Header.VehicleID {
				t.Errorf("VehicleID: full=%q meta=%q", full.Header.VehicleID, meta.Header.VehicleID)
			}
			if full.Header.Venue != meta.Header.Venue {
				t.Errorf("Venue: full=%q meta=%q", full.Header.Venue, meta.Header.Venue)
			}
			if !full.Header.DateTime.Equal(meta.Header.DateTime) {
				t.Errorf("DateTime: full=%v meta=%v", full.Header.DateTime, meta.Header.DateTime)
			}
			if full.Header.NumChannels != meta.Header.NumChannels {
				t.Errorf("NumChannels: full=%d meta=%d", full.Header.NumChannels, meta.Header.NumChannels)
			}

			// Channel catalogue size
			if len(full.Channels) != len(meta.Channels) {
				t.Errorf("channel count: full=%d meta=%d", len(full.Channels), len(meta.Channels))
			}

			// Lap detection must agree
			fullLaps := full.DetectLaps()
			metaLaps := meta.DetectLaps()
			if len(fullLaps) != len(metaLaps) {
				t.Fatalf("lap count: full=%d meta=%d", len(fullLaps), len(metaLaps))
			}
			for i := range fullLaps {
				fl, ml := fullLaps[i], metaLaps[i]
				if fl.Number != ml.Number {
					t.Errorf("lap %d: Number full=%d meta=%d", i, fl.Number, ml.Number)
				}
				if fl.LapTime != ml.LapTime {
					t.Errorf("lap %d: LapTime full=%.3f meta=%.3f", i, fl.LapTime, ml.LapTime)
				}
			}
		})
	}
}

// TestParseMetaNonLapChannelsHaveNoData confirms that non-lap channels
// have nil Data (i.e. we really did skip them).
func TestParseMetaNonLapChannelsHaveNoData(t *testing.T) {
	f, err := ParseMetaFile(gt7TestFile)
	if err != nil {
		t.Fatal(err)
	}
	lapNames := map[string]bool{
		"lap number": true, "lapnumber": true,
		"lap time": true, "laptime": true,
		"lap distance": true, "lapdistance": true, "lap_distance": true,
	}
	skipped, loaded := 0, 0
	for _, ch := range f.Channels {
		if lapNames[strings.ToLower(ch.Name)] {
			loaded++
		} else {
			if ch.Data != nil {
				t.Errorf("channel %q should have nil Data after ParseMeta", ch.Name)
			}
			skipped++
		}
	}
	t.Logf("%d channels skipped, %d lap channels loaded", skipped, loaded)
}
