package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	ldparser "github.com/mail/go-ldparser"
)

// annotation is a user-supplied time marker with a label placed in the report.
type annotation struct {
	T     float64 // time in seconds
	Label string
	Color string // optional hex color, default "#ffdd00"
}

// parseAnnotation parses "seconds:label" or "seconds:label:color".
func parseAnnotation(s string) (annotation, bool) {
	idx := strings.IndexByte(s, ':')
	if idx <= 0 {
		return annotation{}, false
	}
	t, err := strconv.ParseFloat(strings.TrimSpace(s[:idx]), 64)
	if err != nil {
		return annotation{}, false
	}
	rest := s[idx+1:]
	color := "#ffdd00"
	if li := strings.LastIndexByte(rest, ':'); li > 0 {
		candidate := strings.TrimSpace(rest[li+1:])
		if len(candidate) > 0 && candidate[0] == '#' {
			color = candidate
			rest = rest[:li]
		}
	}
	return annotation{T: t, Label: strings.TrimSpace(rest), Color: color}, true
}

// ---------------------------------------------------------------------------
// report command
// ---------------------------------------------------------------------------

// SVG layout constants
const (
	rSvgW  = 900
	rPadL  = 60
	rPadR  = 20
	rPadT  = 44
	rPadB  = 35
	rPlotW = 820
	rPlotH = 110
)

// channel colour palette
var channelColors = map[string]string{
	"speed":    "#00d4ff",
	"throttle": "#00ff88",
	"brake":    "#ff4444",
	"gear":     "#ffaa00",
}

var paletteExtras = []string{
	"#cc88ff", "#ff88cc", "#88ffcc", "#ffcc88", "#88ccff", "#ff8888",
}

func channelColor(name string, idx int) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "speed"):
		return channelColors["speed"]
	case strings.Contains(n, "throttle"):
		return channelColors["throttle"]
	case strings.Contains(n, "brake"):
		return channelColors["brake"]
	case strings.Contains(n, "gear"):
		return channelColors["gear"]
	}
	return paletteExtras[idx%len(paletteExtras)]
}

func runReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	lapFlag := fs.String("lap", "all", "lap number or 'all'")
	outFlag := fs.String("out", "", "output filename (default: report.html or stdout for ascii)")
	format := fs.String("format", "html", "html or ascii")
	var chFlag multiFlag
	fs.Var(&chFlag, "ch", "channel name (repeatable)")
	var annotateFlag multiFlag
	fs.Var(&annotateFlag, "annotate", `time marker in "seconds:label" or "seconds:label:#color" format (repeatable)`)
	refLapFlag := fs.Int("ref", -1, "reference lap number to overlay as dashed traces")

	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)

	if len(filePaths) == 0 {
		fatalJSON("report: no files specified")
	}

	var annotations []annotation
	for _, raw := range annotateFlag {
		if a, ok := parseAnnotation(raw); ok {
			annotations = append(annotations, a)
		}
	}

	files := loadFiles(filePaths)

	switch *format {
	case "ascii":
		runReportASCII(files, *lapFlag, chFlag, annotations)
	default:
		outFile := *outFlag
		if outFile == "" {
			outFile = "report.html"
		}
		runReportHTML(files, *lapFlag, chFlag, outFile, annotations, *refLapFlag)
	}
}

// ---------------------------------------------------------------------------
// Per-lap channel data bundle
// ---------------------------------------------------------------------------

type reportTrace struct {
	name  string
	unit  string
	data  []float64
	tFrom float64
	tTo   float64
	color string
}

type reportLap struct {
	win       lapWindow
	traces    []reportTrace
	refTraces []reportTrace // optional reference lap traces for overlay
	events    []event
}

type reportFile struct {
	fr   fileResult
	laps []reportLap
}

func buildReportData(files []fileResult, lapFlag string, chNames []string, refLap int) []reportFile {
	out := make([]reportFile, 0, len(files))

	for _, fr := range files {
		windows, errMsg := buildWindows(lapFlag, fr.Laps, sessionEnd(fr))
		if errMsg != "" {
			continue
		}

		// Find reference lap window if requested
		var refWin *lapWindow
		if refLap >= 0 {
			for _, l := range fr.Laps {
				if l.Number == refLap {
					n := l.Number
					w := lapWindow{num: &n, from: l.StartTime, to: l.EndTime, lapTime: l.LapTime}
					refWin = &w
					break
				}
			}
		}

		// Resolve channel list
		speedCh := findChannelFuzzy(fr.File, "Ground Speed", "Speed over Ground", "GPS Speed", "Vehicle Speed", "Speed")
		throttleCh := findChannelFuzzy(fr.File, "Throttle Pos", "Throttle Position", "Throttle")
		brakeCh := findChannelFuzzy(fr.File, "Brake Pres Front", "Brake Pressure Front", "Brake Pres", "Brake Pressure")
		gearCh := findChannelFuzzy(fr.File, "Gear", "Gear Position", "Current Gear")
		latGCh := findChannelFuzzy(fr.File, "G Force Lat", "G Lat", "Lateral G", "AccelLat", "Lat Accel")
		wsFLCh := findChannelFuzzy(fr.File, "Wheel Speed FL", "WheelSpeedFL", "Wheel Spd FL")
		wsFRCh := findChannelFuzzy(fr.File, "Wheel Speed FR", "WheelSpeedFR", "Wheel Spd FR")
		wsRLCh := findChannelFuzzy(fr.File, "Wheel Speed RL", "WheelSpeedRL", "Wheel Spd RL")
		wsRRCh := findChannelFuzzy(fr.File, "Wheel Speed RR", "WheelSpeedRR", "Wheel Spd RR")

		var channels []*ldparser.Channel
		if len(chNames) > 0 {
			for _, name := range chNames {
				if ch := findChannelFuzzy(fr.File, name); ch != nil {
					channels = append(channels, ch)
				}
			}
		}
		if len(channels) == 0 {
			// auto-detect defaults
			for _, ch := range []*ldparser.Channel{speedCh, throttleCh, brakeCh, gearCh} {
				if ch != nil {
					channels = append(channels, ch)
				}
			}
		}

		rf := reportFile{fr: fr}
		for _, win := range windows {
			events := detectAllEvents(fr.File, win, speedCh, brakeCh, throttleCh, gearCh, latGCh,
				wsFLCh, wsFRCh, wsRLCh, wsRRCh)
			sort.Slice(events, func(i, j int) bool { return events[i].T < events[j].T })

			var traces []reportTrace
			for idx, ch := range channels {
				data, tFrom := sliceChannel(ch, win.from, win.to)
				if len(data) == 0 {
					continue
				}
				tTo := tFrom + float64(len(data))/float64(ch.Freq)
				traces = append(traces, reportTrace{
					name:  ch.Name,
					unit:  ch.Unit,
					data:  data,
					tFrom: tFrom,
					tTo:   tTo,
					color: channelColor(ch.Name, idx),
				})
			}

			// Build reference traces if a reference lap was found and is different from current lap
			var refTraces []reportTrace
			if refWin != nil && (win.num == nil || *win.num != *refWin.num) {
				for idx, ch := range channels {
					data, tFrom := sliceChannel(ch, refWin.from, refWin.to)
					if len(data) == 0 {
						continue
					}
					tTo := tFrom + float64(len(data))/float64(ch.Freq)
					refTraces = append(refTraces, reportTrace{
						name:  ch.Name,
						unit:  ch.Unit,
						data:  data,
						tFrom: tFrom,
						tTo:   tTo,
						color: channelColor(ch.Name, idx),
					})
				}
			}

			rf.laps = append(rf.laps, reportLap{win: win, traces: traces, refTraces: refTraces, events: events})
		}
		out = append(out, rf)
	}
	return out
}

// ---------------------------------------------------------------------------
// HTML report
// ---------------------------------------------------------------------------

func runReportHTML(files []fileResult, lapFlag string, chNames []string, outFile string, annotations []annotation, refLap int) {
	data := buildReportData(files, lapFlag, chNames, refLap)

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Telemetry Report</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{background:#1a1a2e;color:#e0e0e0;font-family:monospace;font-size:13px;padding:20px}
h1{color:#00d4ff;margin-bottom:16px;font-size:18px}
h2{color:#aaa;margin:24px 0 8px;font-size:15px;border-bottom:1px solid #333;padding-bottom:4px}
h3{color:#888;margin:16px 0 4px;font-size:13px}
.file-header{background:#16213e;border:1px solid #0f3460;border-radius:6px;padding:12px 16px;margin-bottom:16px}
.file-header p{margin:2px 0;color:#ccc}
.file-header strong{color:#00d4ff}
.lap-section{margin-bottom:32px}
.svg-wrap{overflow-x:auto;margin-bottom:8px}
svg{display:block}
.legend{display:flex;gap:16px;flex-wrap:wrap;margin-bottom:8px}
.legend-item{display:flex;align-items:center;gap:6px;font-size:12px}
.legend-dot{width:12px;height:3px;display:inline-block;border-radius:2px}
</style>
</head>
<body>
<h1>Telemetry Report</h1>
`)

	for _, rf := range data {
		h := rf.fr.File.Header
		driver := h.Driver
		venue := h.Venue
		if venue == "" && h.Event != nil && h.Event.Venue != nil {
			venue = h.Event.Venue.Name
		}
		sb.WriteString(`<div class="file-header">`)
		fmt.Fprintf(&sb, "<p><strong>File:</strong> %s</p>\n", rf.fr.Path)
		fmt.Fprintf(&sb, "<p><strong>Driver:</strong> %s &nbsp; <strong>Vehicle:</strong> %s &nbsp; <strong>Venue:</strong> %s</p>\n",
			driver, h.VehicleID, venue)
		fmt.Fprintf(&sb, "<p><strong>Laps detected:</strong> %d &nbsp; <strong>Channels:</strong> %d</p>\n",
			len(rf.fr.Laps), len(rf.fr.File.Channels))
		sb.WriteString("</div>\n")

		for _, lap := range rf.laps {
			sb.WriteString(`<div class="lap-section">`)
			lapLabel := "Full session"
			if lap.win.num != nil {
				lapLabel = fmt.Sprintf("Lap %d", *lap.win.num)
				if lap.win.lapTime > 0 {
					lapLabel += fmt.Sprintf(" — %s", fmtLapTime(lap.win.lapTime))
				}
			}
			fmt.Fprintf(&sb, "<h2>%s</h2>\n", lapLabel)

			// legend
			if len(lap.traces) > 0 {
				sb.WriteString(`<div class="legend">`)
				for _, tr := range lap.traces {
					lbl := tr.name
					if tr.unit != "" {
						lbl += " (" + tr.unit + ")"
					}
					fmt.Fprintf(&sb, `<div class="legend-item"><span class="legend-dot" style="background:%s"></span>%s</div>`,
						tr.color, lbl)
				}
				if len(lap.refTraces) > 0 {
					refNum := "?"
					if lap.refTraces[0].name != "" {
						refNum = "ref"
					}
					fmt.Fprintf(&sb, `<div class="legend-item"><span class="legend-dot" style="background:#555;border-top:2px dashed #888"></span>%s (dashed)</div>`,
						refNum)
				}
				sb.WriteString("</div>\n")
			}

			// one SVG per trace channel
			for i, tr := range lap.traces {
				var refTr *reportTrace
				if i < len(lap.refTraces) {
					refTr = &lap.refTraces[i]
				}
				sb.WriteString(`<div class="svg-wrap">`)
				sb.WriteString(buildTraceSVG(tr, refTr, lap.events, annotations, lap.win.from, lap.win.to))
				sb.WriteString("</div>\n")
			}

			sb.WriteString("</div>\n")
		}
	}

	sb.WriteString("</body>\n</html>\n")

	if err := os.WriteFile(outFile, []byte(sb.String()), 0644); err != nil {
		fatalJSON(fmt.Sprintf("report: cannot write %q: %v", outFile, err))
	}
	fmt.Fprintf(os.Stderr, "report written to %s\n", outFile)
}

// buildTraceSVG renders a single channel trace as an inline SVG with event overlays.
// If refTr is non-nil, it is rendered as a dashed reference trace (time-fraction aligned).
func buildTraceSVG(tr reportTrace, refTr *reportTrace, events []event, annotations []annotation, winFrom, winTo float64) string {
	svgH := rPadT + rPlotH + rPadB

	data := decimateSlice(tr.data, 500)
	freq := float64(len(tr.data)) / (tr.tTo - tr.tFrom)
	if math.IsInf(freq, 0) || math.IsNaN(freq) || freq <= 0 {
		freq = 1
	}
	step := 1
	if len(tr.data) > 500 {
		step = len(tr.data) / 500
	}

	// Compute value range
	minV, maxV := math.MaxFloat64, -math.MaxFloat64
	for _, v := range data {
		sv := sanitize(v)
		if sv < minV {
			minV = sv
		}
		if sv > maxV {
			maxV = sv
		}
	}
	if minV == maxV {
		minV -= 1
		maxV += 1
	}
	// 5% padding
	pad := (maxV - minV) * 0.05
	if pad == 0 {
		pad = 1
	}
	yMin := minV - pad
	yMax := maxV + pad

	tDur := winTo - winFrom
	if tDur <= 0 {
		tDur = 1
	}

	xOf := func(t float64) float64 {
		return float64(rPadL) + (t-winFrom)/tDur*float64(rPlotW)
	}
	yOf := func(v float64) float64 {
		norm := (sanitize(v) - yMin) / (yMax - yMin)
		return float64(rPadT+rPlotH) - norm*float64(rPlotH)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" style="background:#0d0d1a">`,
		rSvgW, svgH)

	// Grid lines + Y labels (5 lines)
	gridColor := "#2a2a4a"
	labelColor := "#666"
	for i := 0; i <= 4; i++ {
		frac := float64(i) / 4.0
		v := yMin + frac*(yMax-yMin)
		y := yOf(v)
		fmt.Fprintf(&sb, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1"/>`,
			rPadL, y, rPadL+rPlotW, y, gridColor)
		fmt.Fprintf(&sb, `<text x="%d" y="%.1f" fill="%s" font-size="10" text-anchor="end" dominant-baseline="middle">%s</text>`,
			rPadL-4, y, labelColor, formatAxisVal(v))
	}

	// X-axis time labels
	xTicks := 6
	for i := 0; i <= xTicks; i++ {
		frac := float64(i) / float64(xTicks)
		t := winFrom + frac*tDur
		x := xOf(t)
		fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="1"/>`,
			x, rPadT, x, rPadT+rPlotH, gridColor)
		fmt.Fprintf(&sb, `<text x="%.1f" y="%d" fill="%s" font-size="10" text-anchor="middle">%.1fs</text>`,
			x, rPadT+rPlotH+14, labelColor, t)
	}

	// Channel name label
	lbl := tr.name
	if tr.unit != "" {
		lbl += " (" + tr.unit + ")"
	}
	fmt.Fprintf(&sb, `<text x="%d" y="%d" fill="%s" font-size="11" font-weight="bold">%s</text>`,
		rPadL, rPadT-8, tr.color, lbl)

	// Event overlays
	for _, ev := range events {
		if ev.T > winTo || (ev.TEnd > 0 && ev.TEnd < winFrom) {
			continue
		}
		x1 := xOf(ev.T)
		switch ev.Type {
		case "braking_zone":
			x2 := xOf(ev.TEnd)
			if x2 < x1 {
				x2 = x1 + 1
			}
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="#ff3333" opacity="0.18"/>`,
				x1, rPadT, x2-x1, rPlotH)
		case "full_throttle_zone":
			x2 := xOf(ev.TEnd)
			if x2 < x1 {
				x2 = x1 + 1
			}
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="#00ff88" opacity="0.12"/>`,
				x1, rPadT, x2-x1, rPlotH)
		case "gear_shift":
			fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="#888" stroke-width="1" stroke-dasharray="3,2"/>`,
				x1, rPadT, x1, rPadT+rPlotH)
			fmt.Fprintf(&sb, `<text x="%.1f" y="%d" fill="#aaa" font-size="9" text-anchor="middle">%d</text>`,
				x1, rPadT+10, ev.GearTo)
		case "corner_apex":
			yc := float64(rPadT + rPlotH/2)
			fmt.Fprintf(&sb, `<circle cx="%.1f" cy="%.1f" r="4" fill="none" stroke="#00cc44" stroke-width="1.5"/>`,
				x1, yc)
		case "lockup":
			fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="#ff8800" stroke-width="1.5"/>`,
				x1, rPadT, x1, rPadT+rPlotH)
		}
	}

	// Annotation markers (user-supplied labeled pointers)
	for _, an := range annotations {
		if an.T < winFrom || an.T > winTo {
			continue
		}
		x := xOf(an.T)
		ac := an.Color
		// Vertical dashed line
		fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="1.5" stroke-dasharray="5,3" opacity="0.9"/>`,
			x, rPadT, x, rPadT+rPlotH, ac)
		// Downward-pointing triangle at top of line
		fmt.Fprintf(&sb, `<polygon points="%.1f,%d %.1f,%d %.1f,%d" fill="%s" opacity="0.9"/>`,
			x-5, rPadT, x+5, rPadT, x, rPadT+8, ac)
		// Label just above the triangle, rotated 45° for compact layout
		label := an.Label
		if len(label) > 20 {
			label = label[:20]
		}
		fmt.Fprintf(&sb, `<text x="%.1f" y="%d" fill="%s" font-size="9" font-weight="bold" transform="rotate(-45 %.1f %d)" text-anchor="start">%s</text>`,
			x+3, rPadT-2, ac, x+3, rPadT-2, label)
	}

	// Reference trace (dashed, dimmer, time-fraction aligned to primary lap)
	if refTr != nil && len(refTr.data) > 0 {
		refData := decimateSlice(refTr.data, 500)
		refStep := 1
		if len(refTr.data) > 500 {
			refStep = len(refTr.data) / 500
		}
		// Extend Y range to include reference data
		for _, v := range refData {
			sv := sanitize(v)
			if sv < yMin+pad {
				yMin = sv - pad
			}
			if sv > yMax-pad {
				yMax = sv + pad
			}
		}
		var refPts strings.Builder
		for i, v := range refData {
			// Map reference trace to primary lap time range via fraction
			frac := float64(i*refStep) / float64(len(refTr.data))
			t := winFrom + frac*tDur
			x := xOf(t)
			y := yOf(v)
			if i == 0 {
				fmt.Fprintf(&refPts, "%.1f,%.1f", x, y)
			} else {
				fmt.Fprintf(&refPts, " %.1f,%.1f", x, y)
			}
		}
		refColor := refTr.color
		fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1" stroke-dasharray="5,3" opacity="0.5" stroke-linejoin="round"/>`,
			refPts.String(), refColor)
	}

	// Trace polyline
	var pts strings.Builder
	for i, v := range data {
		t := tr.tFrom + float64(i*step)/float64(len(tr.data))*(tr.tTo-tr.tFrom)
		x := xOf(t)
		y := yOf(v)
		if i == 0 {
			fmt.Fprintf(&pts, "%.1f,%.1f", x, y)
		} else {
			fmt.Fprintf(&pts, " %.1f,%.1f", x, y)
		}
	}
	fmt.Fprintf(&sb, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.5" stroke-linejoin="round"/>`,
		pts.String(), tr.color)

	// Border
	fmt.Fprintf(&sb, `<rect x="%d" y="%d" width="%d" height="%d" fill="none" stroke="#333" stroke-width="1"/>`,
		rPadL, rPadT, rPlotW, rPlotH)

	sb.WriteString("</svg>")
	return sb.String()
}

func formatAxisVal(v float64) string {
	if math.Abs(v) >= 10000 {
		return fmt.Sprintf("%.0f", v)
	}
	if math.Abs(v) >= 100 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func decimateSlice(src []float64, maxPts int) []float64 {
	if len(src) <= maxPts {
		return src
	}
	step := len(src) / maxPts
	out := make([]float64, 0, maxPts)
	for i := 0; i < len(src); i += step {
		out = append(out, src[i])
	}
	return out
}

// ---------------------------------------------------------------------------
// ASCII report
// ---------------------------------------------------------------------------

const (
	asciiRows = 10
	asciiCols = 78
)

var unicodeBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

func runReportASCII(files []fileResult, lapFlag string, chNames []string, annotations []annotation) {
	data := buildReportData(files, lapFlag, chNames, -1)

	for _, rf := range data {
		h := rf.fr.File.Header
		fmt.Printf("=== %s  driver=%s  venue=%s  laps=%d ===\n",
			rf.fr.Path, h.Driver, h.Venue, len(rf.fr.Laps))

		for _, lap := range rf.laps {
			lapLabel := "Full session"
			if lap.win.num != nil {
				lapLabel = fmt.Sprintf("Lap %d", *lap.win.num)
				if lap.win.lapTime > 0 {
					lapLabel += "  " + fmtLapTime(lap.win.lapTime)
				}
			}
			fmt.Printf("\n--- %s ---\n", lapLabel)

			for _, tr := range lap.traces {
				printASCIIChart(tr, lap.events, annotations, lap.win.from, lap.win.to)
			}
		}
	}
}

func printASCIIChart(tr reportTrace, events []event, annotations []annotation, winFrom, winTo float64) {
	plotCols := asciiCols - 10 // 10 chars for Y label on left

	// Compute value range
	minV, maxV := math.MaxFloat64, -math.MaxFloat64
	for _, v := range tr.data {
		sv := sanitize(v)
		if sv < minV {
			minV = sv
		}
		if sv > maxV {
			maxV = sv
		}
	}
	if minV == maxV {
		minV -= 1
		maxV += 1
	}

	// Decimate to plot width
	stepped := decimateSlice(tr.data, plotCols)
	tDur := winTo - winFrom
	if tDur <= 0 {
		tDur = 1
	}

	// Build event marker row (same width as plot)
	eventRow := make([]rune, plotCols)
	for i := range eventRow {
		eventRow[i] = ' '
	}
	for _, ev := range events {
		if ev.T < winFrom || ev.T > winTo {
			continue
		}
		col := int((ev.T - winFrom) / tDur * float64(plotCols))
		if col < 0 {
			col = 0
		}
		if col >= plotCols {
			col = plotCols - 1
		}
		var marker rune
		switch ev.Type {
		case "gear_shift":
			marker = 'G'
		case "braking_zone":
			marker = 'B'
		case "corner_apex":
			marker = 'C'
		case "full_throttle_zone":
			marker = 'T'
		case "lockup":
			marker = 'L'
		default:
			marker = '|'
		}
		eventRow[col] = marker
	}
	for _, an := range annotations {
		if an.T < winFrom || an.T > winTo {
			continue
		}
		col := int((an.T - winFrom) / tDur * float64(plotCols))
		if col < 0 {
			col = 0
		}
		if col >= plotCols {
			col = plotCols - 1
		}
		eventRow[col] = '^'
	}

	// Build grid: rows x cols of rune
	grid := make([][]rune, asciiRows)
	for r := range grid {
		grid[r] = make([]rune, plotCols)
		for c := range grid[r] {
			grid[r][c] = ' '
		}
	}

	for colIdx, v := range stepped {
		if colIdx >= plotCols {
			break
		}
		sv := sanitize(v)
		norm := (sv - minV) / (maxV - minV)
		if norm < 0 {
			norm = 0
		}
		if norm > 1 {
			norm = 1
		}
		// Map norm [0,1] to block char — bottom row = row asciiRows-1
		blockIdx := int(norm * float64(len(unicodeBlocks)-1))
		if blockIdx < 0 {
			blockIdx = 0
		}
		if blockIdx >= len(unicodeBlocks) {
			blockIdx = len(unicodeBlocks) - 1
		}
		// Row index: norm=1 → row 0 (top), norm=0 → row asciiRows-1 (bottom)
		rowFull := int(norm * float64(asciiRows))
		if rowFull >= asciiRows {
			rowFull = asciiRows - 1
		}
		// Fill from bottom up to rowFull
		for r := asciiRows - 1; r >= asciiRows-1-rowFull; r-- {
			if r < 0 {
				break
			}
			grid[r][colIdx] = '█'
		}
		// Partial block at the top filled row
		topRow := asciiRows - 1 - rowFull
		if topRow >= 0 && topRow < asciiRows {
			grid[topRow][colIdx] = unicodeBlocks[blockIdx]
		}
	}

	// Print title
	unit := tr.unit
	if unit != "" {
		unit = " (" + unit + ")"
	}
	fmt.Printf("┌%s┐\n", strings.Repeat("─", asciiCols-2))
	fmt.Printf("│ %-*s│\n", asciiCols-3, tr.name+unit)
	fmt.Printf("├%s%s┤\n", strings.Repeat("─", 9), strings.Repeat("─", plotCols))

	// Print rows with Y labels
	for rowIdx := 0; rowIdx < asciiRows; rowIdx++ {
		// Y label at 4 positions: top (0), 25% (2), 50% (5), 75% (7), bottom (9)
		frac := 1.0 - float64(rowIdx)/float64(asciiRows-1)
		yVal := minV + frac*(maxV-minV)
		yLbl := fmt.Sprintf("%-8s", formatAxisVal(yVal))
		if len(yLbl) > 8 {
			yLbl = yLbl[:8]
		}
		fmt.Printf("│%s│%s│\n", yLbl, string(grid[rowIdx]))
	}

	// Event row
	fmt.Printf("├%s%s┤\n", strings.Repeat("─", 9), strings.Repeat("─", plotCols))
	fmt.Printf("│%-9s│%s│\n", "events", string(eventRow))

	// X-axis time ticks
	fmt.Printf("├%s%s┤\n", strings.Repeat("─", 9), strings.Repeat("─", plotCols))
	tickRow := make([]rune, plotCols)
	for i := range tickRow {
		tickRow[i] = ' '
	}
	numTicks := 5
	for i := 0; i <= numTicks; i++ {
		frac := float64(i) / float64(numTicks)
		t := winFrom + frac*tDur
		col := int(frac * float64(plotCols-1))
		label := fmt.Sprintf("%.0fs", t)
		for j, ch := range []rune(label) {
			if col+j < plotCols {
				tickRow[col+j] = ch
			}
		}
	}
	fmt.Printf("│%-9s│%s│\n", "time", string(tickRow))
	fmt.Printf("└%s%s┘\n", strings.Repeat("─", 9), strings.Repeat("─", plotCols))

	// Annotation legend: list markers visible in this window
	for _, an := range annotations {
		if an.T < winFrom || an.T > winTo {
			continue
		}
		fmt.Printf("  ^ %.1fs  %s\n", an.T, an.Label)
	}
	fmt.Println()
}
