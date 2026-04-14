package main

import (
	"flag"
	"fmt"
	"math"
	"sort"
	"strings"

	ldparser "github.com/mail/go-ldparser"
)

// ---------------------------------------------------------------------------
// Median lap
// ---------------------------------------------------------------------------
//
// A "median lap" is a synthetic lap that represents the driver's repeatable
// baseline — the patterns they produce on every clean lap, free from one-off
// outliers like a single late-braking event or a traffic incident.
//
// Algorithm:
//
//  1. Select laps: take the best lap time, include all complete laps within
//     ±threshold seconds of it (default 100 ms). Only laps > 30 s qualify.
//
//  2. Distance alignment: for each selected lap, integrate the speed channel
//     (kph → m/s × dt) to get a cumulative distance trace. Corners are then
//     at the same distance on every lap regardless of lap-time variation.
//     This is what makes throttle and brake curves overlap correctly.
//
//  3. Resample: linearly interpolate every channel from each selected lap
//     onto `bins` uniform distance points spanning [0 … lap_distance].
//
//  4. Pointwise median: at each of the `bins` distance positions, sort the N
//     values from the N selected laps and take the middle one. An outlier
//     braking event on one lap does not shift the median at that distance bin.
//
// The result contains `median_sources` (lap numbers used) so the LLM always
// knows which laps contributed and can flag if the sample size is small.
//
// Computational cost: O(laps × channels × samples). For 5 laps × 100 ch ×
// 10 000 samples × 1000 bins this completes in well under a second.

const defaultMedianBins = 1000
const defaultMedianThresholdSec = 0.100     // 100 ms
const defaultDeviationThresholdSec = 0.750  // 750 ms

// medianLapResult is the output of BuildMedianLap. Always included verbatim
// in JSON output so the LLM has full provenance.
type medianLapResult struct {
	Sources      []int     `json:"median_sources"`       // lap numbers that contributed
	LapTimes     []float64 `json:"median_source_times"`  // their individual lap times (s)
	ThresholdSec float64   `json:"median_threshold_sec"` // ±window used for selection
	BestLapTime  float64   `json:"best_lap_time"`        // fastest complete lap in file
	MedianTime   float64   `json:"median_lap_time"`      // median of source lap times
	Bins         int       `json:"distance_bins"`        // number of resampling bins
	TotalDistM   float64   `json:"total_distance_m"`     // median lap distance in metres
	SpeedChannel string    `json:"speed_channel"`        // channel used for integration
	Channels     []medianChannel `json:"channels"`
}

type medianChannel struct {
	Name   string    `json:"name"`
	Unit   string    `json:"unit"`
	// values[i] is the median value at distance fraction i/bins across the lap.
	// values[0] = lap start, values[bins-1] = lap end.
	Values []float64 `json:"values"`
	// stddev[i] is the sample standard deviation across contributing laps at bin i.
	// nil when only one lap contributed (stddev undefined).
	StdDev []float64 `json:"stddev,omitempty"`
}

// BuildMedianLap computes a synthetic median lap from fr.
// thresholdSec: max deviation from best lap time (≤0 → 100 ms default).
// bins: distance resampling resolution (≤0 → 1000 default).
func BuildMedianLap(fr fileResult, thresholdSec float64, bins int) (*medianLapResult, error) {
	if thresholdSec <= 0 {
		thresholdSec = defaultMedianThresholdSec
	}
	if bins <= 0 {
		bins = defaultMedianBins
	}

	// ── 1. Select candidate laps ─────────────────────────────────────────────
	bestTime := math.MaxFloat64
	for _, l := range fr.Laps {
		if l.Number > 0 && l.LapTime >= 30 && l.LapTime < bestTime {
			bestTime = l.LapTime
		}
	}
	if bestTime == math.MaxFloat64 {
		return nil, fmt.Errorf("no complete laps found (need at least one lap ≥ 30 s)")
	}

	var selected []ldparser.Lap
	for _, l := range fr.Laps {
		if l.Number > 0 && l.LapTime >= 30 && math.Abs(l.LapTime-bestTime) <= thresholdSec {
			selected = append(selected, l)
		}
	}

	// ── 2. Find speed channel ─────────────────────────────────────────────────
	speedCh := findChannelFuzzy(fr.File,
		"Ground Speed", "Speed", "Vehicle Speed", "GPS Speed",
		"GroundSpeed", "VehicleSpeed",
	)
	if speedCh == nil {
		return nil, fmt.Errorf("no speed channel found — distance alignment requires a speed channel")
	}

	// ── 3. Resample every channel onto distance bins for each selected lap ────
	var activeChs []*ldparser.Channel
	for i := range fr.File.Channels {
		if fr.File.Channels[i].Data != nil {
			activeChs = append(activeChs, &fr.File.Channels[i])
		}
	}

	type lapBins struct {
		lapNum  int
		lapTime float64
		distM   float64
		ch      [][]float64 // [channel][bin]
	}

	resampled := make([]lapBins, 0, len(selected))

	for _, lap := range selected {
		speedData, _ := sliceChannel(speedCh, lap.StartTime, lap.EndTime)
		if len(speedData) < 2 {
			continue
		}
		dt := 1.0 / float64(speedCh.Freq)

		// Cumulative distance in metres.
		cumDist := make([]float64, len(speedData))
		for i := 1; i < len(speedData); i++ {
			v := speedData[i]
			if v < 0 {
				v = 0
			}
			cumDist[i] = cumDist[i-1] + (v/3.6)*dt
		}
		totalDist := cumDist[len(cumDist)-1]
		if totalDist < 10 {
			continue
		}

		// Resample each channel.
		chBins := make([][]float64, len(activeChs))
		for ci, ch := range activeChs {
			data, _ := sliceChannel(ch, lap.StartTime, lap.EndTime)
			chBins[ci] = distResample(data, cumDist, totalDist, bins)
		}

		resampled = append(resampled, lapBins{
			lapNum:  lap.Number,
			lapTime: lap.LapTime,
			distM:   totalDist,
			ch:      chBins,
		})
	}

	if len(resampled) == 0 {
		return nil, fmt.Errorf("no laps could be resampled (check speed channel data in selected lap windows)")
	}

	// ── 4. Pointwise median + stddev ─────────────────────────────────────────
	n := len(resampled)
	scratch := make([]float64, n)
	medChs := make([]medianChannel, len(activeChs))
	computeStdDev := n >= 2

	for ci, ch := range activeChs {
		vals := make([]float64, bins)
		var devs []float64
		if computeStdDev {
			devs = make([]float64, bins)
		}
		for b := 0; b < bins; b++ {
			for ri := range resampled {
				scratch[ri] = resampled[ri].ch[ci][b]
			}
			vals[b] = medianOf(scratch[:n])
			if computeStdDev {
				devs[b] = stdDevOf(scratch[:n])
			}
		}
		medChs[ci] = medianChannel{Name: ch.Name, Unit: ch.Unit, Values: vals, StdDev: devs}
	}

	// ── 5. Result ─────────────────────────────────────────────────────────────
	sources := make([]int, len(resampled))
	lapTimes := make([]float64, len(resampled))
	dists := make([]float64, len(resampled))
	for i, r := range resampled {
		sources[i] = r.lapNum
		lapTimes[i] = r.lapTime
		dists[i] = r.distM
	}

	return &medianLapResult{
		Sources:      sources,
		LapTimes:     lapTimes,
		ThresholdSec: thresholdSec,
		BestLapTime:  round3(bestTime),
		MedianTime:   round3(medianOf(lapTimes)),
		Bins:         bins,
		TotalDistM:   round3(medianOf(dists)),
		SpeedChannel: speedCh.Name,
		Channels:     medChs,
	}, nil
}

// distResample linearly interpolates data onto bins uniform distance points.
// cumDist is the cumulative distance trace aligned to data's sample count.
// If data has a different length from cumDist, indices are proportionally mapped.
func distResample(data, cumDist []float64, totalDist float64, bins int) []float64 {
	out := make([]float64, bins)
	nd := len(cumDist)
	nv := len(data)
	if nv == 0 || nd < 2 {
		return out
	}

	denom := float64(bins - 1)
	if denom == 0 {
		denom = 1
	}
	for b := 0; b < bins; b++ {
		target := totalDist * float64(b) / denom

		// Binary search for bracket in cumDist.
		lo, hi := 0, nd-1
		for lo < hi-1 {
			mid := (lo + hi) / 2
			if cumDist[mid] <= target {
				lo = mid
			} else {
				hi = mid
			}
		}

		// Map distance indices to data indices (different freq → different length).
		dlo := lo * (nv - 1) / imax(nd-1, 1)
		dhi := hi * (nv - 1) / imax(nd-1, 1)
		if dlo >= nv {
			dlo = nv - 1
		}
		if dhi >= nv {
			dhi = nv - 1
		}

		if dlo == dhi || cumDist[hi] <= cumDist[lo] {
			out[b] = data[dlo]
			continue
		}
		frac := (target - cumDist[lo]) / (cumDist[hi] - cumDist[lo])
		out[b] = data[dlo] + frac*(data[dhi]-data[dlo])
	}
	return out
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// medianOf returns the median of vals (sorts a copy).
func medianOf(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// stdDevOf returns the sample standard deviation of vals (n-1 denominator).
func stdDevOf(vals []float64) float64 {
	n := len(vals)
	if n < 2 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(n)
	var sq float64
	for _, v := range vals {
		d := v - mean
		sq += d * d
	}
	return math.Sqrt(sq / float64(n-1))
}

// ---------------------------------------------------------------------------
// Deviation lap — speed (and channel) stddev across best laps
// ---------------------------------------------------------------------------
//
// BuildDeviationLap is an alias for BuildMedianLap with a 300 ms default
// threshold. The StdDev field on each medianChannel carries the pointwise
// standard deviation — the deviation trace showing where your best laps
// differ from each other.
func BuildDeviationLap(fr fileResult, thresholdSec float64, bins int) (*medianLapResult, error) {
	if thresholdSec <= 0 {
		thresholdSec = defaultDeviationThresholdSec
	}
	return BuildMedianLap(fr, thresholdSec, bins)
}

// ---------------------------------------------------------------------------
// CLI command: analyze median
// ---------------------------------------------------------------------------

func runAnalyzeMedian(args []string) {
	fs := flag.NewFlagSet("analyze median", flag.ExitOnError)
	thresholdMs := fs.Float64("threshold", 100, "±ms around best lap time; only laps within this window contribute")
	bins := fs.Int("bins", defaultMedianBins, "distance resampling bins (higher = finer resolution)")
	chFlag := fs.String("ch", "", "comma-separated channel names or globs (default: all)")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)

	if len(filePaths) == 0 {
		fatalJSON("analyze median: no file paths provided")
	}

	files := loadFiles(filePaths)

	type fileOut struct {
		Path      string           `json:"path"`
		MedianLap *medianLapResult `json:"median_lap,omitempty"`
		Error     string           `json:"error,omitempty"`
	}
	resp := struct {
		Files []fileOut `json:"files"`
		Usage string    `json:"usage"`
	}{
		Usage: strings.Join([]string{
			"median_lap.median_sources lists the lap numbers that contributed.",
			"median_lap.channels[].values is indexed by distance fraction (values[0]=start, values[bins-1]=end).",
			"To compare a specific channel: request --ch 'Brake Pos,Throttle Pos,Ground Speed'.",
			"The median suppresses one-off outliers; repeating patterns appear clearly.",
			"Sample size (len(median_sources)) affects reliability: ≥3 laps is good, 1 lap = no median effect.",
		}, " "),
	}

	for _, fr := range files {
		result, err := BuildMedianLap(fr, *thresholdMs/1000.0, *bins)
		if err != nil {
			resp.Files = append(resp.Files, fileOut{Path: fr.Path, Error: err.Error()})
			continue
		}

		// Filter channels if --ch was specified.
		if *chFlag != "" {
			patterns := strings.Split(*chFlag, ",")
			var kept []medianChannel
			for _, mc := range result.Channels {
				for _, p := range patterns {
					if matchPattern(mc.Name, p) {
						kept = append(kept, mc)
						break
					}
				}
			}
			result.Channels = kept
		}

		resp.Files = append(resp.Files, fileOut{Path: fr.Path, MedianLap: result})
	}

	writeJSON(resp)
}

// ---------------------------------------------------------------------------
// CLI command: analyze deviation
// ---------------------------------------------------------------------------

func runAnalyzeDeviation(args []string) {
	fs := flag.NewFlagSet("analyze deviation", flag.ExitOnError)
	thresholdMs := fs.Float64("threshold", 750, "±ms around best lap time; only laps within this window contribute")
	bins := fs.Int("bins", defaultMedianBins, "distance resampling bins (higher = finer resolution)")
	chFlag := fs.String("ch", "", "comma-separated channel names or globs (default: speed channel only)")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)

	if len(filePaths) == 0 {
		fatalJSON("analyze deviation: no file paths provided")
	}

	files := loadFiles(filePaths)

	type fileOut struct {
		Path         string           `json:"path"`
		DeviationLap *medianLapResult `json:"deviation_lap,omitempty"`
		Error        string           `json:"error,omitempty"`
	}
	resp := struct {
		Files []fileOut `json:"files"`
		Usage string    `json:"usage"`
	}{
		Usage: strings.Join([]string{
			"deviation_lap.channels[].stddev is the speed deviation trace: stddev of the best laps at each distance bin.",
			"A flat stddev line = very consistent. Peaks show where even your best laps differ.",
			"deviation_lap.median_sources lists which laps contributed (replay laps are excluded).",
			"deviation_lap.median_source_times shows their lap times — all within threshold_pct% of best.",
			"Use --ch to focus on speed only: --ch 'Ground Speed'. Default shows speed channel only.",
			"With a perfect driver stddev would be zero everywhere. Peaks at corners = inconsistency zones.",
		}, " "),
	}

	for _, fr := range files {
		result, err := BuildDeviationLap(fr, *thresholdMs/1000.0, *bins)
		if err != nil {
			resp.Files = append(resp.Files, fileOut{Path: fr.Path, Error: err.Error()})
			continue
		}

		// Default: speed channel only (the canonical deviation diagram).
		// Override with --ch to add throttle, brake, etc.
		filter := *chFlag
		if filter == "" {
			filter = result.SpeedChannel
		}
		patterns := strings.Split(filter, ",")
		var kept []medianChannel
		for _, mc := range result.Channels {
			for _, p := range patterns {
				if matchPattern(mc.Name, p) {
					kept = append(kept, mc)
					break
				}
			}
		}
		result.Channels = kept

		resp.Files = append(resp.Files, fileOut{Path: fr.Path, DeviationLap: result})
	}

	writeJSON(resp)
}
