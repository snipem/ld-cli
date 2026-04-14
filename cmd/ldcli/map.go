package main

// ---------------------------------------------------------------------------
// Track map — SVG rendering from GPS or world-coordinate channels
// ---------------------------------------------------------------------------
//
// Position source priority:
//   1. Car Coord X / Car Coord Y  (world metres, sim-exported — no projection)
//   2. GPS Latitude / GPS Longitude (degrees — equirectangular projection)
//
// The path is colored by speed (blue=slow → red=fast) using short line
// segments so each segment can carry its own color.
//
// Annotations let an LLM (or user) mark corners, braking zones, incidents
// with a label, arrow direction, and color. Format on the CLI:
//
//   --mark "0.23:T1:#e74c3c"        distance-fraction : label : color
//   --mark "0.23:T1"                color defaults to yellow
//
// The LLM should:
//   1. Run `ldcli map file.ld` without marks to see the raw map.
//   2. Identify corners by examining the speed trace or the track shape.
//   3. Re-run with --mark flags to annotate.

import (
	"flag"
	"fmt"
	"math"
	"strconv"
	"strings"

	ldparser "github.com/mail/go-ldparser"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type trackPoint struct {
	x, y  float64 // projected metres or world coords
	speed float64 // kph at this point
	dist  float64 // cumulative distance from lap start (m)
}

type mapMark struct {
	DistFrac float64 // 0..1 along the lap
	Label    string
	Color    string // hex, e.g. "#e74c3c"
}

// ---------------------------------------------------------------------------
// Command entry
// ---------------------------------------------------------------------------

func runMap(args []string) {
	fs := flag.NewFlagSet("map", flag.ExitOnError)
	lapFlag := fs.String("lap", "best", "lap number, 'best', or 'all' (overlays all valid laps)")
	widthFlag := fs.Int("width", 800, "SVG canvas width in pixels")
	heightFlag := fs.Int("height", 600, "SVG canvas height in pixels")
	paddingFlag := fs.Int("padding", 40, "padding around map in pixels")
	var markFlags multiFlag
	fs.Var(&markFlags, "mark", "annotation: 'dist_frac:label' or 'dist_frac:label:color' (repeatable)")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)

	if len(filePaths) == 0 {
		fatalJSON("map: no file paths provided")
	}

	files := loadFiles(filePaths)

	type fileOut struct {
		Path         string   `json:"path"`
		SVG          string   `json:"svg,omitempty"`
		PositionSrc  string   `json:"position_source,omitempty"`
		LapsUsed     []int    `json:"laps_used,omitempty"`
		TotalDistM   float64  `json:"total_distance_m,omitempty"`
		Marks        []mapMark `json:"marks,omitempty"`
		Error        string   `json:"error,omitempty"`
	}

	marks := parseMapMarks(markFlags)

	resp := struct {
		Files []fileOut `json:"files"`
		Usage string    `json:"usage"`
	}{
		Usage: strings.Join([]string{
			"svg contains a self-contained SVG track map colored by speed (blue=slow, red=fast).",
			"To annotate corners: re-run with --mark '0.23:T1:#e74c3c' (dist_frac = 0..1 along lap).",
			"dist_frac 0.0 = lap start/finish, 0.5 = halfway through the lap by distance.",
			"Find corner positions by examining where speed is lowest in `ldcli data --lap N --ch 'Ground Speed'`.",
			"Arrow direction follows the driving direction at that point.",
			"--lap all overlays all valid laps (useful to see line variation).",
		}, " "),
	}

	for _, fr := range files {
		// Select laps.
		laps, err := selectLapsForMap(fr, *lapFlag)
		if err != nil {
			resp.Files = append(resp.Files, fileOut{Path: fr.Path, Error: err.Error()})
			continue
		}

		// Find position channels.
		xCh, yCh, src := findPositionChannels(fr.File)
		if xCh == nil || yCh == nil {
			resp.Files = append(resp.Files, fileOut{
				Path:  fr.Path,
				Error: "no position channels found — need GPS Latitude+Longitude or Car Coord X+Y",
			})
			continue
		}

		speedCh := findChannelFuzzy(fr.File,
			"Ground Speed", "Speed", "Vehicle Speed", "GPS Speed",
			"GroundSpeed", "VehicleSpeed",
		)

		// Build point traces for each lap.
		var allTraces [][]trackPoint
		var lapNums []int
		for _, lap := range laps {
			pts := buildTrackPoints(xCh, yCh, speedCh, lap.StartTime, lap.EndTime, src == "gps")
			if len(pts) >= 10 {
				allTraces = append(allTraces, pts)
				lapNums = append(lapNums, lap.Number)
			}
		}
		if len(allTraces) == 0 {
			resp.Files = append(resp.Files, fileOut{Path: fr.Path, Error: "no usable position data in selected laps"})
			continue
		}

		// Use first trace for total distance (best or first lap).
		totalDist := allTraces[0][len(allTraces[0])-1].dist

		svg := renderMapSVG(allTraces, marks, *widthFlag, *heightFlag, *paddingFlag)

		resp.Files = append(resp.Files, fileOut{
			Path:        fr.Path,
			SVG:         svg,
			PositionSrc: src,
			LapsUsed:    lapNums,
			TotalDistM:  round3(totalDist),
			Marks:       marks,
		})
	}

	writeJSON(resp)
}

// ---------------------------------------------------------------------------
// Lap selection
// ---------------------------------------------------------------------------

func selectLapsForMap(fr fileResult, lapFlag string) ([]ldparser.Lap, error) {
	if len(fr.Laps) == 0 {
		return nil, fmt.Errorf("no laps detected")
	}
	switch lapFlag {
	case "all":
		var out []ldparser.Lap
		for _, l := range fr.Laps {
			if l.Number > 0 && l.LapTime >= 30 {
				out = append(out, l)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no complete laps found")
		}
		return out, nil
	case "best":
		best := fr.Laps[0]
		for _, l := range fr.Laps {
			if l.Number > 0 && l.LapTime >= 30 && (best.Number == 0 || l.LapTime < best.LapTime) {
				best = l
			}
		}
		if best.Number == 0 || best.LapTime < 30 {
			return nil, fmt.Errorf("no complete lap found")
		}
		return []ldparser.Lap{best}, nil
	default:
		n, err := strconv.Atoi(lapFlag)
		if err != nil {
			return nil, fmt.Errorf("invalid --lap value %q: use a number, 'best', or 'all'", lapFlag)
		}
		for _, l := range fr.Laps {
			if l.Number == n {
				return []ldparser.Lap{l}, nil
			}
		}
		return nil, fmt.Errorf("lap %d not found", n)
	}
}

// ---------------------------------------------------------------------------
// Position channel detection
// ---------------------------------------------------------------------------

func findPositionChannels(f *ldparser.File) (xCh, yCh *ldparser.Channel, src string) {
	// Prefer world-space coordinates (metres, no projection needed).
	x := findChannelFuzzy(f, "Car Coord X", "Pos X", "World Pos X", "X Position")
	y := findChannelFuzzy(f, "Car Coord Y", "Pos Y", "World Pos Y", "Y Position")
	if x != nil && y != nil && len(x.Data) > 0 && len(y.Data) > 0 {
		return x, y, "world_coords"
	}
	// Fall back to GPS.
	lat := findChannelFuzzy(f, "GPS Latitude", "Latitude", "GPS Lat")
	lon := findChannelFuzzy(f, "GPS Longitude", "Longitude", "GPS Lon", "GPS Long")
	if lat != nil && lon != nil && len(lat.Data) > 0 && len(lon.Data) > 0 {
		return lat, lon, "gps"
	}
	return nil, nil, ""
}

// ---------------------------------------------------------------------------
// Build track point trace
// ---------------------------------------------------------------------------

func buildTrackPoints(xCh, yCh, speedCh *ldparser.Channel, from, to float64, isGPS bool) []trackPoint {
	xs, _ := sliceChannel(xCh, from, to)
	ys, _ := sliceChannel(yCh, from, to)
	if len(xs) == 0 || len(ys) == 0 {
		return nil
	}

	// Downsample to at most 2000 points for SVG size.
	n := len(xs)
	if len(ys) < n {
		n = len(ys)
	}
	step := 1
	if n > 2000 {
		step = n / 2000
	}

	var speeds []float64
	if speedCh != nil {
		speeds, _ = sliceChannel(speedCh, from, to)
	}

	// For GPS: equirectangular projection centered on mean latitude.
	var latRef float64
	if isGPS && n > 0 {
		var sum float64
		for i := 0; i < n; i++ {
			sum += xs[i]
		}
		latRef = sum / float64(n)
	}

	pts := make([]trackPoint, 0, n/step+1)
	var cumDist float64
	var prevX, prevY float64
	first := true

	for i := 0; i < n; i += step {
		var px, py float64
		if isGPS {
			// Equirectangular: x = lon * cos(lat_ref) * R, y = lat * R
			const R = 6371000.0
			latRad := xs[i] * math.Pi / 180.0
			lonRad := ys[i] * math.Pi / 180.0
			latRefRad := latRef * math.Pi / 180.0
			px = lonRad * math.Cos(latRefRad) * R
			py = latRad * R
		} else {
			px = xs[i]
			py = ys[i]
		}

		var spd float64
		if len(speeds) > 0 {
			si := i * len(speeds) / n
			if si >= len(speeds) {
				si = len(speeds) - 1
			}
			spd = speeds[si]
		}

		if !first {
			dx := px - prevX
			dy := py - prevY
			cumDist += math.Sqrt(dx*dx + dy*dy)
		}
		pts = append(pts, trackPoint{x: px, y: py, speed: spd, dist: cumDist})
		prevX, prevY = px, py
		first = false
	}
	return pts
}

// ---------------------------------------------------------------------------
// SVG rendering
// ---------------------------------------------------------------------------

func renderMapSVG(traces [][]trackPoint, marks []mapMark, w, h, pad int) string {
	// Compute bounding box across all traces.
	minX, minY := math.MaxFloat64, math.MaxFloat64
	maxX, maxY := -math.MaxFloat64, -math.MaxFloat64
	var maxSpeed float64
	for _, pts := range traces {
		for _, p := range pts {
			if p.x < minX {
				minX = p.x
			}
			if p.x > maxX {
				maxX = p.x
			}
			if p.y < minY {
				minY = p.y
			}
			if p.y > maxY {
				maxY = p.y
			}
			if p.speed > maxSpeed {
				maxSpeed = p.speed
			}
		}
	}

	plotW := float64(w - 2*pad)
	plotH := float64(h - 2*pad)

	// Equal-axis scaling: preserve track shape.
	rangeX := maxX - minX
	rangeY := maxY - minY
	if rangeX == 0 {
		rangeX = 1
	}
	if rangeY == 0 {
		rangeY = 1
	}
	scale := plotW / rangeX
	if plotH/rangeY < scale {
		scale = plotH / rangeY
	}
	// Center within padded area.
	offX := float64(pad) + (plotW-rangeX*scale)/2
	offY := float64(pad) + (plotH-rangeY*scale)/2

	toSVG := func(x, y float64) (float64, float64) {
		sx := offX + (x-minX)*scale
		// Flip Y: SVG Y increases downward, world Y increases upward.
		sy := float64(h) - offY - (y-minY)*scale
		return sx, sy
	}

	if maxSpeed == 0 {
		maxSpeed = 1
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" style="background:#1a1a2e">`,
		w, h, w, h)
	sb.WriteString("\n")

	// Draw each lap trace as colored segments.
	opacity := "1.0"
	if len(traces) > 1 {
		opacity = "0.6"
	}
	for ti, pts := range traces {
		_ = ti
		if len(pts) < 2 {
			continue
		}
		for i := 1; i < len(pts); i++ {
			x1, y1 := toSVG(pts[i-1].x, pts[i-1].y)
			x2, y2 := toSVG(pts[i].x, pts[i].y)
			col := speedColor(pts[i].speed, maxSpeed)
			fmt.Fprintf(&sb, `  <line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="2.5" stroke-linecap="round" opacity="%s"/>`,
				x1, y1, x2, y2, col, opacity)
			sb.WriteString("\n")
		}
	}

	// Start/finish marker on first trace.
	if len(traces) > 0 && len(traces[0]) > 0 {
		sfx, sfy := toSVG(traces[0][0].x, traces[0][0].y)
		fmt.Fprintf(&sb,
			`  <circle cx="%.1f" cy="%.1f" r="6" fill="#ffffff" stroke="#000000" stroke-width="1.5"/>`,
			sfx, sfy)
		sb.WriteString("\n")
		fmt.Fprintf(&sb,
			`  <text x="%.1f" y="%.1f" font-family="monospace" font-size="11" fill="#ffffff" text-anchor="middle" dy="-10">S/F</text>`,
			sfx, sfy)
		sb.WriteString("\n")
	}

	// Annotation marks.
	for _, m := range marks {
		if len(traces) == 0 || len(traces[0]) == 0 {
			continue
		}
		pts := traces[0]
		totalDist := pts[len(pts)-1].dist
		targetDist := m.DistFrac * totalDist

		// Find nearest point by cumulative distance.
		idx := 0
		for i, p := range pts {
			if p.dist >= targetDist {
				idx = i
				break
			}
		}
		if idx >= len(pts) {
			idx = len(pts) - 1
		}

		mx, my := toSVG(pts[idx].x, pts[idx].y)
		col := m.Color
		if col == "" {
			col = "#f1c40f"
		}

		// Compute arrow direction from surrounding points.
		var dx, dy float64
		if idx+1 < len(pts) {
			ax, ay := toSVG(pts[idx+1].x, pts[idx+1].y)
			dx = ax - mx
			dy = ay - my
		} else if idx > 0 {
			bx, by := toSVG(pts[idx-1].x, pts[idx-1].y)
			dx = mx - bx
			dy = my - by
		}
		mag := math.Sqrt(dx*dx + dy*dy)
		if mag < 0.001 {
			mag = 1
		}
		dx, dy = dx/mag, dy/mag

		// Arrow: stem from a small offset, tip toward driving direction.
		stemLen := 22.0
		tipX := mx + dx*stemLen
		tipY := my + dy*stemLen
		// Arrowhead.
		perpX := -dy * 5
		perpY := dx * 5
		fmt.Fprintf(&sb,
			`  <line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="2"/>`,
			mx, my, tipX, tipY, col)
		sb.WriteString("\n")
		fmt.Fprintf(&sb,
			`  <polygon points="%.1f,%.1f %.1f,%.1f %.1f,%.1f" fill="%s"/>`,
			tipX, tipY,
			tipX-dx*8+perpX, tipY-dy*8+perpY,
			tipX-dx*8-perpX, tipY-dy*8-perpY,
			col)
		sb.WriteString("\n")
		// Label box.
		lx := tipX + dx*4
		ly := tipY + dy*4
		fmt.Fprintf(&sb,
			`  <rect x="%.1f" y="%.1f" width="%d" height="16" rx="3" fill="%s" opacity="0.85"/>`,
			lx-2, ly-12, len(m.Label)*8+6, col)
		sb.WriteString("\n")
		fmt.Fprintf(&sb,
			`  <text x="%.1f" y="%.1f" font-family="monospace" font-size="11" font-weight="bold" fill="#000000">%s</text>`,
			lx+1, ly, m.Label)
		sb.WriteString("\n")
	}

	// Speed legend (color bar).
	legendX := float64(w - pad - 80)
	legendY := float64(pad)
	legendH := 120.0
	legendW := 14.0
	steps := 20
	for i := 0; i < steps; i++ {
		frac := float64(i) / float64(steps-1)
		spd := frac * maxSpeed
		col := speedColor(spd, maxSpeed)
		y0 := legendY + (1.0-frac)*legendH
		segH := legendH / float64(steps)
		fmt.Fprintf(&sb,
			`  <rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`,
			legendX, y0, legendW, segH+1, col)
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb,
		`  <text x="%.1f" y="%.1f" font-family="monospace" font-size="10" fill="#cccccc">%.0f</text>`,
		legendX+legendW+3, legendY+5, maxSpeed)
	sb.WriteString("\n")
	fmt.Fprintf(&sb,
		`  <text x="%.1f" y="%.1f" font-family="monospace" font-size="10" fill="#cccccc">0</text>`,
		legendX+legendW+3, legendY+legendH)
	sb.WriteString("\n")
	fmt.Fprintf(&sb,
		`  <text x="%.1f" y="%.1f" font-family="monospace" font-size="9" fill="#888888">kph</text>`,
		legendX, legendY+legendH+12)
	sb.WriteString("\n")

	sb.WriteString("</svg>")
	return sb.String()
}

// speedColor maps speed [0..maxSpeed] to a blue→cyan→green→yellow→red gradient.
func speedColor(speed, maxSpeed float64) string {
	t := speed / maxSpeed
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	// 5-stop gradient: blue(0) → cyan(0.25) → green(0.5) → yellow(0.75) → red(1)
	type stop struct {
		r, g, b uint8
	}
	stops := [5]stop{
		{0x1a, 0x78, 0xc2}, // blue
		{0x00, 0xbc, 0xd4}, // cyan
		{0x27, 0xae, 0x60}, // green
		{0xf3, 0x9c, 0x12}, // yellow-orange
		{0xe7, 0x4c, 0x3c}, // red
	}
	seg := t * 4.0
	i := int(seg)
	if i >= 4 {
		i = 3
	}
	f := seg - float64(i)
	r := uint8(float64(stops[i].r) + f*float64(int(stops[i+1].r)-int(stops[i].r)))
	g := uint8(float64(stops[i].g) + f*float64(int(stops[i+1].g)-int(stops[i].g)))
	b := uint8(float64(stops[i].b) + f*float64(int(stops[i+1].b)-int(stops[i].b)))
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// ---------------------------------------------------------------------------
// Annotation parsing
// ---------------------------------------------------------------------------

// parseMapMarks parses "--mark" flag values: "dist_frac:label" or "dist_frac:label:color".
func parseMapMarks(raw []string) []mapMark {
	var out []mapMark
	for _, s := range raw {
		parts := strings.SplitN(s, ":", 3)
		if len(parts) < 2 {
			continue
		}
		frac, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		if err != nil {
			continue
		}
		m := mapMark{
			DistFrac: frac,
			Label:    strings.TrimSpace(parts[1]),
			Color:    "#f1c40f",
		}
		if len(parts) == 3 && strings.TrimSpace(parts[2]) != "" {
			m.Color = strings.TrimSpace(parts[2])
		}
		out = append(out, m)
	}
	return out
}
