// ldcli — LLM-friendly CLI for .ld race telemetry files (the de facto standard for sim racing).
//
// Start with: ldcli guide   (JSON usage guide + escalation strategy)
//
// Usage:
//
//	ldcli guide                          # JSON docs for LLM consumption
//	ldcli info   [files...]              # header, lap list, channel catalogue
//	ldcli inspect [files...]             # data quality + channel groups + interesting channels
//	ldcli summarize [files...] [flags]   # per-channel stats per lap; add --sectors N; --lap all adds trends
//	ldcli events [files...] [flags]      # detect gear shifts, braking zones, corners, lockups
//	ldcli diff <file(s)> --ref N --cmp N # lap-to-lap time delta with sector breakdown
//	ldcli data   [files...] [flags]      # time-series samples (150pt LTTB default)
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	ldparser "github.com/mail/go-ldparser"
)

// Version information injected at build time
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "guide":
		runGuide()
	case "laps":
		runInfo(append(os.Args[2:], "--format", "laps"))
	case "info":
		runInfo(os.Args[2:])
	case "inspect":
		runInspect(os.Args[2:])
	case "summarize":
		runSummarize(os.Args[2:])
	case "events":
		runEvents(os.Args[2:])
	case "diff":
		runDiff(os.Args[2:])
	case "data":
		runData(os.Args[2:])
	case "report":
		runReport(os.Args[2:])
	case "analyze":
		runAnalyze(os.Args[2:])
	case "compare":
		runCompare(os.Args[2:])
	case "map":
		runMap(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	case "version", "-v", "--version":
		fmt.Printf("ldcli %s (commit %s, built %s)\n", Version, Commit, BuildTime)
	default:
		fatalJSON(fmt.Sprintf("unknown command %q — run 'ldcli guide' for JSON documentation", os.Args[1]))
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `ldcli — race telemetry CLI for .ld files

Start here:
  ldcli laps <file.ld>                 Driver, vehicle, venue, lap times + best lap

Analysis:
  ldcli inspect <files...>             Data quality + channel groups
  ldcli summarize <files...>           Per-channel stats per lap
  ldcli events <files...>              Braking zones, apexes, gear shifts, lockups
  ldcli diff <file(s)>                 Lap-to-lap time delta with sector breakdown
  ldcli data <files...>                Raw time-series samples
  ldcli analyze braking <files...>     Braking hesitation + speed band breakdown
  ldcli analyze throttle <files...>    Throttle delay from apex per speed band
  ldcli analyze tyre <files...>        Tyre temps + front/rear balance per lap
  ldcli compare <files...>             Side-by-side channel stats across sessions
  ldcli report <files...>              HTML or ASCII telemetry report

  ldcli guide                          Full JSON documentation (for LLMs)`)
}

// ---------------------------------------------------------------------------
// Response types — info
// ---------------------------------------------------------------------------

type infoResponse struct {
	Files []fileInfo `json:"files"`
}

type fileInfo struct {
	Path     string     `json:"path"`
	Header   headerInfo `json:"header"`
	Laps     []lapInfo  `json:"laps"`
	Channels []chanInfo `json:"channels"`
}

type headerInfo struct {
	Driver       string `json:"driver"`
	Vehicle      string `json:"vehicle"`
	Venue        string `json:"venue"`
	DateTime     string `json:"datetime"`
	Event        string `json:"event,omitempty"`
	Session      string `json:"session,omitempty"`
	DeviceType   string `json:"device_type,omitempty"`
	DeviceSerial uint32 `json:"device_serial,omitempty"`
	NumChannels  uint32 `json:"num_channels"`
}

type lapInfo struct {
	Number    int     `json:"number"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	LapTime   float64 `json:"lap_time"`
}

type chanInfo struct {
	Name      string `json:"name"`
	ShortName string `json:"short_name,omitempty"`
	Unit      string `json:"unit"`
	Freq      uint16 `json:"freq"`
	Samples   uint32 `json:"samples"`
}

// fileInfoBrief is used by info --brief: header + laps only, no channel list.
type fileInfoBrief struct {
	Path         string     `json:"path"`
	Header       headerInfo `json:"header"`
	Laps         []lapInfo  `json:"laps"`
	ChannelCount int        `json:"channel_count"`
}

type infoBriefResponse struct {
	Files []fileInfoBrief `json:"files"`
}

// ---------------------------------------------------------------------------
// Response types — summarize
// ---------------------------------------------------------------------------

type summaryResponse struct {
	Files    []sumFileData `json:"files"`
	Warnings []string      `json:"warnings,omitempty"`
}

type sumFileData struct {
	Path   string       `json:"path"`
	Laps   []sumLapData `json:"laps"`
	Trends []chanTrend  `json:"trends,omitempty"` // populated when --lap all and >= 3 laps
}

type sumLapData struct {
	Number    *int          `json:"number"`
	StartTime float64       `json:"start_time"`
	EndTime   float64       `json:"end_time"`
	LapTime   float64       `json:"lap_time,omitempty"`
	Channels  []chanSummary `json:"channels"`
}

type chanSummary struct {
	Name      string        `json:"name"`
	Unit      string        `json:"unit"`
	Freq      uint16        `json:"freq,omitempty"`
	N         int           `json:"n"`
	Min       float64       `json:"min"`
	Max       float64       `json:"max"`
	Mean      float64       `json:"mean"`
	Std       float64       `json:"std"`
	P5        float64       `json:"p5"`
	P50       float64       `json:"p50"`
	P95       float64       `json:"p95"`
	Sectors   []sectorStats `json:"sectors,omitempty"`
	Histogram []histBin     `json:"histogram,omitempty"`
}

type histBin struct {
	Lo  float64 `json:"lo"`
	Hi  float64 `json:"hi"`
	Pct float64 `json:"pct"`
	N   int     `json:"n"`
}

// sectorStats holds per-sector stats within a lap window (from --sectors N).
type sectorStats struct {
	Sector int        `json:"sector"`
	TRange [2]float64 `json:"t_range"`
	N      int        `json:"n"`
	Mean   float64    `json:"mean"`
	Min    float64    `json:"min"`
	Max    float64    `json:"max"`
}

// sumDeltaChan holds per-channel statistics delta: comparison lap vs reference lap.
type sumDeltaChan struct {
	Name      string  `json:"name"`
	Unit      string  `json:"unit"`
	MeanRef   float64 `json:"mean_ref"`
	MeanCmp   float64 `json:"mean_cmp"`
	MeanDelta float64 `json:"mean_delta"` // mean_cmp - mean_ref
	MaxRef    float64 `json:"max_ref"`
	MaxCmp    float64 `json:"max_cmp"`
	MinRef    float64 `json:"min_ref"`
	MinCmp    float64 `json:"min_cmp"`
}

type sumDeltaLap struct {
	Number   *int           `json:"number"`
	LapTime  float64        `json:"lap_time,omitempty"`
	Channels []sumDeltaChan `json:"channels"`
}

type sumDeltaFile struct {
	Path   string        `json:"path"`
	RefLap int           `json:"ref_lap"`
	Laps   []sumDeltaLap `json:"laps"`
}

type sumDeltaResponse struct {
	Files    []sumDeltaFile `json:"files"`
	Warnings []string       `json:"warnings,omitempty"`
}

// chanTrend summarises how a channel evolves across multiple laps.
type chanTrend struct {
	Name        string    `json:"name"`
	Unit        string    `json:"unit"`
	Direction   string    `json:"direction"`    // "increasing", "decreasing", "stable"
	SlopePerLap float64   `json:"slope_per_lap"` // in channel units per lap
	LapMeans    []float64 `json:"lap_means"`
	Note        string    `json:"note,omitempty"`
}

// ---------------------------------------------------------------------------
// Response types — data
// ---------------------------------------------------------------------------

type dataResponse struct {
	Files    []fileData `json:"files"`
	Warnings []string   `json:"warnings,omitempty"`
}

type fileData struct {
	Path string    `json:"path"`
	Laps []lapData `json:"laps"`
}

type lapData struct {
	Number    *int       `json:"number"`
	StartTime float64    `json:"start_time"`
	EndTime   float64    `json:"end_time"`
	LapTime   float64    `json:"lap_time,omitempty"`
	Channels  []chanData `json:"channels"`
}

// chanData uses columnar arrays instead of per-sample objects.
//
// LTTB method (default): non-uniform timestamps → T + V parallel arrays.
//   Reconstruct: zip(t, v)
//
// Avg method: uniform timestamps → TStart + TStep + V only.
//   Reconstruct: t[i] = t_start + i * t_step
type chanData struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
	N    int    `json:"n"`

	// lttb: non-uniform — both t and v present
	T []float64 `json:"t,omitempty"`

	// avg: uniform — reconstruct as t[i] = t_start + i*t_step
	TStart *float64 `json:"t_start,omitempty"`
	TStep  *float64 `json:"t_step,omitempty"`

	V []float64 `json:"v"`

	// verbose-only metadata (--verbose flag)
	Freq              uint16 `json:"freq,omitempty"`
	Step              int    `json:"step,omitempty"`
	Method            string `json:"method,omitempty"`
	SmoothWindow      int    `json:"smooth_window,omitempty"`
	HasInvalidSamples bool   `json:"has_invalid_samples,omitempty"`
}

// ---------------------------------------------------------------------------
// Multi-value --ch flag
// ---------------------------------------------------------------------------

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// ---------------------------------------------------------------------------
// Argument splitting — files come before flags
// ---------------------------------------------------------------------------

// splitFilesAndFlags separates leading positional file paths from flag args.
// This lets users write: ldcli data a.ld b.ld --lap 1 --ch "Ground Speed"
func splitFilesAndFlags(args []string) (files []string, flagArgs []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			return args[:i], args[i:]
		}
	}
	return args, nil
}

// ---------------------------------------------------------------------------
// File loading
// ---------------------------------------------------------------------------

type fileResult struct {
	Path string
	File *ldparser.File
	Laps []ldparser.Lap
}

func loadFiles(paths []string) []fileResult {
	if len(paths) == 0 {
		fatalJSON("no files specified")
	}
	out := make([]fileResult, 0, len(paths))
	for _, p := range paths {
		f, err := ldparser.ParseFile(p)
		if err != nil {
			fatalJSON(fmt.Sprintf("cannot parse %q: %v", p, err))
		}
		out = append(out, fileResult{Path: p, File: f, Laps: f.DetectLaps()})
	}
	return out
}

// ---------------------------------------------------------------------------
// Channel matching and lookup
// ---------------------------------------------------------------------------

func matchChannels(f *ldparser.File, patterns []string) []*ldparser.Channel {
	if len(patterns) == 0 || (len(patterns) == 1 && strings.ToLower(patterns[0]) == "all") {
		out := make([]*ldparser.Channel, len(f.Channels))
		for i := range f.Channels {
			out[i] = &f.Channels[i]
		}
		return out
	}
	seen := map[string]bool{}
	var out []*ldparser.Channel
	for _, pat := range patterns {
		for i := range f.Channels {
			ch := &f.Channels[i]
			if seen[ch.Name] {
				continue
			}
			if matchPattern(ch.Name, pat) {
				out = append(out, ch)
				seen[ch.Name] = true
			}
		}
	}
	return out
}

func matchPattern(name, pat string) bool {
	if strings.EqualFold(name, pat) {
		return true
	}
	matched, _ := path.Match(strings.ToLower(pat), strings.ToLower(name))
	return matched
}

// findChannelFuzzy returns the first channel that matches any candidate name.
// It tries exact case-insensitive match first, then substring match.
func findChannelFuzzy(f *ldparser.File, candidates ...string) *ldparser.Channel {
	for _, name := range candidates {
		for i := range f.Channels {
			if strings.EqualFold(f.Channels[i].Name, name) {
				return &f.Channels[i]
			}
		}
	}
	for _, name := range candidates {
		lower := strings.ToLower(name)
		for i := range f.Channels {
			if strings.Contains(strings.ToLower(f.Channels[i].Name), lower) {
				return &f.Channels[i]
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Time slicing
// ---------------------------------------------------------------------------

func sliceChannel(ch *ldparser.Channel, fromSec, toSec float64) ([]float64, float64) {
	if ch.Freq == 0 || len(ch.Data) == 0 {
		return nil, fromSec
	}
	freq := float64(ch.Freq)
	start := int(fromSec * freq)
	end := int(toSec * freq)
	if start < 0 {
		start = 0
	}
	if end > len(ch.Data) {
		end = len(ch.Data)
	}
	if start >= end {
		return nil, fromSec
	}
	return ch.Data[start:end], float64(start) / freq
}

// ---------------------------------------------------------------------------
// Session helpers
// ---------------------------------------------------------------------------

func sessionEnd(fr fileResult) float64 {
	var end float64
	for _, ch := range fr.File.Channels {
		if ch.Freq > 0 {
			if e := float64(ch.DataLen) / float64(ch.Freq); e > end {
				end = e
			}
		}
	}
	return end
}

type lapWindow struct {
	num     *int
	from    float64
	to      float64
	lapTime float64
}

func buildWindows(lapFlag string, laps []ldparser.Lap, fullEnd float64) ([]lapWindow, string) {
	switch {
	case lapFlag == "all":
		ws := make([]lapWindow, len(laps))
		for i, l := range laps {
			n := l.Number
			ws[i] = lapWindow{&n, l.StartTime, l.EndTime, l.LapTime}
		}
		return ws, ""
	case lapFlag != "":
		lapNum, err := strconv.Atoi(lapFlag)
		if err != nil {
			return nil, fmt.Sprintf("invalid --lap value %q", lapFlag)
		}
		for _, l := range laps {
			if l.Number == lapNum {
				n := l.Number
				return []lapWindow{{&n, l.StartTime, l.EndTime, l.LapTime}}, ""
			}
		}
		return nil, fmt.Sprintf("lap %d not found", lapNum)
	default:
		return []lapWindow{{nil, 0, fullEnd, 0}}, ""
	}
}

// ---------------------------------------------------------------------------
// Data reduction
// ---------------------------------------------------------------------------

func movingAverage(src []float64, w int) []float64 {
	if w <= 1 {
		return src
	}
	out := make([]float64, len(src))
	var sum float64
	for i, v := range src {
		sum += v
		if i >= w {
			sum -= src[i-w]
		}
		win := i + 1
		if win > w {
			win = w
		}
		out[i] = sum / float64(win)
	}
	return out
}

func decimate(src []float64, step int) []float64 {
	if step <= 1 {
		return src
	}
	out := make([]float64, 0, (len(src)-1)/step+2)
	for i := 0; i < len(src); i += step {
		out = append(out, src[i])
	}
	if last := len(src) - 1; last%step != 0 {
		out = append(out, src[last])
	}
	return out
}

func computeStep(sliceLen, downsample, maxPoints int) int {
	step := 1
	if downsample > 1 {
		step = downsample
	}
	if maxPoints > 0 && sliceLen > maxPoints {
		auto := (sliceLen + maxPoints - 1) / maxPoints
		if auto > step {
			step = auto
		}
	}
	return step
}

// lttbArrays implements Largest Triangle Three Buckets downsampling.
// Returns parallel t (session-seconds) and v slices of at most n points.
func lttbArrays(src []float64, tStart, freq float64, n int) (ts, vs []float64) {
	if n <= 0 || len(src) == 0 {
		return nil, nil
	}
	if len(src) <= n {
		ts = make([]float64, len(src))
		vs = make([]float64, len(src))
		for i, v := range src {
			ts[i] = round3(tStart + float64(i)/freq)
			vs[i] = round3(sanitize(v))
		}
		return
	}
	ts = make([]float64, 0, n)
	vs = make([]float64, 0, n)
	ts = append(ts, round3(tStart))
	vs = append(vs, round3(sanitize(src[0])))
	bucketSize := float64(len(src)-2) / float64(n-2)
	var prevIdx int
	for i := 0; i < n-2; i++ {
		nextStart := int((float64(i+1)+1)*bucketSize) + 1
		nextEnd := int((float64(i+2)+1)*bucketSize) + 1
		if nextEnd >= len(src) {
			nextEnd = len(src) - 1
		}
		var avgV, avgIdx float64
		for j := nextStart; j <= nextEnd; j++ {
			avgV += src[j]
			avgIdx += float64(j)
		}
		cnt := float64(nextEnd - nextStart + 1)
		avgV /= cnt
		avgIdx /= cnt
		curStart := int((float64(i)+1)*bucketSize) + 1
		curEnd := int((float64(i+1)+1)*bucketSize) + 1
		if curEnd >= len(src) {
			curEnd = len(src) - 1
		}
		maxArea := -1.0
		pickIdx := curStart
		prevV := src[prevIdx]
		pf := float64(prevIdx)
		for j := curStart; j <= curEnd; j++ {
			area := math.Abs((pf-avgIdx)*(src[j]-prevV)-(pf-float64(j))*(avgV-prevV)) * 0.5
			if area > maxArea {
				maxArea = area
				pickIdx = j
			}
		}
		ts = append(ts, round3(tStart+float64(pickIdx)/freq))
		vs = append(vs, round3(sanitize(src[pickIdx])))
		prevIdx = pickIdx
	}
	last := len(src) - 1
	ts = append(ts, round3(tStart+float64(last)/freq))
	vs = append(vs, round3(sanitize(src[last])))
	return
}

func avgArrays(src []float64, tStart, freq float64, smoothW, step int) (tStartOut, tStep float64, vs []float64) {
	reduced := src
	if smoothW > 1 {
		reduced = movingAverage(reduced, smoothW)
	}
	reduced = decimate(reduced, step)
	vs = make([]float64, len(reduced))
	for i, v := range reduced {
		vs[i] = round3(sanitize(v))
	}
	tStartOut = round3(tStart)
	tStep = round3(float64(step) / freq)
	return
}

// ---------------------------------------------------------------------------
// Statistics helpers
// ---------------------------------------------------------------------------

func computeStats(data []float64) (min, max, mean, std, p5, p50, p95 float64, n int) {
	clean := make([]float64, 0, len(data))
	for _, v := range data {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
		}
	}
	n = len(clean)
	if n == 0 {
		return
	}
	min, max = clean[0], clean[0]
	sum := 0.0
	for _, v := range clean {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}
	mean = sum / float64(n)
	sumSq := 0.0
	for _, v := range clean {
		d := v - mean
		sumSq += d * d
	}
	std = math.Sqrt(sumSq / float64(n))
	sorted := make([]float64, n)
	copy(sorted, clean)
	sort.Float64s(sorted)
	p5 = percentile(sorted, 5)
	p50 = percentile(sorted, 50)
	p95 = percentile(sorted, 95)
	return
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p / 100) * float64(len(sorted)-1)
	lo := int(idx)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[lo]
	}
	return sorted[lo]*(1-(idx-float64(lo))) + sorted[hi]*(idx-float64(lo))
}

// linearRegression returns slope and intercept for ys ~ a*xs + b.
func linearRegression(xs, ys []float64) (slope, intercept float64) {
	n := float64(len(xs))
	if n < 2 {
		if n == 1 {
			return 0, ys[0]
		}
		return 0, 0
	}
	var sumX, sumY, sumXY, sumX2 float64
	for i, x := range xs {
		sumX += x
		sumY += ys[i]
		sumXY += x * ys[i]
		sumX2 += x * x
	}
	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		return 0, sumY / n
	}
	slope = (n*sumXY - sumX*sumY) / denom
	intercept = (sumY - slope*sumX) / n
	return
}

// interpolate linearly interpolates y at x using sorted xs/ys arrays.
func interpolate(xs, ys []float64, x float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	if x <= xs[0] {
		return ys[0]
	}
	if x >= xs[n-1] {
		return ys[n-1]
	}
	lo, hi := 0, n-1
	for lo < hi-1 {
		mid := (lo + hi) / 2
		if xs[mid] <= x {
			lo = mid
		} else {
			hi = mid
		}
	}
	if xs[hi] == xs[lo] {
		return ys[lo]
	}
	frac := (x - xs[lo]) / (xs[hi] - xs[lo])
	return ys[lo] + frac*(ys[hi]-ys[lo])
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func sanitize(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func hasInvalid(src []float64) bool {
	for _, v := range src {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return true
		}
	}
	return false
}

func lapStr(n *int) string {
	if n == nil {
		return "null"
	}
	return strconv.Itoa(*n)
}

func fmtLapTime(s float64) string {
	if s <= 0 {
		return "-"
	}
	m := int(s) / 60
	sec := s - float64(m*60)
	return fmt.Sprintf("%d:%06.3f", m, sec)
}

func fatalJSON(msg string) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]string{"error": msg})
	os.Exit(1)
}

func writeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "json encode error: %v\n", err)
	}
}

// ---------------------------------------------------------------------------
// guide command
// ---------------------------------------------------------------------------

func runGuide() {
	guide := map[string]any{
		"tool": "ldcli",
		"description": strings.Join([]string{
			"CLI for .ld race telemetry files — the binary format used by many racing sims and exported by",
			"iRacing, Assetto Corsa (AC), Assetto Corsa Competizione (ACC), Le Mans Ultimate (LMU),",
			"rFactor2, and GT7. Files contain 10–245 channels of time-series data",
			"(speed, throttle, brake, suspension, tyres, GPS, etc.) sampled at 10–100 Hz.",
			"All output is JSON by default (machine-readable). Use --format text for compact human tables.",
			"Start cheap with 'ldcli info --brief', escalate to data/events only when needed.",
		}, " "),

		"cold_session_start": []string{
			"You have one or more .ld files and don't know their contents yet. Do this first:",
			"1. ldcli info <file> --brief --format text   — driver, venue, lap times, channel count (~300 tokens)",
			"2. ldcli info <file> --format text           — add full channel catalogue to find channel names",
			"3. Pick a lap number from step 1. All subsequent commands take --lap N.",
			"Rule: NEVER guess channel names. Always read them from step 2 first.",
		},

		"recommended_workflow": []string{
			"── Orientation (always start here) ──",
			"ldcli info <file> --brief          — lap list + channel count (~300 tokens)",
			"ldcli info <file>                  — full channel catalogue when names are needed (~2 KB)",
			"ldcli inspect <file>               — data quality, channel groups, interesting channels",
			"── Per-lap analysis ──",
			"ldcli summarize <file> --lap N --ch 'Ground Speed,Throttle Pos,Brake Pos'  — key channel stats",
			"ldcli summarize <file> --lap N --ref M   — compact delta vs reference lap (~40 tokens/ch)",
			"ldcli summarize <file> --lap all --trends-only  — session trends only (~20 tokens/ch)",
			"ldcli events <file> --lap N --type braking_zone --format text  — compact braking table",
			"ldcli events <file> --lap N --type corner_apex --format text   — apex speed table",
			"── Lap comparison ──",
			"ldcli diff <file> --ref N --cmp M --cumulative  — sector deltas + 20-point time gap trace",
			"ldcli compare file1.ld file2.ld --lap best      — cross-session channel stats with delta",
			"── Deep coaching analysis ──",
			"ldcli analyze braking <file> --lap N   — hesitation detection + speed-band summary",
			"ldcli analyze throttle <file> --lap N  — apex-to-throttle delay per corner speed band",
			"ldcli analyze tyre <file> --lap all    — tyre temps + front/rear balance per lap",
			"── Raw data (use sparingly) ──",
			"ldcli data <file> --lap N --ch name --max-points 150  — 150-pt LTTB series (~700 tokens/ch)",
			"ldcli data <file> --lap N --ch name --max-points 0    — full resolution (WARNING: can be 200k+ tokens)",
			"── Reporting ──",
			"ldcli report <file> --lap N --ref M               — HTML report with reference lap overlay",
			"ldcli events <file> --lap N --format annotate | xargs -I{} echo '--annotate {}' | xargs ldcli report <file> --lap N",
		},

		"channel_names_by_game": map[string]any{
			"note": "Channel names vary by game/hardware. ALWAYS read from 'ldcli info' before using --ch. Common names:",
			"speed":    map[string]string{"AC/LMU/ADL": "Ground Speed", "GT7": "Ground Speed"},
			"throttle": map[string]string{"AC/LMU/ADL": "Throttle Pos", "GT7": "Throttle Pos"},
			"brake":    map[string]string{"AC/LMU/ADL": "Brake Pos", "GT7": "Brake Pos"},
			"gear":     map[string]string{"AC/LMU/ADL": "Gear", "GT7": "Gear"},
			"lat_g":    map[string]string{"AC/LMU/ADL": "G Force Lat", "GT7": "G Force Lat"},
			"long_g":   map[string]string{"AC/LMU/ADL": "G Force Long", "GT7": "G Force Long"},
			"lap_number": map[string]string{"all": "Lap Number"},
			"tyre_temp":  map[string]string{"GT7": "Tyre Temp FL/FR/RL/RR", "AC": "varies — use inspect to find"},
			"game_notes": []string{
				"LMU: 78 channels, no driver name in header, vehicle in event→venue→vehicle chain, no Lap Time channel (wall-clock used)",
				"AC/Telemetrick: 166 channels, Lap Time is a running timer that resets each lap",
				"GT7: 37 channels, GPS lat/long present, short_comment field may contain conversion metadata",
				"ADL hardware logger: channel names differ from sim exports — use inspect to discover groups",
			},
		},

		"token_estimates": map[string]string{
			"info_brief":                    "~300 tokens",
			"info":                          "1–3 KB per file",
			"inspect":                       "2–5 KB per file",
			"summarize_per_channel_per_lap": "~80 tokens",
			"summarize_ref_delta_per_lap":   "~40 tokens/channel (compact delta only)",
			"summarize_trends_only":         "~20 tokens/channel (no per-lap data)",
			"events_per_lap":                "~50–200 events × ~50 tokens = 2–10 KB",
			"events_text_format":            "~50–200 events × ~15 tokens = 0.5–3 KB (most compact)",
			"diff":                          "~1–3 KB",
			"diff_cumulative":               "+~0.5 KB (20-point delta trace appended)",
			"data_150pt_lttb_per_channel":   "~700 tokens",
			"data_150pt_avg_per_channel":    "~450 tokens (no t array — uniform step)",
			"data_full_100hz_60s":           "~250 000 tokens — always use --max-points",
		},

		"commands": map[string]any{
			"guide": map[string]any{
				"usage":       "ldcli guide",
				"description": "Print this JSON guide.",
			},

			"info": map[string]any{
				"usage":        "ldcli info <files...> [--format json|csv|text] [--brief]",
				"description":  "File metadata: header, lap list, full channel catalogue with units and sample rates. No sample data.",
				"flags":        map[string]string{"--brief": "Omit channel list; output header + laps + channel_count only. Use first to orient without burning tokens on 200+ channel catalogues."},
				"output":       "files[].{ path, header, laps[{number,start_time,end_time,lap_time}], channels[{name,unit,freq,samples}] }",
				"output_brief": "files[].{ path, header, laps[{number,start_time,end_time,lap_time}], channel_count }",
			},

			"inspect": map[string]any{
				"usage":       "ldcli inspect <files...> [--lap N] [--format json|text]",
				"description": "Three-part file overview: (1) data quality per channel, (2) interesting channels worth investigating, (3) channel groups by purpose. Best second step after info.",
				"flags": map[string]string{
					"--lap":    "Restrict quality check to a specific lap (default: full session)",
					"--format": "json (default), text",
				},
				"output": "files[].{ summary, data_quality[{name,issue,recommendation}], interesting_channels[{name,reason,note}], channel_groups{speeds:[],tyres:[],suspension:[],g_forces:[],temperatures:[],pressures:[],engine:[],driver_inputs:[],other:[]} }",
				"issues": map[string]string{
					"constant":     "all samples are the same value — channel carries no information",
					"all_zero":     "all samples are 0.0 — likely disconnected sensor",
					"has_invalid":  "contains NaN or Inf samples",
					"low_variance": "relative std < 1% — mostly flat signal",
					"none":         "channel looks healthy",
				},
				"interesting_reasons": map[string]string{
					"high_variance":         "std/mean > 0.3 — dynamic channel, likely informative",
					"strong_trend_increase": "mean increases significantly across the session",
					"strong_trend_decrease": "mean decreases significantly across the session",
					"driver_input":          "throttle/brake/steering/gear — always useful",
					"primary_speed":         "main vehicle speed channel",
				},
			},

			"summarize": map[string]any{
				"usage":       "ldcli summarize <files...> [flags]",
				"description": "Per-channel statistics per lap. Add --sectors N to see stats per track sector. Use --lap all to get session trends. Use --ref N for compact delta vs a reference lap.",
				"flags": map[string]string{
					"--lap":         "Lap number, 'all', or omit for full session. 'all' also computes session trends.",
					"--ref":         "Reference lap number. Switches to delta mode: outputs mean/min/max delta (cmp-ref) instead of raw stats. Compatible with --lap all or --lap N.",
					"--trends-only":    "Suppress per-lap data; output trends[] only. Requires --lap all. Saves ~95% of tokens vs --lap all with many channels.",
					"--ch":             "Channel name or glob (repeatable). Default: all channels",
					"--sectors":        "Split each lap into N equal time sectors (default: 0 = no sectors)",
					"--from":           "Start time in seconds",
					"--to":             "End time in seconds",
					"--verbose":        "Include channel freq in output",
					"--format":         "json (default), csv, text",
					"--histogram":      "Add histogram[] to each channel: % of time in each value band.",
					"--histogram-bins": "Number of bins for --histogram (default 10)",
				},
				"output":             "files[].{ laps[{number, channels[{name,unit,n,min,max,mean,std,p5,p50,p95, sectors?[], histogram?[{lo,hi,pct,n}]}]}], trends?[{name,direction,slope_per_lap,lap_means}] }",
				"output_ref_delta":   "files[].{ ref_lap, laps[{number, lap_time, channels[{name,unit,mean_ref,mean_cmp,mean_delta,max_ref,max_cmp,min_ref,min_cmp}]}] }",
				"output_trends_only": "files[].{ trends[{name,direction,slope_per_lap,lap_means}] } — laps[] is empty",
				"trends_note":        "trends[] only appears when --lap all is used and >= 3 laps exist. direction is 'increasing', 'decreasing', or 'stable' (|slope| < 5% of mean).",
				"sectors_note":       "sectors[] in each channel splits the lap window into N equal time segments with their own min/max/mean.",
			},

			"events": map[string]any{
				"usage":       "ldcli events <files...> [--lap N|all] [--type TYPE] [--format json|text|csv]",
				"description": "Detect driving events from channel data. Events are sorted by time within each lap.",
				"flags": map[string]string{
					"--lap":    "Lap number, 'all' (default), or omit for full session",
					"--type":   "Filter to one event type: braking_zone, corner_apex, full_throttle_zone, gear_shift, lockup",
					"--format": "json (default), text (compact per-type numeric tables), csv, annotate (time:label lines for --annotate piping)",
				},
				"text_format_note":     "text format groups events by type with numeric column headers — most token-efficient for single event type analysis",
				"annotate_format_note": "annotate format outputs one 'seconds:label' line per event, ready to pipe into 'ldcli report --annotate'. Example: ldcli events file.ld --lap 3 --type braking_zone --format annotate | xargs -I{} echo '--annotate {}' | xargs ldcli report file.ld --lap 3",
				"output": "files[].laps[{number,event_count,events[{type,t,t_end?,duration?,note,...}]}]",
				"event_types": map[string]any{
					"gear_shift": map[string]string{
						"fields":    "gear_from, gear_to, direction (up/down)",
						"detection": "gear channel integer value change (sustained ≥2 samples to filter glitches)",
					},
					"braking_zone": map[string]string{
						"fields":    "speed_entry, speed_exit, peak_decel_g, peak_brake_pct, t_end, duration",
						"detection": "brake pressure > 10% of session max, sustained ≥ 0.3 s",
					},
					"corner_apex": map[string]string{
						"fields":    "speed, lat_g",
						"detection": "local speed minimum while lateral G > 0.5 G (or speed < 70% of max if no lat_g channel)",
					},
					"full_throttle_zone": map[string]string{
						"fields":    "t_end, duration",
						"detection": "throttle > 95%, sustained ≥ 1.0 s",
					},
					"lockup": map[string]string{
						"fields":    "slip_ratio, wheel (FL/FR/RL/RR)",
						"detection": "wheel speed < vehicle speed × 0.85 for ≥ 0.1 s",
					},
				},
				"note": "Event detection degrades gracefully — if a required channel is missing for a given event type, that type is skipped and listed in warnings.",
			},

			"diff": map[string]any{
				"usage":       "ldcli diff <file> [file2] --ref N --cmp M [--sectors N] [--ch names] [--cumulative] [--cum-points N] [--format json|text]",
				"description": "Compare two laps: total delta, per-sector time delta, and per-channel mean comparison. If two files are given, --ref applies to file1 and --cmp applies to file2.",
				"flags": map[string]string{
					"--ref":        "Reference lap number",
					"--cmp":        "Comparison lap number",
					"--sectors":    "Number of equal sectors (default 4)",
					"--ch":         "Channels to include in channel_deltas (default: speed + throttle + brake)",
					"--cumulative": "Add cumulative_delta: running time gap at N evenly-spaced points across the lap. Shows WHERE the gap opens and closes.",
					"--cum-points": "Number of points in cumulative delta trace (default 20)",
					"--format":     "json (default), text",
				},
				"output":                "{ reference, comparison, delta_total, delta_note, alignment, sectors[], channels[], cumulative_delta? }",
				"cumulative_delta_note": "cumulative_delta.delta[i] is the running time gap at t_ref[i]. Positive = comparison is behind. A rising value means time is being lost; falling means time is being gained back.",
				"alignment_note":        "alignment='lap_distance' when 'Lap Distance' channel exists (accurate distance-based). alignment='time_fraction' otherwise (time-proportional estimate).",
				"sector_delta":          "positive delta = comparison lap is SLOWER in that sector. negative = comparison is FASTER.",
				"channel_delta":         "mean_delta = mean_cmp - mean_ref. worst_point shows the single sample with largest absolute delta.",
			},

			"data": map[string]any{
				"usage":       "ldcli data <files...> [flags]",
				"description": "Time-series samples. Default: 150-point LTTB per channel. Columnar output minimises tokens.",
				"flags": map[string]string{
					"--lap":        "Lap number, 'all', or omit for full session",
					"--ch":         "Channel name or glob (repeatable). Default: all channels",
					"--max-points": "Points per channel. Default 150. Use 0 for unlimited (WARNING: a warning is emitted when output exceeds 5000 total samples).",
					"--method":     "'lttb' (default): shape-preserving, non-uniform t. 'avg': moving-avg + uniform decimate.",
					"--smooth":     "Moving-average window (avg method only)",
					"--downsample": "Keep 1 of every N samples",
					"--from":       "Start time in seconds",
					"--to":         "End time in seconds",
					"--verbose":    "Add freq/step/method metadata to each channel",
					"--format":     "json (default), csv, text, ndjson",
				},
				"output":        "files[].{ path, laps[{ number, start_time, end_time, lap_time, channels[{name,unit,n,...}] }] }",
				"output_note":   "channels are always nested under laps[], even when --from/--to is used without --lap. A window without a lap number has number:null. Do NOT access files[0].channels — it does not exist.",
				"output_lttb":   "channel fields: name, unit, n, t:[float...], v:[float...]  — t[i] and v[i] are paired",
				"output_avg":    "channel fields: name, unit, n, t_start:float, t_step:float, v:[float...]  — t[i] = t_start + i*t_step",
				"ndjson_format": "one line per sample: {f,lap,ch,unit,t,v}",
			},

			"report": map[string]any{
				"usage":       "ldcli report <files...> [flags]",
				"description": "Generate a self-contained HTML (or ASCII) telemetry report with channel traces and event overlays. The LLM can annotate specific moments using --annotate.",
				"flags": map[string]string{
					"--lap":      "Lap number or 'all' (default: all laps)",
					"--out":      "Output filename (default: report.html; ASCII prints to stdout)",
					"--format":   "html (default) or ascii",
					"--ch":       "Channel name to include (repeatable). Default: speed, throttle, brake, gear.",
					"--annotate": `Labeled time marker: "seconds:label" or "seconds:label:#color" (repeatable). Renders as a colored dashed vertical line with label in HTML (default yellow #ffdd00), and ^ in ASCII event row.`,
					"--ref":      "Reference lap number to overlay as dashed traces on the same HTML charts (time-fraction aligned). Enables visual two-lap comparison without opening two tabs.",
				},
				"annotate_example": `ldcli report file.ld --lap 3 --annotate "47.2:Late braking T1:#ff3333" --annotate "112.8:Oversteer snap:#00ccff"`,
				"annotate_note":    "Times are session-relative seconds (same scale as all other ldcli output). Labels are truncated to 20 chars in HTML and rendered at 45° — keep them SHORT (≤12 chars ideal, 20 max) or they will overlap neighbouring annotations and clip outside the SVG. Use multiple --annotate flags for multiple markers. Markers outside the rendered lap window are silently ignored. Optional trailing :#hex sets the annotation color (line, triangle, label); defaults to #ffdd00 (yellow) if omitted.",
				"output_html":      "Self-contained HTML file with inline SVGs — one chart per channel per lap, event overlays (braking zones, gear shifts, apexes, lockups), and annotation markers.",
				"output_ascii":     "Terminal-friendly block-character charts with event/annotation markers in a shared row. Annotation legend printed below each chart.",
			},

			"analyze": map[string]any{
				"usage":       "ldcli analyze braking|throttle|tyre|median <files...> [flags]",
				"description": "Deep analysis of specific driving aspects. Each subcommand is purpose-built for a coaching workflow that previously required manual raw-data inspection.",
				"subcommands": map[string]any{
					"braking": map[string]any{
						"usage":       "ldcli analyze braking <files...> [--lap N|all] [--format json|text]",
						"description": "Classify each braking zone as clean or hesitant (stab-release-reapply). Groups by entry speed band.",
						"output":      "files[].laps[].{ zones[{t,t_end,duration,entry_speed,exit_speed,peak_brake_pct,time_to_peak_s,hesitant,hesitation_type}], speed_bands[{band,count,hesitant_count,hesitant_pct,mean_time_to_peak_s}], summary{total_zones,hesitant_zones,hesitant_pct,mean_time_to_peak_s} }",
						"hesitation":  "hesitant=true when a local minimum below 50% of peak pressure occurs before the peak (stab_release pattern). time_to_peak_s measures ramp-up quality.",
					},
					"throttle": map[string]any{
						"usage":       "ldcli analyze throttle <files...> [--lap N|all] [--format json|text]",
						"description": "Measure time from speed minimum (apex) to throttle thresholds (20%, 50%, 90%). Groups by apex speed band. Reveals throttle timidity at specific corner types.",
						"output":      "files[].laps[].{ apexes[{t,apex_speed,delay_20_pct_s,delay_50_pct_s,delay_90_pct_s}], speed_bands[{band,count,mean_delay_20_pct_s,mean_delay_50_pct_s,mean_delay_90_pct_s}] }",
						"note":        "Delays are null when threshold not reached within 5s of apex. Group by speed band to find corner types with worst throttle application.",
					},
					"tyre": map[string]any{
						"usage":       "ldcli analyze tyre <files...> [--lap N|all] [--format json|text]",
						"description": "Tyre temperature analysis: inner/outer/core spread per corner, front-rear and left-right balance per lap, trend across laps.",
						"output":      "files[].laps[].{ corners{FL,FR,RL,RR:{inner_mean,outer_mean,core_mean,spread}}, balance{front_mean,rear_mean,front_rear_delta,left_mean,right_mean,left_right_delta} }",
						"note":        "Channel detection is fuzzy — scans for 'tyre temp'/'tire temp'/'wheel temp' patterns. detected_channels lists what was mapped. Any corner with no matching channels is omitted. Maps ONE channel per corner (typically surface outer). Surface temps are noisy — check carcass and rubber/contact-patch channels separately via summarize --ch for true tyre state. Tyre pressure channels also require manual read.",
					},
					"median": map[string]any{
						"usage":       "ldcli analyze median <files...> [--threshold ms] [--bins N] [--ch channels]",
						"description": "Builds a synthetic median lap: pointwise median of all complete laps within ±threshold ms of best lap. Distance-aligned so throttle/brake curves overlap exactly. Reveals patterns you repeat on every lap.",
						"output":      "files[].median_lap.{ median_sources[lap numbers], median_source_times[], best_lap_time, median_lap_time, distance_bins, total_distance_m, speed_channel, channels[{name,unit,values[bins]}] }",
						"flags": map[string]string{
							"--threshold": "±ms around best lap time for lap selection (default 100). Increase to 500+ if laps are spread.",
							"--bins":      "Distance resampling resolution (default 1000).",
							"--ch":        "Comma-separated channel names or globs (default: all channels).",
						},
						"values_index": "channels[].values[i] = median value at distance fraction i/(bins-1). values[0]=start, values[bins-1]=end.",
						"note":         "Always check median_sources count: 1 lap = no median effect. ≥3 laps is good. One-off outliers on a single lap are suppressed; repeating patterns are amplified.",
						"example":      "ldcli analyze median session.ld --threshold 500 --ch 'Brake Pos,Throttle Pos,Ground Speed'",
					},
					"deviation": map[string]any{
						"usage":          "ldcli analyze deviation <files...> [--threshold ms] [--bins N] [--ch channels]",
						"description":    "Speed deviation diagram: pointwise standard deviation of speed across best laps within a % threshold of best lap time. Shows WHERE on track your best laps differ — the consistency fingerprint of a driver. Flat line = consistent. Peaks = inconsistency zones.",
						"output":         "files[].deviation_lap.{ median_sources, median_source_times, best_lap_time, distance_bins, total_distance_m, channels[{name,unit,values[bins],stddev[bins]}] }",
						"key_field":      "channels[].stddev — the deviation trace. stddev[i] = stddev of channel values across contributing laps at distance bin i.",
						"flags": map[string]string{
							"--threshold": "±ms around best lap time (default 300). On a 90s lap 300ms = top ~0.3%. Tight window keeps only your most consistent best laps.",
							"--bins":          "Distance resampling resolution (default 1000).",
							"--ch":            "Comma-separated channel names or globs. Default: speed channel only.",
						},
						"interpretation": "Bins with highest stddev = inconsistency zones. Near-zero on straights is normal. Peaks at corner entries = braking point variation. Peaks at exits = throttle application variation.",
						"example":        "ldcli analyze deviation session.ld --threshold 300 --ch 'Ground Speed,Brake Pos,Throttle Pos'",
					},
				},
			},

			"compare": map[string]any{
				"usage":       "ldcli compare <file1> [file2] [--ch name...] [--lap N|best] [--format json|text]",
				"description": "Side-by-side channel statistics across two sessions or laps. Default --lap best picks the fastest lap per file.",
				"flags": map[string]string{
					"--lap": "Lap number or 'best' (fastest lap, default). Applied to each file.",
					"--ch":  "Channel name (repeatable). Default: speed, throttle, brake, gear.",
					"--format": "json (default), text",
				},
				"output":     "{ channels[{name,unit,laps[{path,lap,lap_time,mean,min,max,std,p50,n}],delta?{mean,max,min,std,p50}}] }",
				"delta_note": "delta = laps[1] - laps[0]. Only present when both laps have data. Positive = second file/lap is higher.",
				"example":    "ldcli compare session1.ld session2.ld --ch 'Brake Pos' --lap best --format text",
			},
		},

			"map": map[string]any{
				"usage":       "ldcli map <files...> [--lap N|best|all] [--width N] [--height N] [--mark dist:label[:color]...]",
				"description": "Renders a track map as a self-contained SVG, path colored by speed (blue=slow → red=fast). Auto-detects position source: Car Coord X/Y (world metres) or GPS Latitude/Longitude. Equal-axis scaling preserves track shape.",
				"output":      "files[].{ svg, position_source, laps_used[], total_distance_m, marks[] }",
				"flags": map[string]string{
					"--lap":    "Lap to draw: number, 'best' (default), or 'all' (overlays all valid laps).",
					"--width":  "SVG canvas width in pixels (default 800).",
					"--height": "SVG canvas height in pixels (default 600).",
					"--mark":   "Annotation: 'dist_frac:label' or 'dist_frac:label:color'. dist_frac=0..1 along lap by distance. Repeatable.",
				},
				"position_sources": map[string]string{
					"world_coords": "Car Coord X/Y channels (metres). Used by Assetto Corsa. No projection needed.",
					"gps":          "GPS Latitude/Longitude (degrees). Used by GT7, LMU, hardware loggers. Equirectangular projection applied.",
				},
				"annotation_workflow": "1. Run without --mark to get the raw map. 2. Find corner dist_frac: run `ldcli data file.ld --lap best --ch 'Ground Speed' --max-points 200`, speed minima index/200 = dist_frac. 3. Re-run with --mark per corner.",
				"mark_colors":         "Any CSS hex color. Suggestions: T1=#e74c3c (red), T2=#3498db (blue), chicane=#9b59b6 (purple), incident=#e67e22 (orange). Default: #f1c40f (yellow).",
				"example":             "ldcli map session.ld --lap best --mark '0.12:T1:#e74c3c' --mark '0.31:T2' --mark '0.58:T3:#3498db'",
			},

		"general_notes": []string{
			"All timestamps are session-relative seconds (float, 3 dp).",
			"All floats rounded to 3 decimal places.",
			"Multiple files: pass multiple paths; output always has a top-level 'files' array.",
			"Glob channels: --ch 'Tyre*' matches all tyre channels (case-insensitive).",
			"Lap 0 is always the outlap (session start to first lap crossing). Last lap is often a partial inlap.",
			"'warnings' lists non-fatal issues (e.g. missing channels, skipped event types). Always check it.",
			"Errors are JSON: {\"error\":\"...\"} exit 1.",
			"NEVER guess channel names — they vary by game and hardware. Read them from 'ldcli info' first.",
			"Best lap for analysis: use 'ldcli info --brief' to find the fastest complete lap, then --lap N.",
			"For cross-session comparison always use --lap best to auto-select fastest lap per file.",
		},

		"token_traps": []string{
			"TRAP: guessing channel names. AC uses 'Brake Pos', some hardware uses 'Brake Pressure'. Always info first.",
			"TRAP: ldcli info on LMU/rF2 files lists 200+ channels. Use --brief first (~300 tokens vs 3 KB+).",
			"TRAP: summarize --lap all × 200 channels × 22 laps = 30 000+ tokens. Filter with --ch or use --trends-only.",
			"TRAP: data --max-points 0 on a full race = 200 000+ tokens. A warnings[] entry appears when output > 5000 samples.",
			"TRAP: summarize --lap all puts trends[] at the BOTTOM. Use --trends-only to skip the per-lap data.",
			"TRAP: events without --type returns all event types. Use --type braking_zone or --format text to reduce output.",
			"TRAP: lap 0 (outlap) and the last lap (inlap) are usually incomplete. Exclude them from analysis.",
			"TRAP: analyze tyre only maps one surface temp channel per corner. Carcass and rubber/contact-patch temps are not included — read them with summarize --ch. Surface outer alone understates tyre state by 10–20°C vs carcass on a working tyre.",
		},
	}
	writeJSON(guide)
}

// ---------------------------------------------------------------------------
// info command
// ---------------------------------------------------------------------------

func runInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	format := fs.String("format", "json", "")
	briefFlag := fs.Bool("brief", false, "")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)
	files := loadFiles(filePaths)

	// Build header + laps for every file (always needed).
	type parsed struct {
		fr       fileResult
		hi       headerInfo
		lapInfos []lapInfo
	}
	parsedFiles := make([]parsed, len(files))
	for i, fr := range files {
		h := fr.File.Header
		hi := headerInfo{
			Driver:       h.Driver,
			Vehicle:      h.VehicleID,
			Venue:        h.Venue,
			DateTime:     h.DateTime.Format("2006-01-02T15:04:05Z"),
			DeviceType:   h.DeviceType,
			DeviceSerial: h.DeviceSerial,
			NumChannels:  h.NumChannels,
		}
		if h.Event != nil {
			hi.Event = h.Event.Name
			hi.Session = h.Event.Session
			if h.Event.Venue != nil && h.Event.Venue.Vehicle != nil {
				veh := h.Event.Venue.Vehicle
				if hi.Vehicle == "" {
					if veh.LongName != "" {
						hi.Vehicle = veh.LongName
					} else {
						hi.Vehicle = veh.ID
					}
				}
			}
		}
		lapInfos := make([]lapInfo, len(fr.Laps))
		for j, l := range fr.Laps {
			lapInfos[j] = lapInfo{l.Number, round3(l.StartTime), round3(l.EndTime), round3(l.LapTime)}
		}
		parsedFiles[i] = parsed{fr, hi, lapInfos}
	}

	if *briefFlag {
		briefs := make([]fileInfoBrief, len(files))
		for i, p := range parsedFiles {
			briefs[i] = fileInfoBrief{
				Path:         p.fr.Path,
				Header:       p.hi,
				Laps:         p.lapInfos,
				ChannelCount: len(p.fr.File.Channels),
			}
		}
		writeJSON(infoBriefResponse{Files: briefs})
		return
	}

	resp := infoResponse{Files: make([]fileInfo, len(files))}
	for i, p := range parsedFiles {
		chanInfos := make([]chanInfo, len(p.fr.File.Channels))
		for j, ch := range p.fr.File.Channels {
			chanInfos[j] = chanInfo{ch.Name, ch.ShortName, ch.Unit, ch.Freq, ch.DataLen}
		}
		resp.Files[i] = fileInfo{p.fr.Path, p.hi, p.lapInfos, chanInfos}
	}

	switch *format {
	case "csv":
		writeInfoCSV(&resp)
	case "laps":
		writeInfoLaps(&resp)
	case "text":
		writeInfoText(&resp)
	default:
		writeJSON(resp)
	}
}

// ---------------------------------------------------------------------------
// summarize command
// ---------------------------------------------------------------------------

func runSummarize(args []string) {
	fs := flag.NewFlagSet("summarize", flag.ExitOnError)
	format := fs.String("format", "json", "")
	lapFlag := fs.String("lap", "", "")
	refFlag := fs.Int("ref", -1, "")
	trendsOnly := fs.Bool("trends-only", false, "")
	var chFlags multiFlag
	fs.Var(&chFlags, "ch", "")
	fromFlag := fs.Float64("from", math.NaN(), "")
	toFlag := fs.Float64("to", math.NaN(), "")
	sectorsFlag := fs.Int("sectors", 0, "")
	verbose := fs.Bool("verbose", false, "")
	histFlag := fs.Bool("histogram", false, "")
	histBinsFlag := fs.Int("histogram-bins", 10, "")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)
	files := loadFiles(filePaths)

	// Delta mode: --ref N computes per-channel delta vs reference lap.
	if *refFlag >= 0 {
		runSummarizeDelta(files, *refFlag, *lapFlag, []string(chFlags))
		return
	}

	var warnings []string
	resp := summaryResponse{Files: make([]sumFileData, 0, len(files))}

	for _, fr := range files {
		channels := matchChannels(fr.File, []string(chFlags))
		if len(channels) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: no channels matched", fr.Path))
			continue
		}
		windows, errMsg := buildWindows(*lapFlag, fr.Laps, sessionEnd(fr))
		if errMsg != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s", fr.Path, errMsg))
			continue
		}
		if !math.IsNaN(*fromFlag) || !math.IsNaN(*toFlag) {
			for i := range windows {
				if !math.IsNaN(*fromFlag) {
					windows[i].from = *fromFlag
				}
				if !math.IsNaN(*toFlag) {
					windows[i].to = *toFlag
				}
			}
		}

		fd := sumFileData{Path: fr.Path, Laps: make([]sumLapData, 0, len(windows))}

		// Per-channel mean per lap (for trend computation)
		lapMeansByChannel := map[string][]float64{}

		for _, win := range windows {
			ld := sumLapData{
				Number:    win.num,
				StartTime: round3(win.from),
				EndTime:   round3(win.to),
				LapTime:   round3(win.lapTime),
				Channels:  make([]chanSummary, 0, len(channels)),
			}
			for _, ch := range channels {
				raw, tStart := sliceChannel(ch, win.from, win.to)
				if len(raw) == 0 {
					continue
				}
				mn, mx, mean, std, p5, p50, p95, n := computeStats(raw)
				cs := chanSummary{
					Name: ch.Name, Unit: ch.Unit, N: n,
					Min: round3(mn), Max: round3(mx), Mean: round3(mean), Std: round3(std),
					P5: round3(p5), P50: round3(p50), P95: round3(p95),
				}
				if *verbose {
					cs.Freq = ch.Freq
				}
				if *sectorsFlag > 1 {
					cs.Sectors = computeSectors(raw, tStart, win.from, win.to, ch.Freq, *sectorsFlag)
				}
				if *histFlag {
					cs.Histogram = computeHistogram(raw, *histBinsFlag)
				}
				ld.Channels = append(ld.Channels, cs)
				if win.num != nil {
					lapMeansByChannel[ch.Name] = append(lapMeansByChannel[ch.Name], mean)
				}
			}
			fd.Laps = append(fd.Laps, ld)
		}

		// --trends-only: discard per-lap data, keep only trends.
		if *trendsOnly {
			if *lapFlag != "all" {
				warnings = append(warnings, fmt.Sprintf("%s: --trends-only requires --lap all; ignored", fr.Path))
			} else {
				fd.Laps = nil
			}
		}

		// Compute trends when --lap all and >= 3 laps
		if *lapFlag == "all" && len(windows) >= 3 {
			xs := make([]float64, len(windows))
			for i := range windows {
				xs[i] = float64(i)
			}
			for _, ch := range channels {
				means, ok := lapMeansByChannel[ch.Name]
				if !ok || len(means) < 3 {
					continue
				}
				slope, _ := linearRegression(xs[:len(means)], means)
				mean := means[0]
				for _, m := range means[1:] {
					mean += m
				}
				mean /= float64(len(means))
				dir := "stable"
				if mean != 0 && math.Abs(slope/mean) > 0.05 {
					if slope > 0 {
						dir = "increasing"
					} else {
						dir = "decreasing"
					}
				}
				roundedMeans := make([]float64, len(means))
				for i, m := range means {
					roundedMeans[i] = round3(m)
				}
				note := fmt.Sprintf("%.3g %s per lap", slope, ch.Unit)
				fd.Trends = append(fd.Trends, chanTrend{
					Name:        ch.Name,
					Unit:        ch.Unit,
					Direction:   dir,
					SlopePerLap: round3(slope),
					LapMeans:    roundedMeans,
					Note:        note,
				})
			}
		}

		resp.Files = append(resp.Files, fd)
	}
	resp.Warnings = warnings

	switch *format {
	case "csv":
		writeSumCSV(&resp)
	case "text":
		writeSumText(&resp)
	default:
		writeJSON(resp)
	}
}

// computeSectors divides a channel's data into nSectors equal time windows
// and returns stats for each sector.
func computeHistogram(data []float64, bins int) []histBin {
	if bins <= 0 || len(data) == 0 {
		return nil
	}
	mn, mx := data[0], data[0]
	for _, v := range data {
		sv := sanitize(v)
		if sv < mn {
			mn = sv
		}
		if sv > mx {
			mx = sv
		}
	}
	if mn == mx {
		return []histBin{{Lo: round3(mn), Hi: round3(mx), Pct: 100, N: len(data)}}
	}
	width := (mx - mn) / float64(bins)
	counts := make([]int, bins)
	valid := 0
	for _, v := range data {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		sv := sanitize(v)
		idx := int((sv - mn) / width)
		if idx >= bins {
			idx = bins - 1
		}
		counts[idx]++
		valid++
	}
	out := make([]histBin, bins)
	for i := range out {
		lo := mn + float64(i)*width
		hi := lo + width
		pct := 0.0
		if valid > 0 {
			pct = round3(100 * float64(counts[i]) / float64(valid))
		}
		out[i] = histBin{Lo: round3(lo), Hi: round3(hi), Pct: pct, N: counts[i]}
	}
	return out
}

func computeSectors(raw []float64, tStart, winFrom, winTo float64, freq uint16, nSectors int) []sectorStats {
	if nSectors < 1 || len(raw) == 0 {
		return nil
	}
	duration := winTo - winFrom
	secDur := duration / float64(nSectors)
	f := float64(freq)
	sectors := make([]sectorStats, 0, nSectors)
	for s := 0; s < nSectors; s++ {
		secFrom := winFrom + float64(s)*secDur
		secTo := winFrom + float64(s+1)*secDur
		startIdx := int((secFrom - tStart) * f)
		endIdx := int((secTo - tStart) * f)
		if startIdx < 0 {
			startIdx = 0
		}
		if endIdx > len(raw) {
			endIdx = len(raw)
		}
		if startIdx >= endIdx {
			continue
		}
		slice := raw[startIdx:endIdx]
		mn, mx, mean, _, _, _, _, n := computeStats(slice)
		sectors = append(sectors, sectorStats{
			Sector: s + 1,
			TRange: [2]float64{round3(secFrom), round3(secTo)},
			N:      n,
			Mean:   round3(mean),
			Min:    round3(mn),
			Max:    round3(mx),
		})
	}
	return sectors
}

// ---------------------------------------------------------------------------
// summarize --ref delta mode
// ---------------------------------------------------------------------------

// runSummarizeDelta outputs per-channel statistics delta (comparison lap vs reference lap).
// When lapFlag is "all" or empty, all laps except the ref lap are compared.
func runSummarizeDelta(files []fileResult, refLapNum int, lapFlag string, chNames []string) {
	if lapFlag == "" {
		lapFlag = "all"
	}
	var warnings []string
	resp := sumDeltaResponse{Files: make([]sumDeltaFile, 0, len(files))}

	for _, fr := range files {
		channels := matchChannels(fr.File, chNames)
		if len(channels) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: no channels matched", fr.Path))
			continue
		}

		refLap := findLap(fr.Laps, refLapNum)
		if refLap == nil {
			warnings = append(warnings, fmt.Sprintf("%s: ref lap %d not found", fr.Path, refLapNum))
			continue
		}

		// Pre-compute ref stats per channel.
		type refStat struct{ mn, mx, mean float64 }
		refStats := make(map[string]refStat, len(channels))
		for _, ch := range channels {
			raw, _ := sliceChannel(ch, refLap.StartTime, refLap.EndTime)
			if len(raw) == 0 {
				continue
			}
			mn, mx, mean, _, _, _, _, _ := computeStats(raw)
			refStats[ch.Name] = refStat{round3(mn), round3(mx), round3(mean)}
		}

		windows, errMsg := buildWindows(lapFlag, fr.Laps, sessionEnd(fr))
		if errMsg != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s", fr.Path, errMsg))
			continue
		}

		df := sumDeltaFile{Path: fr.Path, RefLap: refLapNum}
		for _, win := range windows {
			if win.num != nil && *win.num == refLapNum {
				continue // skip ref lap itself
			}
			dl := sumDeltaLap{Number: win.num, LapTime: round3(win.lapTime)}
			for _, ch := range channels {
				rs, ok := refStats[ch.Name]
				if !ok {
					continue
				}
				raw, _ := sliceChannel(ch, win.from, win.to)
				if len(raw) == 0 {
					continue
				}
				mn, mx, mean, _, _, _, _, _ := computeStats(raw)
				dl.Channels = append(dl.Channels, sumDeltaChan{
					Name:      ch.Name,
					Unit:      ch.Unit,
					MeanRef:   rs.mean,
					MeanCmp:   round3(mean),
					MeanDelta: round3(mean - rs.mean),
					MaxRef:    rs.mx,
					MaxCmp:    round3(mx),
					MinRef:    rs.mn,
					MinCmp:    round3(mn),
				})
			}
			df.Laps = append(df.Laps, dl)
		}
		resp.Files = append(resp.Files, df)
	}
	resp.Warnings = warnings
	writeJSON(resp)
}

// ---------------------------------------------------------------------------
// data command
// ---------------------------------------------------------------------------

func runData(args []string) {
	fs := flag.NewFlagSet("data", flag.ExitOnError)
	format := fs.String("format", "json", "")
	lapFlag := fs.String("lap", "", "")
	var chFlags multiFlag
	fs.Var(&chFlags, "ch", "")
	fromFlag := fs.Float64("from", math.NaN(), "")
	toFlag := fs.Float64("to", math.NaN(), "")
	smoothFlag := fs.Int("smooth", 0, "")
	downsampleFlag := fs.Int("downsample", 0, "")
	maxPointsFlag := fs.Int("max-points", 150, "")
	methodFlag := fs.String("method", "lttb", "")
	verbose := fs.Bool("verbose", false, "")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)
	files := loadFiles(filePaths)

	var warnings []string
	resp := dataResponse{Files: make([]fileData, 0, len(files))}

	for _, fr := range files {
		channels := matchChannels(fr.File, []string(chFlags))
		if len(channels) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: no channels matched", fr.Path))
			continue
		}
		windows, errMsg := buildWindows(*lapFlag, fr.Laps, sessionEnd(fr))
		if errMsg != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s", fr.Path, errMsg))
			continue
		}
		if !math.IsNaN(*fromFlag) || !math.IsNaN(*toFlag) {
			for i := range windows {
				if !math.IsNaN(*fromFlag) {
					windows[i].from = *fromFlag
				}
				if !math.IsNaN(*toFlag) {
					windows[i].to = *toFlag
				}
			}
		}

		fd := fileData{Path: fr.Path, Laps: make([]lapData, 0, len(windows))}
		for _, win := range windows {
			ld := lapData{
				Number:    win.num,
				StartTime: round3(win.from),
				EndTime:   round3(win.to),
				LapTime:   round3(win.lapTime),
				Channels:  make([]chanData, 0, len(channels)),
			}
			for _, ch := range channels {
				raw, tStart := sliceChannel(ch, win.from, win.to)
				if len(raw) == 0 {
					continue
				}
				freq := float64(ch.Freq)
				invalid := hasInvalid(raw)
				step := computeStep(len(raw), *downsampleFlag, *maxPointsFlag)
				method := strings.ToLower(*methodFlag)

				cd := chanData{Name: ch.Name, Unit: ch.Unit}
				if *verbose {
					cd.Freq = ch.Freq
					cd.Step = step
					cd.Method = method
					cd.HasInvalidSamples = invalid
					if *smoothFlag > 1 && method == "avg" {
						cd.SmoothWindow = *smoothFlag
					}
				} else if invalid {
					cd.HasInvalidSamples = true
				}

				if method == "lttb" {
					n := len(raw)
					if *maxPointsFlag > 0 && *maxPointsFlag < n {
						n = *maxPointsFlag
					} else if *downsampleFlag > 1 {
						n = len(raw) / *downsampleFlag
					}
					if n < 2 {
						n = 2
					}
					cd.T, cd.V = lttbArrays(raw, tStart, freq, n)
					cd.N = len(cd.V)
				} else {
					ts, tstep, vs := avgArrays(raw, tStart, freq, *smoothFlag, step)
					cd.TStart = &ts
					cd.TStep = &tstep
					cd.V = vs
					cd.N = len(vs)
				}
				ld.Channels = append(ld.Channels, cd)
			}
			fd.Laps = append(fd.Laps, ld)
		}
		resp.Files = append(resp.Files, fd)
	}

	// Warn when --max-points 0 (unlimited) produces a very large response.
	if *maxPointsFlag == 0 {
		total := 0
		for _, fd := range resp.Files {
			for _, ld := range fd.Laps {
				for _, cd := range ld.Channels {
					total += cd.N
				}
			}
		}
		if total > 5000 {
			warnings = append(warnings, fmt.Sprintf("--max-points 0 produced %d total samples — this response may be very large (consider --max-points 500 or lower)", total))
		}
	}

	resp.Warnings = warnings

	switch *format {
	case "csv":
		writeDataCSV(&resp)
	case "text":
		writeDataText(&resp)
	case "ndjson":
		writeDataNDJSON(&resp)
	default:
		writeJSON(resp)
	}
}

// ---------------------------------------------------------------------------
// Output formatters — info
// ---------------------------------------------------------------------------

func writeInfoCSV(resp *infoResponse) {
	w := csv.NewWriter(os.Stdout)
	_ = w.Write([]string{"file", "section", "key", "value"})
	for _, fi := range resp.Files {
		h := fi.Header
		for _, row := range [][2]string{
			{"driver", h.Driver}, {"vehicle", h.Vehicle}, {"venue", h.Venue},
			{"datetime", h.DateTime}, {"event", h.Event}, {"session", h.Session},
			{"device_type", h.DeviceType}, {"num_channels", fmt.Sprintf("%d", h.NumChannels)},
		} {
			_ = w.Write([]string{fi.Path, "header", row[0], row[1]})
		}
		for _, l := range fi.Laps {
			_ = w.Write([]string{fi.Path, "lap", fmt.Sprintf("%d", l.Number), fmt.Sprintf("%.3f", l.LapTime)})
		}
		for _, ch := range fi.Channels {
			_ = w.Write([]string{fi.Path, "channel", ch.Name, fmt.Sprintf("%s %dHz %d samples", ch.Unit, ch.Freq, ch.Samples)})
		}
	}
	w.Flush()
}

func writeInfoText(resp *infoResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, fi := range resp.Files {
		h := fi.Header
		fmt.Fprintf(tw, "File:\t%s\n", fi.Path)
		if h.Driver != "" {
			fmt.Fprintf(tw, "Driver:\t%s\n", h.Driver)
		}
		fmt.Fprintf(tw, "Venue:\t%s\n", h.Venue)
		if h.Event != "" {
			fmt.Fprintf(tw, "Event:\t%s  Session: %s\n", h.Event, h.Session)
		}
		fmt.Fprintf(tw, "DateTime:\t%s\n", h.DateTime)
		fmt.Fprintf(tw, "Channels:\t%d\n\n", h.NumChannels)
		fmt.Fprintln(tw, "LAP\tTIME\tSTART\tEND")
		for _, l := range fi.Laps {
			fmt.Fprintf(tw, "%d\t%s\t%.3f\t%.3f\n", l.Number, fmtLapTime(l.LapTime), l.StartTime, l.EndTime)
		}
		fmt.Fprintln(tw)
		fmt.Fprintln(tw, "CHANNEL\tUNIT\tFREQ\tSAMPLES")
		for _, ch := range fi.Channels {
			fmt.Fprintf(tw, "%s\t%s\t%dHz\t%d\n", ch.Name, ch.Unit, ch.Freq, ch.Samples)
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()
}

func writeInfoLaps(resp *infoResponse) {
	sep := strings.Repeat("─", 52)
	for fi, f := range resp.Files {
		if fi > 0 {
			fmt.Println()
		}
		h := f.Header
		fmt.Println(sep)
		fmt.Printf("  %-12s %s\n", "File:", f.Path)
		fmt.Println(sep)
		if h.Driver != "" {
			fmt.Printf("  %-12s %s\n", "Driver:", h.Driver)
		}
		if h.Vehicle != "" {
			fmt.Printf("  %-12s %s\n", "Vehicle:", h.Vehicle)
		}
		fmt.Printf("  %-12s %s\n", "Venue:", h.Venue)
		if h.Event != "" {
			fmt.Printf("  %-12s %s\n", "Event:", h.Event)
		}
		if h.Session != "" {
			fmt.Printf("  %-12s %s\n", "Session:", h.Session)
		}
		fmt.Printf("  %-12s %s\n", "Date/Time:", h.DateTime)
		if h.DeviceType != "" {
			serial := ""
			if h.DeviceSerial != 0 {
				serial = fmt.Sprintf("  (serial %d)", h.DeviceSerial)
			}
			fmt.Printf("  %-12s %s%s\n", "Logger:", h.DeviceType, serial)
		}
		fmt.Printf("  %-12s %d\n", "Channels:", h.NumChannels)

		if len(f.Laps) == 0 {
			fmt.Println("\n  (no laps detected)")
			continue
		}

		// Best lap = fastest timed lap with duration > 30s (exclude partial/outlap)
		bestTime := -1.0
		for _, l := range f.Laps {
			if l.Number > 0 && l.LapTime > 30 && (bestTime < 0 || l.LapTime < bestTime) {
				bestTime = l.LapTime
			}
		}

		fmt.Println()
		fmt.Printf("  %-4s  %-11s  %-9s  %-8s  %s\n", "LAP", "TIME", "GAP", "START", "END")
		fmt.Println("  " + strings.Repeat("─", 48))
		for _, l := range f.Laps {
			marker := "  "
			if bestTime > 0 && l.LapTime == bestTime {
				marker = "▶ "
			}
			lapLabel := fmt.Sprintf("%d", l.Number)
			if l.Number == 0 {
				lapLabel = "out"
			}
			timeStr := fmtLapTime(l.LapTime)
			gapStr := ""
			if bestTime > 0 && l.LapTime > 30 && l.Number > 0 {
				gap := l.LapTime - bestTime
				if gap == 0 {
					gapStr = "──────"
				} else {
					gapStr = fmt.Sprintf("+%.3f", gap)
				}
			}
			fmt.Printf("%s%-4s  %-11s  %-9s  %-8.3f  %.3f\n",
				marker, lapLabel, timeStr, gapStr, l.StartTime, l.EndTime)
		}
		fmt.Println()
		fmt.Printf("  %d lap(s)", len(f.Laps))
		if bestTime > 0 {
			fmt.Printf("  │  best: %s", fmtLapTime(bestTime))
		}
		fmt.Println()
		fmt.Println(sep)
	}
}

// ---------------------------------------------------------------------------
// Output formatters — summarize
// ---------------------------------------------------------------------------

func writeSumCSV(resp *summaryResponse) {
	w := csv.NewWriter(os.Stdout)
	_ = w.Write([]string{"file", "lap", "channel", "unit", "n", "min", "max", "mean", "std", "p5", "p50", "p95"})
	for _, fd := range resp.Files {
		for _, ld := range fd.Laps {
			ls := lapStr(ld.Number)
			for _, cs := range ld.Channels {
				_ = w.Write([]string{fd.Path, ls, cs.Name, cs.Unit,
					fmt.Sprintf("%d", cs.N),
					fmt.Sprintf("%.3f", cs.Min), fmt.Sprintf("%.3f", cs.Max),
					fmt.Sprintf("%.3f", cs.Mean), fmt.Sprintf("%.3f", cs.Std),
					fmt.Sprintf("%.3f", cs.P5), fmt.Sprintf("%.3f", cs.P50), fmt.Sprintf("%.3f", cs.P95),
				})
			}
		}
	}
	w.Flush()
}

func writeSumText(resp *summaryResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, fd := range resp.Files {
		fmt.Fprintf(tw, "=== %s ===\n", fd.Path)
		for _, ld := range fd.Laps {
			if ld.Number != nil {
				fmt.Fprintf(tw, "Lap %d  (%.3f–%.3f s  %s)\n", *ld.Number, ld.StartTime, ld.EndTime, fmtLapTime(ld.LapTime))
			} else {
				fmt.Fprintf(tw, "Full session  (%.3f–%.3f s)\n", ld.StartTime, ld.EndTime)
			}
			fmt.Fprintln(tw, "CHANNEL\tUNIT\tN\tMIN\tMAX\tMEAN\tSTD\tP50")
			for _, cs := range ld.Channels {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\n",
					cs.Name, cs.Unit, cs.N, cs.Min, cs.Max, cs.Mean, cs.Std, cs.P50)
				for _, sec := range cs.Sectors {
					fmt.Fprintf(tw, "  S%d [%.1f–%.1f]\t\t%d\t%.3f\t%.3f\t%.3f\t\t\n",
						sec.Sector, sec.TRange[0], sec.TRange[1], sec.N, sec.Min, sec.Max, sec.Mean)
				}
			}
			fmt.Fprintln(tw)
		}
		if len(fd.Trends) > 0 {
			fmt.Fprintln(tw, "TRENDS")
			fmt.Fprintln(tw, "CHANNEL\tUNIT\tDIRECTION\tSLOPE/LAP\tNOTE")
			for _, t := range fd.Trends {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%.3f\t%s\n", t.Name, t.Unit, t.Direction, t.SlopePerLap, t.Note)
			}
			fmt.Fprintln(tw)
		}
	}
	tw.Flush()
}

// ---------------------------------------------------------------------------
// Output formatters — data
// ---------------------------------------------------------------------------

func chanSamples(cd chanData) (ts, vs []float64) {
	if cd.T != nil {
		return cd.T, cd.V
	}
	ts = make([]float64, len(cd.V))
	for i := range cd.V {
		ts[i] = round3(*cd.TStart + float64(i)**cd.TStep)
	}
	return ts, cd.V
}

func writeDataCSV(resp *dataResponse) {
	w := csv.NewWriter(os.Stdout)
	_ = w.Write([]string{"file", "lap", "channel", "unit", "t", "v"})
	for _, fd := range resp.Files {
		for _, ld := range fd.Laps {
			ls := lapStr(ld.Number)
			for _, cd := range ld.Channels {
				ts, vs := chanSamples(cd)
				for i, t := range ts {
					_ = w.Write([]string{fd.Path, ls, cd.Name, cd.Unit, fmt.Sprintf("%.3f", t), fmt.Sprintf("%.3f", vs[i])})
				}
			}
		}
	}
	w.Flush()
}

func writeDataText(resp *dataResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, fd := range resp.Files {
		fmt.Fprintf(tw, "=== %s ===\n", fd.Path)
		for _, ld := range fd.Laps {
			if ld.Number != nil {
				fmt.Fprintf(tw, "Lap %d  (%.3f–%.3f s  %s)\n", *ld.Number, ld.StartTime, ld.EndTime, fmtLapTime(ld.LapTime))
			} else {
				fmt.Fprintf(tw, "Full session  (%.3f–%.3f s)\n", ld.StartTime, ld.EndTime)
			}
			for _, cd := range ld.Channels {
				fmt.Fprintf(tw, "  %s [%s]  %d pts\n", cd.Name, cd.Unit, cd.N)
				fmt.Fprintln(tw, "  T\tV")
				ts, vs := chanSamples(cd)
				for i, t := range ts {
					fmt.Fprintf(tw, "  %.3f\t%.3f\n", t, vs[i])
				}
			}
		}
	}
	tw.Flush()
}

func writeDataNDJSON(resp *dataResponse) {
	enc := json.NewEncoder(os.Stdout)
	for _, fd := range resp.Files {
		for _, ld := range fd.Laps {
			ls := lapStr(ld.Number)
			for _, cd := range ld.Channels {
				ts, vs := chanSamples(cd)
				for i, t := range ts {
					_ = enc.Encode(map[string]any{
						"f": fd.Path, "lap": ls,
						"ch": cd.Name, "unit": cd.Unit,
						"t": t, "v": vs[i],
					})
				}
			}
		}
	}
}
