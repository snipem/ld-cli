package main

import (
	"math"
	"testing"

	ldparser "github.com/mail/go-ldparser"
)

// ---------------------------------------------------------------------------
// classifyBrakingHesitation
// ---------------------------------------------------------------------------

func TestClassifyBrakingHesitation_CleanZone(t *testing.T) {
	// Smooth ramp up to peak — no hesitation
	zone := []float64{0, 10, 30, 60, 90, 100, 95, 80}
	hesitant, typ := classifyBrakingHesitation(zone, 100)
	if hesitant {
		t.Errorf("expected clean zone, got hesitant (type=%s)", typ)
	}
}

func TestClassifyBrakingHesitation_HesitantZone(t *testing.T) {
	// Stab (50), release (15 — below half peak), reapply to 100
	zone := []float64{0, 50, 40, 15, 30, 70, 100, 95}
	hesitant, typ := classifyBrakingHesitation(zone, 100)
	if !hesitant {
		t.Error("expected hesitant zone")
	}
	if typ != "stab_release" {
		t.Errorf("expected stab_release, got %q", typ)
	}
}

func TestClassifyBrakingHesitation_TooShort(t *testing.T) {
	zone := []float64{0, 50, 100}
	hesitant, _ := classifyBrakingHesitation(zone, 100)
	if hesitant {
		t.Error("too-short zone should not be classified as hesitant")
	}
}

func TestClassifyBrakingHesitation_ZeroPeak(t *testing.T) {
	zone := []float64{0, 0, 0, 0, 0}
	hesitant, _ := classifyBrakingHesitation(zone, 0)
	if hesitant {
		t.Error("zero-peak zone should not be classified as hesitant")
	}
}

func TestClassifyBrakingHesitation_PeakAtStart(t *testing.T) {
	// Peak is index 2, peakIdx < 3 → not classified
	zone := []float64{50, 80, 100, 90, 80, 60}
	hesitant, _ := classifyBrakingHesitation(zone, 100)
	if hesitant {
		t.Error("peak too early — should not classify")
	}
}

// ---------------------------------------------------------------------------
// speedBandLabel
// ---------------------------------------------------------------------------

func TestSpeedBandLabel(t *testing.T) {
	cases := []struct {
		kmh  float64
		want string
	}{
		{0, "<80"},
		{79.9, "<80"},
		{80, "80-120"},
		{119.9, "80-120"},
		{120, "120-160"},
		{159.9, "120-160"},
		{160, "160-200"},
		{199.9, "160-200"},
		{200, ">200"},
		{300, ">200"},
	}
	for _, tc := range cases {
		got := speedBandLabel(tc.kmh)
		if got != tc.want {
			t.Errorf("speedBandLabel(%.1f) = %q, want %q", tc.kmh, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// brakingBySpeedBand
// ---------------------------------------------------------------------------

func TestBrakingBySpeedBand(t *testing.T) {
	zones := []brakingZoneDetail{
		{EntrySpeed: 100, Hesitant: true, TimeToPeak: 0.4},
		{EntrySpeed: 110, Hesitant: false, TimeToPeak: 0.3},
		{EntrySpeed: 180, Hesitant: true, TimeToPeak: 0.5},
	}
	bands := brakingBySpeedBand(zones)
	if len(bands) != 2 {
		t.Fatalf("expected 2 bands, got %d", len(bands))
	}
	// First band should be 80-120 with 2 zones, 1 hesitant
	b0 := bands[0]
	if b0.Band != "80-120" {
		t.Errorf("band[0] = %q, want 80-120", b0.Band)
	}
	if b0.Count != 2 {
		t.Errorf("band[0].Count = %d, want 2", b0.Count)
	}
	if b0.HesitantCount != 1 {
		t.Errorf("band[0].HesitantCount = %d, want 1", b0.HesitantCount)
	}
	if math.Abs(b0.HesitantPct-50) > 0.01 {
		t.Errorf("band[0].HesitantPct = %.2f, want 50", b0.HesitantPct)
	}
	if math.Abs(b0.MeanTimeToPeak-0.35) > 0.01 {
		t.Errorf("band[0].MeanTimeToPeak = %.3f, want 0.350", b0.MeanTimeToPeak)
	}
	// Second band should be 160-200 with 1 zone, 1 hesitant
	b1 := bands[1]
	if b1.Band != "160-200" {
		t.Errorf("band[1] = %q, want 160-200", b1.Band)
	}
	if b1.Count != 1 || b1.HesitantCount != 1 {
		t.Errorf("band[1]: Count=%d HesitantCount=%d, want 1/1", b1.Count, b1.HesitantCount)
	}
}

// ---------------------------------------------------------------------------
// throttleBySpeedBand
// ---------------------------------------------------------------------------

func TestThrottleBySpeedBand(t *testing.T) {
	d20a := 0.3
	d50a := 0.7
	d20b := 0.5
	apexes := []throttleApexDelay{
		{ApexSpeed: 90, Delay20: &d20a, Delay50: &d50a},
		{ApexSpeed: 95, Delay20: &d20b},
	}
	bands := throttleBySpeedBand(apexes)
	if len(bands) != 1 {
		t.Fatalf("expected 1 band, got %d", len(bands))
	}
	b := bands[0]
	if b.Band != "80-120" {
		t.Errorf("band = %q, want 80-120", b.Band)
	}
	if b.Count != 2 {
		t.Errorf("Count = %d, want 2", b.Count)
	}
	wantD20 := (0.3 + 0.5) / 2
	if math.Abs(b.MeanDelay20-wantD20) > 0.01 {
		t.Errorf("MeanDelay20 = %.3f, want %.3f", b.MeanDelay20, wantD20)
	}
	// Only one apex has d50
	if math.Abs(b.MeanDelay50-0.7) > 0.01 {
		t.Errorf("MeanDelay50 = %.3f, want 0.700", b.MeanDelay50)
	}
	// No d90 → should be 0
	if b.MeanDelay90 != 0 {
		t.Errorf("MeanDelay90 = %.3f, want 0", b.MeanDelay90)
	}
}

// ---------------------------------------------------------------------------
// computeHistogram
// ---------------------------------------------------------------------------

func TestComputeHistogram(t *testing.T) {
	data := make([]float64, 100)
	for i := range data {
		data[i] = float64(i) // 0..99
	}
	bins := computeHistogram(data, 10)
	if len(bins) != 10 {
		t.Fatalf("expected 10 bins, got %d", len(bins))
	}
	// Each bin should have ~10% of data
	totalPct := 0.0
	for _, b := range bins {
		totalPct += b.Pct
	}
	if math.Abs(totalPct-100) > 1 {
		t.Errorf("total pct = %.1f, want ~100", totalPct)
	}
	// First bin: lo=0, hi=9.9
	if bins[0].Lo != 0 {
		t.Errorf("bins[0].Lo = %v, want 0", bins[0].Lo)
	}
	// Last bin should include max value
	if bins[9].N == 0 {
		t.Error("last bin should have samples")
	}
}

func TestComputeHistogramConstant(t *testing.T) {
	data := []float64{5.0, 5.0, 5.0}
	bins := computeHistogram(data, 5)
	if len(bins) != 1 {
		t.Fatalf("constant data should produce 1 bin, got %d", len(bins))
	}
	if bins[0].Pct != 100 {
		t.Errorf("bin.Pct = %.1f, want 100", bins[0].Pct)
	}
}

func TestComputeHistogramEmpty(t *testing.T) {
	if computeHistogram(nil, 10) != nil {
		t.Error("nil data should return nil")
	}
	if computeHistogram([]float64{1, 2, 3}, 0) != nil {
		t.Error("zero bins should return nil")
	}
}

// ---------------------------------------------------------------------------
// annotateLabel
// ---------------------------------------------------------------------------

func TestAnnotateLabel(t *testing.T) {
	cases := []struct {
		e    event
		want string
	}{
		{event{Type: "braking_zone", SpeedEntry: 180}, "brake 180km/h"},
		{event{Type: "corner_apex", Speed: 85.5}, "apex 86km/h"},
		{event{Type: "gear_shift", Direction: "up", GearFrom: 3, GearTo: 4}, "up 3>4"},
		{event{Type: "full_throttle_zone"}, "full thr"},
		{event{Type: "lockup", Wheel: "FL"}, "lockup FL"},
		{event{Type: "unknown", Note: "some note"}, "some note"},
	}
	for _, tc := range cases {
		got := annotateLabel(tc.e)
		if got != tc.want {
			t.Errorf("annotateLabel(%s) = %q, want %q", tc.e.Type, got, tc.want)
		}
	}
}

func TestAnnotateLabelTruncate(t *testing.T) {
	e := event{Type: "unknown", Note: "this is a very long note that exceeds 20 chars"}
	got := annotateLabel(e)
	if len(got) > 20 {
		t.Errorf("label len = %d, want <= 20", len(got))
	}
}

// ---------------------------------------------------------------------------
// resolveLapWindow — best lap selection
// ---------------------------------------------------------------------------

func TestResolveLapWindowBest(t *testing.T) {
	fr := fileResult{
		Path: "test.ld",
		File: &ldparser.File{},
		Laps: []ldparser.Lap{
			{Number: 1, StartTime: 0, EndTime: 110, LapTime: 110},
			{Number: 2, StartTime: 110, EndTime: 218, LapTime: 108},
			{Number: 3, StartTime: 218, EndTime: 330, LapTime: 112},
		},
	}
	win, warn := resolveLapWindow(fr, "best")
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	if win.num == nil || *win.num != 2 {
		t.Errorf("expected best lap=2, got %v", win.num)
	}
	if win.lapTime != 108 {
		t.Errorf("expected lapTime=108, got %v", win.lapTime)
	}
}

func TestResolveLapWindowBestNoLaps(t *testing.T) {
	fr := fileResult{
		Path: "test.ld",
		File: &ldparser.File{},
		Laps: []ldparser.Lap{
			{Number: 1, StartTime: 0, EndTime: 110, LapTime: 0}, // LapTime=0 skipped
		},
	}
	_, warn := resolveLapWindow(fr, "best")
	if warn == "" {
		t.Error("expected warning for no laps with positive lap time")
	}
}

func TestResolveLapWindowNumber(t *testing.T) {
	fr := fileResult{
		Path: "test.ld",
		File: &ldparser.File{},
		Laps: []ldparser.Lap{
			{Number: 1, StartTime: 0, EndTime: 110, LapTime: 110},
			{Number: 2, StartTime: 110, EndTime: 218, LapTime: 108},
		},
	}
	win, warn := resolveLapWindow(fr, "1")
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	if win.num == nil || *win.num != 1 {
		t.Errorf("expected lap=1, got %v", win.num)
	}
}

// ---------------------------------------------------------------------------
// analyzeBrakingZones — integration with synthetic data
// ---------------------------------------------------------------------------

func TestAnalyzeBrakingZones_Basic(t *testing.T) {
	const freq = 100
	const n = 1000

	// Brake channel: one braking zone from 200-400 (200ms = 2s at 100Hz... wait, 200 samples at 100Hz = 2s)
	brakeData := make([]float64, n)
	for i := 200; i < 400; i++ {
		brakeData[i] = float64(i-200) / 200.0 * 100 // ramp 0→100
	}

	brakeCh := &ldparser.Channel{
		Name:    "Brake Pos",
		Freq:    freq,
		DataLen: n,
		Data:    brakeData,
	}

	zones := analyzeBrakingZones(brakeCh, nil, 0, float64(n)/freq)
	if len(zones) != 1 {
		t.Fatalf("expected 1 braking zone, got %d", len(zones))
	}
	z := zones[0]
	if z.Hesitant {
		t.Error("smooth ramp should not be classified as hesitant")
	}
	if z.PeakBrakePct < 99 {
		t.Errorf("peak brake pct = %.1f, want ~100", z.PeakBrakePct)
	}
}

func TestAnalyzeBrakingZones_Hesitation(t *testing.T) {
	const freq = 100
	const n = 1000

	// Stab-release-reapply pattern: hit 60, dip to 10, ramp to 100
	brakeData := make([]float64, n)
	for i := 100; i < 130; i++ {
		brakeData[i] = 60
	}
	// Clear local minimum at index 140: 30, 10, 30 (strictly below neighbors)
	brakeData[138] = 30
	brakeData[139] = 25
	brakeData[140] = 10 // local minimum below 50% of peak=100
	brakeData[141] = 25
	brakeData[142] = 30
	for i := 143; i < 300; i++ {
		brakeData[i] = 100
	}

	brakeCh := &ldparser.Channel{
		Name:    "Brake Pos",
		Freq:    freq,
		DataLen: n,
		Data:    brakeData,
	}

	zones := analyzeBrakingZones(brakeCh, nil, 0, float64(n)/freq)
	if len(zones) == 0 {
		t.Fatal("expected at least 1 braking zone")
	}
	z := zones[0]
	if !z.Hesitant {
		t.Error("stab-release pattern should be classified as hesitant")
	}
}

// ---------------------------------------------------------------------------
// medianOf
// ---------------------------------------------------------------------------

func TestMedianOf_Empty(t *testing.T) {
	result := medianOf([]float64{})
	if result != 0 {
		t.Errorf("medianOf(empty) = %v, want 0", result)
	}
}

func TestMedianOf_Single(t *testing.T) {
	result := medianOf([]float64{5.5})
	if result != 5.5 {
		t.Errorf("medianOf([5.5]) = %v, want 5.5", result)
	}
}

func TestMedianOf_OddCount(t *testing.T) {
	// Median of [1, 3, 5] is 3
	result := medianOf([]float64{5, 1, 3})
	if result != 3 {
		t.Errorf("medianOf([5, 1, 3]) = %v, want 3", result)
	}
}

func TestMedianOf_EvenCount(t *testing.T) {
	// Median of [1, 2, 3, 4] is (2+3)/2 = 2.5
	result := medianOf([]float64{4, 1, 2, 3})
	if result != 2.5 {
		t.Errorf("medianOf([4, 1, 2, 3]) = %v, want 2.5", result)
	}
}

func TestMedianOf_Duplicates(t *testing.T) {
	// Median of [1, 1, 1, 5, 5] is 1
	result := medianOf([]float64{5, 1, 1, 5, 1})
	if result != 1 {
		t.Errorf("medianOf([5, 1, 1, 5, 1]) = %v, want 1", result)
	}
}

func TestMedianOf_NegativeValues(t *testing.T) {
	// Median of [-5, -2, -1, 0, 2] is -1
	result := medianOf([]float64{2, -5, 0, -1, -2})
	if result != -1 {
		t.Errorf("medianOf([-5, -2, -1, 0, 2]) = %v, want -1", result)
	}
}

func TestMedianOf_DoesNotMutateInput(t *testing.T) {
	input := []float64{3, 1, 2}
	original := make([]float64, len(input))
	copy(original, input)
	medianOf(input)
	for i, v := range input {
		if v != original[i] {
			t.Errorf("medianOf mutated input: input[%d] was %v, now %v", i, original[i], v)
		}
	}
}

// ---------------------------------------------------------------------------
// stdDevOf
// ---------------------------------------------------------------------------

func TestStdDevOf_Empty(t *testing.T) {
	result := stdDevOf([]float64{})
	if result != 0 {
		t.Errorf("stdDevOf(empty) = %v, want 0", result)
	}
}

func TestStdDevOf_Single(t *testing.T) {
	result := stdDevOf([]float64{5.5})
	if result != 0 {
		t.Errorf("stdDevOf([5.5]) = %v, want 0", result)
	}
}

func TestStdDevOf_TwoValues(t *testing.T) {
	// StdDev of [1, 3]: mean=2, sq_diff=1+1=2, stddev=sqrt(2/1)=sqrt(2)≈1.414
	result := stdDevOf([]float64{1, 3})
	expected := math.Sqrt(2)
	if math.Abs(result-expected) > 1e-9 {
		t.Errorf("stdDevOf([1, 3]) = %v, want %v", result, expected)
	}
}

func TestStdDevOf_Constant(t *testing.T) {
	result := stdDevOf([]float64{5, 5, 5, 5})
	if result != 0 {
		t.Errorf("stdDevOf([5, 5, 5, 5]) = %v, want 0", result)
	}
}

func TestStdDevOf_KnownValues(t *testing.T) {
	// StdDev of [0, 1, 2]: mean=1, sq_diffs=1+0+1=2, stddev=sqrt(2/(3-1))=sqrt(1)=1
	result := stdDevOf([]float64{0, 1, 2})
	if math.Abs(result-1.0) > 1e-9 {
		t.Errorf("stdDevOf([0, 1, 2]) = %v, want 1.0", result)
	}
}

func TestStdDevOf_Negative(t *testing.T) {
	// StdDev of [-1, 1]: mean=0, sq_diffs=1+1=2, stddev=sqrt(2)
	result := stdDevOf([]float64{-1, 1})
	expected := math.Sqrt(2)
	if math.Abs(result-expected) > 1e-9 {
		t.Errorf("stdDevOf([-1, 1]) = %v, want %v", result, expected)
	}
}

// ---------------------------------------------------------------------------
// distResample
// ---------------------------------------------------------------------------

func TestDistResample_Empty(t *testing.T) {
	result := distResample([]float64{}, []float64{}, 100, 10)
	if len(result) != 10 {
		t.Fatalf("distResample(empty) should return 10 bins, got %d", len(result))
	}
	for _, v := range result {
		if v != 0 {
			t.Errorf("distResample(empty) bin = %v, want 0", v)
		}
	}
}

func TestDistResample_SingleBin(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5}
	cumDist := []float64{0, 1, 2, 3, 4}
	result := distResample(data, cumDist, 4, 1)
	if len(result) != 1 {
		t.Fatalf("distResample with 1 bin should return 1 bin, got %d", len(result))
	}
	if result[0] != 1 {
		t.Errorf("distResample(1 bin) = %v, want 1", result[0])
	}
}

func TestDistResample_Linear(t *testing.T) {
	// Simple case: data and cumDist have same length, linear interp
	data := []float64{10, 20, 30}
	cumDist := []float64{0, 1, 2}
	result := distResample(data, cumDist, 2, 3)
	if len(result) != 3 {
		t.Fatalf("distResample should return 3 bins, got %d", len(result))
	}
	// At bin 0 (target=0): interp at cumDist[0]=0 → data[0]=10
	if result[0] != 10 {
		t.Errorf("bin 0 = %v, want 10", result[0])
	}
	// At bin 1 (target=1): interp at cumDist[1]=1 → data[1]=20
	if result[1] != 20 {
		t.Errorf("bin 1 = %v, want 20", result[1])
	}
	// At bin 2 (target=2): interp at cumDist[2]=2 → data[2]=30
	if result[2] != 30 {
		t.Errorf("bin 2 = %v, want 30", result[2])
	}
}

func TestDistResample_Interpolation(t *testing.T) {
	// data = [0, 100], cumDist = [0, 10]
	// 5 bins: targets = [0, 2.5, 5, 7.5, 10]
	// All should interpolate linearly: [0, 25, 50, 75, 100]
	data := []float64{0, 100}
	cumDist := []float64{0, 10}
	result := distResample(data, cumDist, 10, 5)
	if len(result) != 5 {
		t.Fatalf("distResample should return 5 bins, got %d", len(result))
	}
	expected := []float64{0, 25, 50, 75, 100}
	for i, v := range result {
		if math.Abs(v-expected[i]) > 0.1 {
			t.Errorf("bin %d = %v, want %v", i, v, expected[i])
		}
	}
}

func TestDistResample_DifferentLengths(t *testing.T) {
	// data has 10 samples, cumDist has 5 samples (frequency mismatch)
	data := make([]float64, 10)
	for i := range data {
		data[i] = float64(i)
	}
	cumDist := []float64{0, 1, 2, 3, 4}
	result := distResample(data, cumDist, 4, 5)
	if len(result) != 5 {
		t.Fatalf("distResample should return 5 bins, got %d", len(result))
	}
	// Should not panic and should return monotonically increasing values
	for i := 1; i < len(result); i++ {
		if result[i] < result[i-1] {
			t.Errorf("bin[%d]=%v < bin[%d]=%v (should be monotonic)", i, result[i], i-1, result[i-1])
		}
	}
}

func TestDistResample_FlatDistance(t *testing.T) {
	// cumDist doesn't increase much
	data := []float64{10, 20, 30}
	cumDist := []float64{0, 0.1, 0.2}
	result := distResample(data, cumDist, 0.2, 3)
	if len(result) != 3 {
		t.Fatalf("distResample should return 3 bins, got %d", len(result))
	}
	// Should still produce valid output without NaN
	for i, v := range result {
		if math.IsNaN(v) {
			t.Errorf("bin %d is NaN", i)
		}
	}
}

// ---------------------------------------------------------------------------
// speedColor
// ---------------------------------------------------------------------------

func TestSpeedColor_ZeroSpeed(t *testing.T) {
	col := speedColor(0, 100)
	if col == "" {
		t.Error("speedColor(0, 100) returned empty string")
	}
	// Should return blue (slowest)
	if col != "#1a78c2" {
		t.Errorf("speedColor(0, 100) = %s, want #1a78c2 (blue)", col)
	}
}

func TestSpeedColor_MaxSpeed(t *testing.T) {
	col := speedColor(100, 100)
	if col == "" {
		t.Error("speedColor(100, 100) returned empty string")
	}
	// Should return red (fastest)
	if col != "#e74c3c" {
		t.Errorf("speedColor(100, 100) = %s, want #e74c3c (red)", col)
	}
}

func TestSpeedColor_HalfSpeed(t *testing.T) {
	col := speedColor(50, 100)
	if col == "" {
		t.Error("speedColor(50, 100) returned empty string")
	}
	// At midpoint (0.5), should be green (#27ae60)
	if col != "#27ae60" {
		t.Errorf("speedColor(50, 100) = %s, want #27ae60 (green)", col)
	}
}

func TestSpeedColor_BelowZero(t *testing.T) {
	col := speedColor(-10, 100)
	// Should clamp to 0 (blue)
	if col != "#1a78c2" {
		t.Errorf("speedColor(-10, 100) = %s, want #1a78c2 (blue)", col)
	}
}

func TestSpeedColor_AboveMax(t *testing.T) {
	col := speedColor(150, 100)
	// Should clamp to 1 (red)
	if col != "#e74c3c" {
		t.Errorf("speedColor(150, 100) = %s, want #e74c3c (red)", col)
	}
}

func TestSpeedColor_ZeroMaxSpeed(t *testing.T) {
	col := speedColor(10, 0)
	// Should treat 0 maxSpeed as valid and clamp speed to range
	if col == "" {
		t.Error("speedColor(10, 0) returned empty string")
	}
}

func TestSpeedColor_Quarter(t *testing.T) {
	col := speedColor(25, 100)
	// At 0.25, should be cyan (#00bcd4)
	if col != "#00bcd4" {
		t.Errorf("speedColor(25, 100) = %s, want #00bcd4 (cyan)", col)
	}
}

// ---------------------------------------------------------------------------
// parseMapMarks
// ---------------------------------------------------------------------------

func TestParseMapMarks_Empty(t *testing.T) {
	result := parseMapMarks([]string{})
	if len(result) != 0 {
		t.Errorf("parseMapMarks(empty) returned %d marks, want 0", len(result))
	}
}

func TestParseMapMarks_SingleMark_Default(t *testing.T) {
	result := parseMapMarks([]string{"0.23:T1"})
	if len(result) != 1 {
		t.Fatalf("parseMapMarks should return 1 mark, got %d", len(result))
	}
	m := result[0]
	if math.Abs(m.DistFrac-0.23) > 0.001 {
		t.Errorf("DistFrac = %v, want 0.23", m.DistFrac)
	}
	if m.Label != "T1" {
		t.Errorf("Label = %q, want %q", m.Label, "T1")
	}
	if m.Color != "#f1c40f" {
		t.Errorf("Color = %q, want %q (default yellow)", m.Color, "#f1c40f")
	}
}

func TestParseMapMarks_FullMark(t *testing.T) {
	result := parseMapMarks([]string{"0.5:Apex:#e74c3c"})
	if len(result) != 1 {
		t.Fatalf("parseMapMarks should return 1 mark, got %d", len(result))
	}
	m := result[0]
	if math.Abs(m.DistFrac-0.5) > 0.001 {
		t.Errorf("DistFrac = %v, want 0.5", m.DistFrac)
	}
	if m.Label != "Apex" {
		t.Errorf("Label = %q, want %q", m.Label, "Apex")
	}
	if m.Color != "#e74c3c" {
		t.Errorf("Color = %q, want %q", m.Color, "#e74c3c")
	}
}

func TestParseMapMarks_Multiple(t *testing.T) {
	result := parseMapMarks([]string{
		"0.2:T1",
		"0.5:Apex:#00ff00",
		"0.8:Exit",
	})
	if len(result) != 3 {
		t.Fatalf("parseMapMarks should return 3 marks, got %d", len(result))
	}
}

func TestParseMapMarks_InvalidFraction(t *testing.T) {
	result := parseMapMarks([]string{"abc:T1"})
	// Invalid fraction should be skipped
	if len(result) != 0 {
		t.Errorf("parseMapMarks with invalid fraction should skip mark, got %d marks", len(result))
	}
}

func TestParseMapMarks_TooFewParts(t *testing.T) {
	result := parseMapMarks([]string{"0.5"})
	// Need at least "dist_frac:label"
	if len(result) != 0 {
		t.Errorf("parseMapMarks with too few parts should skip mark, got %d marks", len(result))
	}
}

func TestParseMapMarks_Whitespace(t *testing.T) {
	result := parseMapMarks([]string{" 0.23 : T1 : #e74c3c "})
	if len(result) != 1 {
		t.Fatalf("parseMapMarks should handle whitespace, got %d marks", len(result))
	}
	m := result[0]
	if math.Abs(m.DistFrac-0.23) > 0.001 {
		t.Errorf("DistFrac = %v, want 0.23", m.DistFrac)
	}
	if m.Label != "T1" {
		t.Errorf("Label = %q, want %q", m.Label, "T1")
	}
	if m.Color != "#e74c3c" {
		t.Errorf("Color = %q, want %q", m.Color, "#e74c3c")
	}
}

func TestParseMapMarks_EmptyColor(t *testing.T) {
	result := parseMapMarks([]string{"0.23:T1:  "})
	if len(result) != 1 {
		t.Fatalf("parseMapMarks should return 1 mark, got %d", len(result))
	}
	// If color is empty after trim, should use default
	m := result[0]
	if m.Color != "#f1c40f" {
		t.Errorf("Color = %q, want %q (empty should default)", m.Color, "#f1c40f")
	}
}

func TestParseMapMarks_EdgeCases(t *testing.T) {
	result := parseMapMarks([]string{
		"0:Start",
		"1:End",
		"0.5:Mid:#ffffff",
	})
	if len(result) != 3 {
		t.Fatalf("parseMapMarks should parse edge fractions, got %d marks", len(result))
	}
	if result[0].DistFrac != 0 {
		t.Errorf("mark[0].DistFrac = %v, want 0", result[0].DistFrac)
	}
	if result[1].DistFrac != 1 {
		t.Errorf("mark[1].DistFrac = %v, want 1", result[1].DistFrac)
	}
}

// ---------------------------------------------------------------------------
// findPositionChannels
// ---------------------------------------------------------------------------

func TestFindPositionChannels_WorldCoords(t *testing.T) {
	f := &ldparser.File{
		Channels: []ldparser.Channel{
			{Name: "Car Coord X", Data: []float64{1, 2, 3}},
			{Name: "Car Coord Y", Data: []float64{4, 5, 6}},
		},
	}
	xCh, yCh, src := findPositionChannels(f)
	if xCh == nil || yCh == nil {
		t.Fatal("findPositionChannels should find world coordinates")
	}
	if src != "world_coords" {
		t.Errorf("source = %q, want world_coords", src)
	}
}

func TestFindPositionChannels_GPS(t *testing.T) {
	f := &ldparser.File{
		Channels: []ldparser.Channel{
			{Name: "GPS Latitude", Data: []float64{48.5, 48.6}},
			{Name: "GPS Longitude", Data: []float64{2.2, 2.3}},
		},
	}
	xCh, yCh, src := findPositionChannels(f)
	if xCh == nil || yCh == nil {
		t.Fatal("findPositionChannels should find GPS coordinates")
	}
	if src != "gps" {
		t.Errorf("source = %q, want gps", src)
	}
}

func TestFindPositionChannels_WorldCoordsPriority(t *testing.T) {
	// World coords should take priority over GPS
	f := &ldparser.File{
		Channels: []ldparser.Channel{
			{Name: "Car Coord X", Data: []float64{1, 2}},
			{Name: "Car Coord Y", Data: []float64{3, 4}},
			{Name: "GPS Latitude", Data: []float64{48.5, 48.6}},
			{Name: "GPS Longitude", Data: []float64{2.2, 2.3}},
		},
	}
	_, _, src := findPositionChannels(f)
	if src != "world_coords" {
		t.Errorf("source = %q, want world_coords (priority)", src)
	}
}

func TestFindPositionChannels_None(t *testing.T) {
	f := &ldparser.File{
		Channels: []ldparser.Channel{
			{Name: "Speed", Data: []float64{1, 2}},
		},
	}
	xCh, yCh, src := findPositionChannels(f)
	if xCh != nil || yCh != nil {
		t.Error("findPositionChannels should return nil for missing position channels")
	}
	if src != "" {
		t.Errorf("source = %q, want empty", src)
	}
}

func TestFindPositionChannels_EmptyData(t *testing.T) {
	f := &ldparser.File{
		Channels: []ldparser.Channel{
			{Name: "Car Coord X", Data: []float64{}},
			{Name: "Car Coord Y", Data: []float64{}},
			{Name: "GPS Latitude", Data: []float64{48.5}},
			{Name: "GPS Longitude", Data: []float64{2.2}},
		},
	}
	_, _, src := findPositionChannels(f)
	// Should fall back to GPS since world coords are empty
	if src != "gps" {
		t.Errorf("source = %q, want gps (fallback)", src)
	}
}

func TestFindPositionChannels_PartialWorldCoords(t *testing.T) {
	// Only X is present, should fall back to GPS
	f := &ldparser.File{
		Channels: []ldparser.Channel{
			{Name: "Car Coord X", Data: []float64{1, 2}},
			{Name: "GPS Latitude", Data: []float64{48.5}},
			{Name: "GPS Longitude", Data: []float64{2.2}},
		},
	}
	_, _, src := findPositionChannels(f)
	if src != "gps" {
		t.Errorf("source = %q, want gps (partial world coords)", src)
	}
}

// ---------------------------------------------------------------------------
// buildTrackPoints
// ---------------------------------------------------------------------------

func TestBuildTrackPoints_WorldCoords(t *testing.T) {
	xCh := &ldparser.Channel{
		Name: "Car Coord X",
		Freq: 100,
		Data: []float64{0, 1, 2, 3, 4, 5},
	}
	yCh := &ldparser.Channel{
		Name: "Car Coord Y",
		Freq: 100,
		Data: []float64{0, 0, 0, 1, 1, 1},
	}
	speedCh := &ldparser.Channel{
		Name: "Ground Speed",
		Freq: 100,
		Data: []float64{100, 100, 100, 100, 100, 100},
	}
	pts := buildTrackPoints(xCh, yCh, speedCh, 0, 0.06, false)
	if len(pts) == 0 {
		t.Fatal("buildTrackPoints should return points")
	}
	// All points should have monotonically increasing distance
	for i := 1; i < len(pts); i++ {
		if pts[i].dist < pts[i-1].dist {
			t.Errorf("distance not monotonic: pts[%d].dist=%v < pts[%d].dist=%v", i, pts[i].dist, i-1, pts[i-1].dist)
		}
	}
}

func TestBuildTrackPoints_EmptyXData(t *testing.T) {
	xCh := &ldparser.Channel{Name: "X", Freq: 100, Data: []float64{}}
	yCh := &ldparser.Channel{Name: "Y", Freq: 100, Data: []float64{1}}
	pts := buildTrackPoints(xCh, yCh, nil, 0, 1, false)
	if len(pts) != 0 {
		t.Errorf("buildTrackPoints with empty X data should return empty, got %d points", len(pts))
	}
}

func TestBuildTrackPoints_EmptyYData(t *testing.T) {
	xCh := &ldparser.Channel{Name: "X", Freq: 100, Data: []float64{1}}
	yCh := &ldparser.Channel{Name: "Y", Freq: 100, Data: []float64{}}
	pts := buildTrackPoints(xCh, yCh, nil, 0, 1, false)
	if len(pts) != 0 {
		t.Errorf("buildTrackPoints with empty Y data should return empty, got %d points", len(pts))
	}
}

func TestBuildTrackPoints_Downsampling(t *testing.T) {
	// Create large data arrays (should trigger downsampling at 2000 points)
	n := 5000
	xCh := &ldparser.Channel{
		Name: "Car Coord X",
		Freq: 100,
		Data: make([]float64, n),
	}
	yCh := &ldparser.Channel{
		Name: "Car Coord Y",
		Freq: 100,
		Data: make([]float64, n),
	}
	for i := 0; i < n; i++ {
		xCh.Data[i] = float64(i)
		yCh.Data[i] = float64(i)
	}
	pts := buildTrackPoints(xCh, yCh, nil, 0, float64(n)/100, false)
	if len(pts) > 2500 {
		t.Errorf("buildTrackPoints should downsample to ~2000 points, got %d", len(pts))
	}
}

func TestBuildTrackPoints_NoSpeedChannel(t *testing.T) {
	xCh := &ldparser.Channel{
		Name: "Car Coord X",
		Freq: 100,
		Data: []float64{0, 1, 2},
	}
	yCh := &ldparser.Channel{
		Name: "Car Coord Y",
		Freq: 100,
		Data: []float64{0, 1, 2},
	}
	pts := buildTrackPoints(xCh, yCh, nil, 0, 0.03, false)
	if len(pts) == 0 {
		t.Fatal("buildTrackPoints should work without speed channel")
	}
	for i, pt := range pts {
		if pt.speed != 0 {
			t.Errorf("pts[%d].speed = %v, want 0 (no speed channel)", i, pt.speed)
		}
	}
}

func TestBuildTrackPoints_GPS(t *testing.T) {
	// Latitude/Longitude channels
	latCh := &ldparser.Channel{
		Name: "GPS Latitude",
		Freq: 10,
		Data: []float64{48.85, 48.851, 48.852},
	}
	lonCh := &ldparser.Channel{
		Name: "GPS Longitude",
		Freq: 10,
		Data: []float64{2.295, 2.296, 2.297},
	}
	pts := buildTrackPoints(latCh, lonCh, nil, 0, 0.3, true)
	if len(pts) == 0 {
		t.Fatal("buildTrackPoints should handle GPS coordinates")
	}
	// Points should have different X,Y coords (projected)
	if pts[0].x == pts[1].x && pts[0].y == pts[1].y {
		t.Error("GPS projection should produce different coordinates")
	}
}

func TestBuildTrackPoints_CumulativeDistance(t *testing.T) {
	// Simple square: (0,0) -> (1,0) -> (1,1) -> (0,1)
	xCh := &ldparser.Channel{
		Name: "Car Coord X",
		Freq: 1, // 1 Hz for easy math
		Data: []float64{0, 1, 1, 0},
	}
	yCh := &ldparser.Channel{
		Name: "Car Coord Y",
		Freq: 1,
		Data: []float64{0, 0, 1, 1},
	}
	pts := buildTrackPoints(xCh, yCh, nil, 0, 4, false)
	if len(pts) == 0 {
		t.Fatal("buildTrackPoints should return points")
	}
	// Last point distance should be ~3 (1 + 1 + 1)
	lastDist := pts[len(pts)-1].dist
	if math.Abs(lastDist-3.0) > 0.1 {
		t.Errorf("final distance = %v, want ~3.0 (perimeter of square)", lastDist)
	}
}
