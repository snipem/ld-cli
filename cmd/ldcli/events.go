package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	ldparser "github.com/mail/go-ldparser"
)

// ---------------------------------------------------------------------------
// Response types — events
// ---------------------------------------------------------------------------

type eventsResponse struct {
	Files    []fileEvents `json:"files"`
	Warnings []string     `json:"warnings,omitempty"`
}

type fileEvents struct {
	Path string      `json:"path"`
	Laps []lapEvents `json:"laps"`
}

type lapEvents struct {
	Number     *int    `json:"number"`
	StartTime  float64 `json:"start_time"`
	EndTime    float64 `json:"end_time"`
	LapTime    float64 `json:"lap_time,omitempty"`
	EventCount int     `json:"event_count"`
	Events     []event `json:"events"`
}

type event struct {
	Type     string  `json:"type"`
	T        float64 `json:"t"`
	TEnd     float64 `json:"t_end,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Note     string  `json:"note,omitempty"`
	// gear_shift
	GearFrom  int    `json:"gear_from,omitempty"`
	GearTo    int    `json:"gear_to,omitempty"`
	Direction string `json:"direction,omitempty"`
	// braking_zone
	SpeedEntry float64 `json:"speed_entry,omitempty"`
	SpeedExit  float64 `json:"speed_exit,omitempty"`
	PeakDecelG float64 `json:"peak_decel_g,omitempty"`
	PeakBrake  float64 `json:"peak_brake_pct,omitempty"`
	// corner_apex
	Speed float64 `json:"speed,omitempty"`
	LatG  float64 `json:"lat_g,omitempty"`
	// lockup
	SlipRatio float64 `json:"slip_ratio,omitempty"`
	Wheel     string  `json:"wheel,omitempty"`
}

// ---------------------------------------------------------------------------
// events command
// ---------------------------------------------------------------------------

func runEvents(args []string) {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	lapFlag := fs.String("lap", "all", "")
	format := fs.String("format", "json", "")
	typeFilter := fs.String("type", "", "")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)
	files := loadFiles(filePaths)

	var warnings []string
	resp := eventsResponse{Files: make([]fileEvents, 0, len(files))}

	for _, fr := range files {
		windows, errMsg := buildWindows(*lapFlag, fr.Laps, sessionEnd(fr))
		if errMsg != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s", fr.Path, errMsg))
			continue
		}

		// Look up channels once per file
		speedCh := findChannelFuzzy(fr.File, "Ground Speed", "Speed over Ground", "GPS Speed", "Vehicle Speed", "Speed")
		brakeCh := findChannelFuzzy(fr.File, "Brake Pres Front", "Brake Pressure Front", "Brake Pres", "Brake Pressure", "Brake Pos", "Brake Position")
		throttleCh := findChannelFuzzy(fr.File, "Throttle Pos", "Throttle Position", "Throttle")
		gearCh := findChannelFuzzy(fr.File, "Gear", "Gear Position", "Current Gear")
		latGCh := findChannelFuzzy(fr.File, "G Force Lat", "G Lat", "Lateral G", "AccelLat", "Lat Accel")
		wsFLCh := findChannelFuzzy(fr.File, "Wheel Speed FL", "WheelSpeedFL", "Wheel Spd FL")
		wsFRCh := findChannelFuzzy(fr.File, "Wheel Speed FR", "WheelSpeedFR", "Wheel Spd FR")
		wsRLCh := findChannelFuzzy(fr.File, "Wheel Speed RL", "WheelSpeedRL", "Wheel Spd RL")
		wsRRCh := findChannelFuzzy(fr.File, "Wheel Speed RR", "WheelSpeedRR", "Wheel Spd RR")

		if gearCh == nil {
			warnings = append(warnings, fmt.Sprintf("%s: gear channel not found — gear_shift events skipped", fr.Path))
		}
		if brakeCh == nil {
			warnings = append(warnings, fmt.Sprintf("%s: brake channel not found — braking_zone events skipped", fr.Path))
		}
		if speedCh == nil {
			warnings = append(warnings, fmt.Sprintf("%s: speed channel not found — braking/corner events degraded", fr.Path))
		}
		if throttleCh == nil {
			warnings = append(warnings, fmt.Sprintf("%s: throttle channel not found — full_throttle_zone events skipped", fr.Path))
		}

		fe := fileEvents{Path: fr.Path, Laps: make([]lapEvents, 0, len(windows))}
		for _, win := range windows {
			events := detectAllEvents(fr.File, win, speedCh, brakeCh, throttleCh, gearCh, latGCh,
				wsFLCh, wsFRCh, wsRLCh, wsRRCh)
			sort.Slice(events, func(i, j int) bool { return events[i].T < events[j].T })
			if *typeFilter != "" {
				filtered := make([]event, 0, len(events))
				for _, e := range events {
					if e.Type == *typeFilter {
						filtered = append(filtered, e)
					}
				}
				events = filtered
			}
			fe.Laps = append(fe.Laps, lapEvents{
				Number:     win.num,
				StartTime:  round3(win.from),
				EndTime:    round3(win.to),
				LapTime:    round3(win.lapTime),
				EventCount: len(events),
				Events:     events,
			})
		}
		// Warn if brake channel exists but produced zero braking zones
		if brakeCh != nil {
			totalBraking := 0
			for _, le := range fe.Laps {
				for _, e := range le.Events {
					if e.Type == "braking_zone" {
						totalBraking++
					}
				}
			}
			if totalBraking == 0 {
				warnings = append(warnings, fmt.Sprintf("%s: braking_zone detection found 0 zones despite brake channel %q being present — check channel scale or signal range", fr.Path, brakeCh.Name))
			}
		}
		resp.Files = append(resp.Files, fe)
	}
	resp.Warnings = warnings

	switch *format {
	case "csv":
		writeEventsCSV(&resp)
	case "text":
		writeEventsText(&resp)
	case "annotate":
		writeEventsAnnotate(&resp)
	default:
		writeJSON(resp)
	}
}

// ---------------------------------------------------------------------------
// Event detection — dispatcher
// ---------------------------------------------------------------------------

func detectAllEvents(f *ldparser.File, win lapWindow,
	speedCh, brakeCh, throttleCh, gearCh, latGCh,
	wsFLCh, wsFRCh, wsRLCh, wsRRCh *ldparser.Channel) []event {

	var events []event

	if gearCh != nil {
		events = append(events, detectGearShifts(gearCh, win.from, win.to)...)
	}
	if brakeCh != nil {
		events = append(events, detectBrakingZones(brakeCh, speedCh, win.from, win.to)...)
	}
	if speedCh != nil {
		events = append(events, detectCorners(speedCh, latGCh, win.from, win.to)...)
	}
	if throttleCh != nil {
		events = append(events, detectFullThrottleZones(throttleCh, win.from, win.to)...)
	}
	if speedCh != nil {
		wsChs := [4]*ldparser.Channel{wsFLCh, wsFRCh, wsRLCh, wsRRCh}
		wsNames := [4]string{"FL", "FR", "RL", "RR"}
		for i, wsCh := range wsChs {
			if wsCh != nil {
				events = append(events, detectLockups(speedCh, wsCh, wsNames[i], win.from, win.to)...)
			}
		}
	}
	return events
}

// ---------------------------------------------------------------------------
// Gear shifts
// ---------------------------------------------------------------------------

func detectGearShifts(gearCh *ldparser.Channel, from, to float64) []event {
	data, tStart := sliceChannel(gearCh, from, to)
	if len(data) < 2 {
		return nil
	}
	freq := float64(gearCh.Freq)
	var events []event
	prev := int(math.Round(data[0]))
	for i := 1; i < len(data); i++ {
		curr := int(math.Round(data[i]))
		if curr == prev || curr <= 0 || prev <= 0 {
			prev = curr
			continue
		}
		// Require the new gear to persist for at least 2 samples (filter glitches)
		if i+1 < len(data) && int(math.Round(data[i+1])) != curr {
			continue
		}
		t := round3(tStart + float64(i)/freq)
		dir := "up"
		if curr < prev {
			dir = "down"
		}
		events = append(events, event{
			Type:      "gear_shift",
			T:         t,
			GearFrom:  prev,
			GearTo:    curr,
			Direction: dir,
			Note:      fmt.Sprintf("%s %d→%d", dir, prev, curr),
		})
		prev = curr
	}
	return events
}

// ---------------------------------------------------------------------------
// Braking zones
// ---------------------------------------------------------------------------

func detectBrakingZones(brakeCh, speedCh *ldparser.Channel, from, to float64) []event {
	brakeData, tStart := sliceChannel(brakeCh, from, to)
	if len(brakeData) == 0 {
		return nil
	}
	freq := float64(brakeCh.Freq)

	// Adaptive threshold: 10% of session max brake value
	maxBrake := 0.0
	for _, v := range brakeData {
		if v > maxBrake {
			maxBrake = v
		}
	}
	if maxBrake <= 0 {
		return nil
	}
	threshold := maxBrake * 0.10

	// Get speed data at matching resolution (resample to brake freq if needed)
	var speedData []float64
	if speedCh != nil {
		speedData, _ = sliceChannel(speedCh, from, to)
	}
	speedAt := func(i int) float64 {
		if speedData == nil || len(speedData) == 0 {
			return 0
		}
		idx := i * int(brakeCh.Freq) / int(speedCh.Freq)
		if idx >= len(speedData) {
			idx = len(speedData) - 1
		}
		return speedData[idx]
	}

	minDuration := 0.3 // seconds
	minSamples := int(minDuration * freq)
	if minSamples < 1 {
		minSamples = 1
	}

	type zone struct{ start, end int }
	var zones []zone
	zoneStart := -1
	for i, v := range brakeData {
		if v >= threshold {
			if zoneStart < 0 {
				zoneStart = i
			}
		} else {
			if zoneStart >= 0 {
				if i-zoneStart >= minSamples {
					zones = append(zones, zone{zoneStart, i})
				}
				zoneStart = -1
			}
		}
	}
	if zoneStart >= 0 && len(brakeData)-zoneStart >= minSamples {
		zones = append(zones, zone{zoneStart, len(brakeData)})
	}

	// Merge zones closer than 0.5s
	mergeGap := int(0.5 * freq)
	merged := make([]zone, 0, len(zones))
	for _, z := range zones {
		if len(merged) > 0 && z.start-merged[len(merged)-1].end < mergeGap {
			merged[len(merged)-1].end = z.end
		} else {
			merged = append(merged, z)
		}
	}

	events := make([]event, 0, len(merged))
	for _, z := range merged {
		peakBrake := 0.0
		for _, v := range brakeData[z.start:z.end] {
			if v > peakBrake {
				peakBrake = v
			}
		}
		t := round3(tStart + float64(z.start)/freq)
		tEnd := round3(tStart + float64(z.end)/freq)
		dur := round3(tEnd - t)
		entrySpeed := speedAt(z.start)
		exitSpeed := speedAt(z.end - 1)

		// Peak deceleration from speed channel
		peakDecelG := 0.0
		if speedData != nil && speedCh != nil {
			sf := float64(speedCh.Freq)
			siStart := int(float64(z.start) / freq * sf)
			siEnd := int(float64(z.end) / freq * sf)
			if siEnd > len(speedData) {
				siEnd = len(speedData)
			}
			for i := siStart + 1; i < siEnd; i++ {
				dv := (speedData[i-1] - speedData[i]) * sf // km/h per second (delta * freq)
				g := dv / 3.6 / 9.81                       // → m/s² → G
				if g > peakDecelG {
					peakDecelG = g
				}
			}
		}

		events = append(events, event{
			Type:       "braking_zone",
			T:          t,
			TEnd:       tEnd,
			Duration:   dur,
			SpeedEntry: round3(entrySpeed),
			SpeedExit:  round3(exitSpeed),
			PeakDecelG: round3(peakDecelG),
			PeakBrake:  round3(100 * peakBrake / maxBrake),
			Note:       fmt.Sprintf("from %.0f km/h, peak %.1fG, braked to %.0f km/h", entrySpeed, peakDecelG, exitSpeed),
		})
	}
	return events
}

// ---------------------------------------------------------------------------
// Corner apexes
// ---------------------------------------------------------------------------

func detectCorners(speedCh, latGCh *ldparser.Channel, from, to float64) []event {
	speedData, tStart := sliceChannel(speedCh, from, to)
	if len(speedData) < 10 {
		return nil
	}
	freq := float64(speedCh.Freq)

	maxSpeed := 0.0
	for _, v := range speedData {
		if v > maxSpeed {
			maxSpeed = v
		}
	}
	if maxSpeed <= 0 {
		return nil
	}

	var latGData []float64
	if latGCh != nil {
		latGData, _ = sliceChannel(latGCh, from, to)
	}
	latGAt := func(i int) float64 {
		if len(latGData) == 0 {
			return 0
		}
		idx := i * int(speedCh.Freq) / int(latGCh.Freq)
		if idx >= len(latGData) {
			idx = len(latGData) - 1
		}
		return math.Abs(latGData[idx])
	}

	// Smooth speed to avoid noise-driven false minima
	smooth := movingAverage(speedData, max(int(freq/2), 3))

	// Find local minima in smoothed speed
	window := max(int(freq*1.0), 5) // 1-second window either side
	latGThresh := 0.5                // G
	speedPctMax := 0.80              // only flag if speed < 80% of session max

	var events []event
	lastApexIdx := -window * 3 // prevent clustering

	for i := window; i < len(smooth)-window; i++ {
		v := smooth[i]
		if v > maxSpeed*speedPctMax {
			continue // too fast to be a corner
		}
		isMin := true
		for k := i - window; k <= i+window; k++ {
			if k != i && smooth[k] < v {
				isMin = false
				break
			}
		}
		if !isMin {
			continue
		}
		if i-lastApexIdx < window {
			continue // too close to previous apex
		}

		lg := latGAt(i)
		if latGData != nil && lg < latGThresh {
			continue // not a real corner
		}

		t := round3(tStart + float64(i)/freq)
		events = append(events, event{
			Type:  "corner_apex",
			T:     t,
			Speed: round3(v),
			LatG:  round3(lg),
			Note:  fmt.Sprintf("apex at %.0f km/h, %.2fG lateral", v, lg),
		})
		lastApexIdx = i
	}
	return events
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Full throttle zones
// ---------------------------------------------------------------------------

func detectFullThrottleZones(throttleCh *ldparser.Channel, from, to float64) []event {
	data, tStart := sliceChannel(throttleCh, from, to)
	if len(data) == 0 {
		return nil
	}
	freq := float64(throttleCh.Freq)

	maxThrottle := 0.0
	for _, v := range data {
		if v > maxThrottle {
			maxThrottle = v
		}
	}
	if maxThrottle <= 0 {
		return nil
	}
	threshold := maxThrottle * 0.95
	minDuration := 1.0
	minSamples := int(minDuration * freq)
	if minSamples < 1 {
		minSamples = 1
	}

	var events []event
	zoneStart := -1
	for i, v := range data {
		if v >= threshold {
			if zoneStart < 0 {
				zoneStart = i
			}
		} else {
			if zoneStart >= 0 {
				dur := float64(i-zoneStart) / freq
				if i-zoneStart >= minSamples {
					t := round3(tStart + float64(zoneStart)/freq)
					tEnd := round3(tStart + float64(i)/freq)
					events = append(events, event{
						Type:     "full_throttle_zone",
						T:        t,
						TEnd:     tEnd,
						Duration: round3(dur),
						Note:     fmt.Sprintf("full throttle for %.1fs", dur),
					})
				}
				zoneStart = -1
			}
		}
	}
	if zoneStart >= 0 {
		dur := float64(len(data)-zoneStart) / freq
		if len(data)-zoneStart >= minSamples {
			t := round3(tStart + float64(zoneStart)/freq)
			tEnd := round3(tStart + float64(len(data))/freq)
			events = append(events, event{
				Type:     "full_throttle_zone",
				T:        t,
				TEnd:     tEnd,
				Duration: round3(dur),
				Note:     fmt.Sprintf("full throttle for %.1fs", dur),
			})
		}
	}
	return events
}

// ---------------------------------------------------------------------------
// Lockups
// ---------------------------------------------------------------------------

func detectLockups(speedCh, wheelCh *ldparser.Channel, wheelName string, from, to float64) []event {
	speedData, tStart := sliceChannel(speedCh, from, to)
	wheelData, _ := sliceChannel(wheelCh, from, to)
	if len(speedData) == 0 || len(wheelData) == 0 {
		return nil
	}

	freq := float64(wheelCh.Freq)
	minDuration := 0.1
	minSamples := int(minDuration * freq)
	if minSamples < 1 {
		minSamples = 1
	}
	slipThreshold := 0.15

	speedAt := func(i int) float64 {
		idx := i * int(wheelCh.Freq) / int(speedCh.Freq)
		if idx >= len(speedData) {
			idx = len(speedData) - 1
		}
		return speedData[idx]
	}

	var events []event
	zoneStart := -1
	peakSlip := 0.0

	for i, ws := range wheelData {
		vs := speedAt(i)
		var slip float64
		if vs > 5 { // only detect when moving > 5 km/h
			slip = (vs - ws) / vs
			if slip < 0 {
				slip = 0
			}
		}
		if slip >= slipThreshold {
			if zoneStart < 0 {
				zoneStart = i
				peakSlip = slip
			} else if slip > peakSlip {
				peakSlip = slip
			}
		} else {
			if zoneStart >= 0 {
				if i-zoneStart >= minSamples {
					t := round3(tStart + float64(zoneStart)/freq)
					tEnd := round3(tStart + float64(i)/freq)
					dur := round3(tEnd - t)
					events = append(events, event{
						Type:      "lockup",
						T:         t,
						TEnd:      tEnd,
						Duration:  dur,
						SlipRatio: round3(peakSlip),
						Wheel:     wheelName,
						Note:      fmt.Sprintf("lockup %s, peak slip %.0f%%", wheelName, peakSlip*100),
					})
				}
				zoneStart = -1
				peakSlip = 0
			}
		}
	}
	return events
}

// ---------------------------------------------------------------------------
// Output formatters — events
// ---------------------------------------------------------------------------

func writeEventsCSV(resp *eventsResponse) {
	w := csv.NewWriter(os.Stdout)
	_ = w.Write([]string{"file", "lap", "type", "t", "t_end", "duration", "note"})
	for _, fe := range resp.Files {
		for _, le := range fe.Laps {
			ls := lapStr(le.Number)
			for _, e := range le.Events {
				_ = w.Write([]string{
					fe.Path, ls, e.Type,
					fmt.Sprintf("%.3f", e.T),
					fmt.Sprintf("%.3f", e.TEnd),
					fmt.Sprintf("%.3f", e.Duration),
					e.Note,
				})
			}
		}
	}
	w.Flush()
}

func writeEventsAnnotate(resp *eventsResponse) {
	for _, fe := range resp.Files {
		for _, le := range fe.Laps {
			for _, e := range le.Events {
				fmt.Printf("%.3f:%s\n", e.T, annotateLabel(e))
			}
		}
	}
	for _, w := range resp.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
}

func annotateLabel(e event) string {
	var label string
	switch e.Type {
	case "braking_zone":
		label = fmt.Sprintf("brake %.0fkm/h", e.SpeedEntry)
	case "corner_apex":
		label = fmt.Sprintf("apex %.0fkm/h", e.Speed)
	case "gear_shift":
		label = fmt.Sprintf("%s %d>%d", e.Direction, e.GearFrom, e.GearTo)
	case "full_throttle_zone":
		label = "full thr"
	case "lockup":
		label = fmt.Sprintf("lockup %s", e.Wheel)
	default:
		label = e.Type
		if e.Note != "" {
			label = e.Note
		}
	}
	if len(label) > 20 {
		label = label[:20]
	}
	return label
}

func writeEventsText(resp *eventsResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, fe := range resp.Files {
		fmt.Fprintf(tw, "=== %s ===\n", fe.Path)
		for _, le := range fe.Laps {
			if le.Number != nil {
				fmt.Fprintf(tw, "Lap %d  (%d events)\n", *le.Number, le.EventCount)
			} else {
				fmt.Fprintf(tw, "Full session  (%d events)\n", le.EventCount)
			}

			// Group events by type for compact per-type tables.
			byType := map[string][]event{}
			typeOrder := []string{"braking_zone", "corner_apex", "full_throttle_zone", "gear_shift", "lockup"}
			for _, e := range le.Events {
				byType[e.Type] = append(byType[e.Type], e)
			}
			// also catch any unknown types
			for _, e := range le.Events {
				found := false
				for _, t := range typeOrder {
					if t == e.Type {
						found = true
						break
					}
				}
				if !found {
					typeOrder = append(typeOrder, e.Type)
				}
			}

			for _, typ := range typeOrder {
				evs := byType[typ]
				if len(evs) == 0 {
					continue
				}
				fmt.Fprintf(tw, "\n%s  (%d)\n", strings.ToUpper(typ), len(evs))
				switch typ {
				case "braking_zone":
					fmt.Fprintln(tw, "t\tt_end\tdur\tspd_in\tspd_out\tpeak_g\tpeak_brk")
					for _, e := range evs {
						fmt.Fprintf(tw, "%.3f\t%.3f\t%.3f\t%.0f\t%.0f\t%.2f\t%.0f%%\n",
							e.T, e.TEnd, e.Duration, e.SpeedEntry, e.SpeedExit, e.PeakDecelG, e.PeakBrake)
					}
				case "corner_apex":
					fmt.Fprintln(tw, "t\tspeed\tlat_g")
					for _, e := range evs {
						fmt.Fprintf(tw, "%.3f\t%.1f\t%.2f\n", e.T, e.Speed, e.LatG)
					}
				case "full_throttle_zone":
					fmt.Fprintln(tw, "t\tt_end\tdur")
					for _, e := range evs {
						fmt.Fprintf(tw, "%.3f\t%.3f\t%.3f\n", e.T, e.TEnd, e.Duration)
					}
				case "gear_shift":
					fmt.Fprintln(tw, "t\tfrom\tto\tdir")
					for _, e := range evs {
						fmt.Fprintf(tw, "%.3f\t%d\t%d\t%s\n", e.T, e.GearFrom, e.GearTo, e.Direction)
					}
				case "lockup":
					fmt.Fprintln(tw, "t\tt_end\tdur\twheel\tslip")
					for _, e := range evs {
						fmt.Fprintf(tw, "%.3f\t%.3f\t%.3f\t%s\t%.2f\n",
							e.T, e.TEnd, e.Duration, e.Wheel, e.SlipRatio)
					}
				default:
					fmt.Fprintln(tw, "t\tnote")
					for _, e := range evs {
						fmt.Fprintf(tw, "%.3f\t%s\n", e.T, e.Note)
					}
				}
			}
			fmt.Fprintln(tw)
		}
	}
	if len(resp.Warnings) > 0 {
		fmt.Fprintf(tw, "WARNINGS:\n")
		for _, w := range resp.Warnings {
			fmt.Fprintf(tw, "  %s\n", w)
		}
	}
	tw.Flush()
}
