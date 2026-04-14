package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	ldparser "github.com/mail/go-ldparser"
)

// ---------------------------------------------------------------------------
// analyze command dispatcher
// ---------------------------------------------------------------------------

func runAnalyze(args []string) {
	if len(args) == 0 {
		fatalJSON("analyze: subcommand required (braking, throttle)")
	}
	switch args[0] {
	case "braking":
		runAnalyzeBraking(args[1:])
	case "throttle":
		runAnalyzeThrottle(args[1:])
	case "tyre", "tire":
		runAnalyzeTyre(args[1:])
	case "median":
		runAnalyzeMedian(args[1:])
	case "deviation":
		runAnalyzeDeviation(args[1:])
	default:
		fatalJSON(fmt.Sprintf("analyze: unknown subcommand %q — use braking, throttle, tyre, median, or deviation", args[0]))
	}
}

// ---------------------------------------------------------------------------
// Response types — analyze braking
// ---------------------------------------------------------------------------

type brakingZoneDetail struct {
	T              float64 `json:"t"`
	TEnd           float64 `json:"t_end"`
	Duration       float64 `json:"duration"`
	EntrySpeed     float64 `json:"entry_speed"`
	ExitSpeed      float64 `json:"exit_speed"`
	PeakBrakePct   float64 `json:"peak_brake_pct"`
	TimeToPeak     float64 `json:"time_to_peak_s"`
	Hesitant       bool    `json:"hesitant"`
	HesitationType string  `json:"hesitation_type,omitempty"`
}

type brakingSpeedBand struct {
	Band           string  `json:"band"`
	Count          int     `json:"count"`
	HesitantCount  int     `json:"hesitant_count"`
	HesitantPct    float64 `json:"hesitant_pct"`
	MeanTimeToPeak float64 `json:"mean_time_to_peak_s"`
}

type brakingLapSummary struct {
	TotalZones     int     `json:"total_zones"`
	HesitantZones  int     `json:"hesitant_zones"`
	HesitantPct    float64 `json:"hesitant_pct"`
	MeanTimeToPeak float64 `json:"mean_time_to_peak_s"`
}

type brakingAnalysisLap struct {
	Number     *int                `json:"number"`
	StartTime  float64             `json:"start_time"`
	EndTime    float64             `json:"end_time"`
	LapTime    float64             `json:"lap_time,omitempty"`
	Zones      []brakingZoneDetail `json:"zones"`
	SpeedBands []brakingSpeedBand  `json:"speed_bands,omitempty"`
	Summary    brakingLapSummary   `json:"summary"`
}

type brakingAnalysisFile struct {
	Path     string               `json:"path"`
	Laps     []brakingAnalysisLap `json:"laps"`
	Warnings []string             `json:"warnings,omitempty"`
}

type brakingAnalysisResponse struct {
	Files    []brakingAnalysisFile `json:"files"`
	Warnings []string              `json:"warnings,omitempty"`
}

// ---------------------------------------------------------------------------
// analyze braking
// ---------------------------------------------------------------------------

func runAnalyzeBraking(args []string) {
	fs := flag.NewFlagSet("analyze braking", flag.ExitOnError)
	lapFlag := fs.String("lap", "all", "lap number or 'all'")
	format := fs.String("format", "json", "json or text")
	_ = fs.Bool("hesitation", false, "classify zones as hesitant/clean (always enabled)")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)
	files := loadFiles(filePaths)

	var globalWarnings []string
	resp := brakingAnalysisResponse{Files: make([]brakingAnalysisFile, 0, len(files))}

	for _, fr := range files {
		windows, errMsg := buildWindows(*lapFlag, fr.Laps, sessionEnd(fr))
		if errMsg != "" {
			globalWarnings = append(globalWarnings, fmt.Sprintf("%s: %s", fr.Path, errMsg))
			continue
		}

		brakeCh := findChannelFuzzy(fr.File, "Brake Pres Front", "Brake Pressure Front", "Brake Pres", "Brake Pressure", "Brake Pos", "Brake Position")
		speedCh := findChannelFuzzy(fr.File, "Ground Speed", "Speed over Ground", "GPS Speed", "Vehicle Speed", "Speed")

		var fileWarnings []string
		if brakeCh == nil {
			fileWarnings = append(fileWarnings, "brake channel not found — cannot analyze braking zones")
		}

		af := brakingAnalysisFile{
			Path:     fr.Path,
			Warnings: fileWarnings,
			Laps:     make([]brakingAnalysisLap, 0, len(windows)),
		}
		if brakeCh == nil {
			resp.Files = append(resp.Files, af)
			continue
		}

		for _, win := range windows {
			zones := analyzeBrakingZones(brakeCh, speedCh, win.from, win.to)
			bands := brakingBySpeedBand(zones)

			hesCount := 0
			sumTTP := 0.0
			for _, z := range zones {
				if z.Hesitant {
					hesCount++
				}
				sumTTP += z.TimeToPeak
			}
			hesitantPct := 0.0
			meanTTP := 0.0
			if len(zones) > 0 {
				hesitantPct = round3(100 * float64(hesCount) / float64(len(zones)))
				meanTTP = round3(sumTTP / float64(len(zones)))
			}

			af.Laps = append(af.Laps, brakingAnalysisLap{
				Number:     win.num,
				StartTime:  round3(win.from),
				EndTime:    round3(win.to),
				LapTime:    round3(win.lapTime),
				Zones:      zones,
				SpeedBands: bands,
				Summary: brakingLapSummary{
					TotalZones:     len(zones),
					HesitantZones:  hesCount,
					HesitantPct:    hesitantPct,
					MeanTimeToPeak: meanTTP,
				},
			})
		}
		resp.Files = append(resp.Files, af)
	}
	resp.Warnings = globalWarnings

	switch *format {
	case "text":
		writeAnalyzeBrakingText(&resp)
	default:
		writeJSON(resp)
	}
}

func analyzeBrakingZones(brakeCh, speedCh *ldparser.Channel, from, to float64) []brakingZoneDetail {
	brakeData, tStart := sliceChannel(brakeCh, from, to)
	if len(brakeData) == 0 {
		return nil
	}
	freq := float64(brakeCh.Freq)

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

	var speedData []float64
	if speedCh != nil {
		speedData, _ = sliceChannel(speedCh, from, to)
	}
	speedAt := func(i int) float64 {
		if len(speedData) == 0 || speedCh == nil {
			return 0
		}
		idx := i * int(brakeCh.Freq) / int(speedCh.Freq)
		if idx >= len(speedData) {
			idx = len(speedData) - 1
		}
		return speedData[idx]
	}

	minSamples := int(0.3 * freq)
	if minSamples < 1 {
		minSamples = 1
	}

	type rawZone struct{ start, end int }
	var zones []rawZone
	zoneStart := -1
	for i, v := range brakeData {
		if v >= threshold {
			if zoneStart < 0 {
				zoneStart = i
			}
		} else if zoneStart >= 0 {
			if i-zoneStart >= minSamples {
				zones = append(zones, rawZone{zoneStart, i})
			}
			zoneStart = -1
		}
	}
	if zoneStart >= 0 && len(brakeData)-zoneStart >= minSamples {
		zones = append(zones, rawZone{zoneStart, len(brakeData)})
	}

	mergeGap := int(0.5 * freq)
	merged := make([]rawZone, 0, len(zones))
	for _, z := range zones {
		if len(merged) > 0 && z.start-merged[len(merged)-1].end < mergeGap {
			merged[len(merged)-1].end = z.end
		} else {
			merged = append(merged, z)
		}
	}

	out := make([]brakingZoneDetail, 0, len(merged))
	for _, z := range merged {
		zone := brakeData[z.start:z.end]
		peakBrake := 0.0
		peakIdx := 0
		for i, v := range zone {
			if v > peakBrake {
				peakBrake = v
				peakIdx = i
			}
		}
		timeToPeak := round3(float64(peakIdx) / freq)
		t := round3(tStart + float64(z.start)/freq)
		tEnd := round3(tStart + float64(z.end)/freq)
		hesitant, hesType := classifyBrakingHesitation(zone, peakBrake)

		out = append(out, brakingZoneDetail{
			T:              t,
			TEnd:           tEnd,
			Duration:       round3(tEnd - t),
			EntrySpeed:     round3(speedAt(z.start)),
			ExitSpeed:      round3(speedAt(z.end - 1)),
			PeakBrakePct:   round3(100 * peakBrake / maxBrake),
			TimeToPeak:     timeToPeak,
			Hesitant:       hesitant,
			HesitationType: hesType,
		})
	}
	return out
}

// classifyBrakingHesitation detects a stab-release-reapply pattern: a local
// minimum below 50% of peak that occurs before the peak is reached.
func classifyBrakingHesitation(zone []float64, peakVal float64) (bool, string) {
	if len(zone) < 5 || peakVal <= 0 {
		return false, ""
	}
	halfPeak := peakVal * 0.5
	peakIdx := 0
	for i, v := range zone {
		if v >= peakVal {
			peakIdx = i
			break
		}
	}
	if peakIdx < 3 {
		return false, ""
	}
	for i := 1; i < peakIdx-1; i++ {
		if zone[i] < halfPeak && zone[i] < zone[i-1] && zone[i] < zone[i+1] {
			return true, "stab_release"
		}
	}
	return false, ""
}

// speedBandLabel returns a speed band label for a given speed in km/h.
func speedBandLabel(kmh float64) string {
	switch {
	case kmh < 80:
		return "<80"
	case kmh < 120:
		return "80-120"
	case kmh < 160:
		return "120-160"
	case kmh < 200:
		return "160-200"
	default:
		return ">200"
	}
}

var speedBandOrder = []string{"<80", "80-120", "120-160", "160-200", ">200"}

func brakingBySpeedBand(zones []brakingZoneDetail) []brakingSpeedBand {
	type acc struct {
		count, hesCount int
		sumTTP          float64
	}
	m := map[string]*acc{}
	for _, z := range zones {
		b := speedBandLabel(z.EntrySpeed)
		if m[b] == nil {
			m[b] = &acc{}
		}
		m[b].count++
		m[b].sumTTP += z.TimeToPeak
		if z.Hesitant {
			m[b].hesCount++
		}
	}
	var bands []brakingSpeedBand
	for _, b := range speedBandOrder {
		a, ok := m[b]
		if !ok {
			continue
		}
		hesitantPct := 0.0
		meanTTP := 0.0
		if a.count > 0 {
			hesitantPct = round3(100 * float64(a.hesCount) / float64(a.count))
			meanTTP = round3(a.sumTTP / float64(a.count))
		}
		bands = append(bands, brakingSpeedBand{
			Band:           b,
			Count:          a.count,
			HesitantCount:  a.hesCount,
			HesitantPct:    hesitantPct,
			MeanTimeToPeak: meanTTP,
		})
	}
	return bands
}

func writeAnalyzeBrakingText(resp *brakingAnalysisResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, af := range resp.Files {
		fmt.Fprintf(tw, "=== %s ===\n", af.Path)
		for _, w := range af.Warnings {
			fmt.Fprintf(tw, "WARNING: %s\n", w)
		}
		for _, al := range af.Laps {
			if al.Number != nil {
				fmt.Fprintf(tw, "Lap %d  %d zones, %d hesitant (%.0f%%), mean_ttp=%.3fs\n",
					*al.Number, al.Summary.TotalZones, al.Summary.HesitantZones,
					al.Summary.HesitantPct, al.Summary.MeanTimeToPeak)
			} else {
				fmt.Fprintf(tw, "Session  %d zones, %d hesitant (%.0f%%), mean_ttp=%.3fs\n",
					al.Summary.TotalZones, al.Summary.HesitantZones,
					al.Summary.HesitantPct, al.Summary.MeanTimeToPeak)
			}
			if len(al.Zones) > 0 {
				fmt.Fprintln(tw, "t\tt_end\tdur\tspd_in\tspd_out\tpeak_brk%\tttp\thesitant")
				for _, z := range al.Zones {
					h := "-"
					if z.Hesitant {
						h = z.HesitationType
						if h == "" {
							h = "yes"
						}
					}
					fmt.Fprintf(tw, "%.3f\t%.3f\t%.3f\t%.0f\t%.0f\t%.0f\t%.3f\t%s\n",
						z.T, z.TEnd, z.Duration, z.EntrySpeed, z.ExitSpeed,
						z.PeakBrakePct, z.TimeToPeak, h)
				}
			}
			if len(al.SpeedBands) > 0 {
				fmt.Fprintln(tw, "\nSPEED BANDS")
				fmt.Fprintln(tw, "band\tcount\thesitant\thes%\tmean_ttp")
				for _, b := range al.SpeedBands {
					fmt.Fprintf(tw, "%s\t%d\t%d\t%.0f\t%.3f\n",
						b.Band, b.Count, b.HesitantCount, b.HesitantPct, b.MeanTimeToPeak)
				}
			}
			fmt.Fprintln(tw)
		}
	}
	tw.Flush()
}

// ---------------------------------------------------------------------------
// Response types — analyze throttle
// ---------------------------------------------------------------------------

type throttleApexDelay struct {
	T         float64  `json:"t"`
	ApexSpeed float64  `json:"apex_speed"`
	Delay20   *float64 `json:"delay_20_pct_s,omitempty"`
	Delay50   *float64 `json:"delay_50_pct_s,omitempty"`
	Delay90   *float64 `json:"delay_90_pct_s,omitempty"`
}

type throttleSpeedBand struct {
	Band        string  `json:"band"`
	Count       int     `json:"count"`
	MeanDelay20 float64 `json:"mean_delay_20_pct_s"`
	MeanDelay50 float64 `json:"mean_delay_50_pct_s"`
	MeanDelay90 float64 `json:"mean_delay_90_pct_s"`
}

type throttleAnalysisLap struct {
	Number     *int                `json:"number"`
	StartTime  float64             `json:"start_time"`
	EndTime    float64             `json:"end_time"`
	LapTime    float64             `json:"lap_time,omitempty"`
	Apexes     []throttleApexDelay `json:"apexes"`
	SpeedBands []throttleSpeedBand `json:"speed_bands,omitempty"`
}

type throttleAnalysisFile struct {
	Path     string               `json:"path"`
	Laps     []throttleAnalysisLap `json:"laps"`
	Warnings []string             `json:"warnings,omitempty"`
}

type throttleAnalysisResponse struct {
	Files    []throttleAnalysisFile `json:"files"`
	Warnings []string               `json:"warnings,omitempty"`
}

// ---------------------------------------------------------------------------
// analyze throttle
// ---------------------------------------------------------------------------

func runAnalyzeThrottle(args []string) {
	fs := flag.NewFlagSet("analyze throttle", flag.ExitOnError)
	lapFlag := fs.String("lap", "all", "lap number or 'all'")
	format := fs.String("format", "json", "json or text")
	_ = fs.Bool("apex-delay", false, "measure time from apex to throttle thresholds (always enabled)")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)
	files := loadFiles(filePaths)

	var globalWarnings []string
	resp := throttleAnalysisResponse{Files: make([]throttleAnalysisFile, 0, len(files))}

	for _, fr := range files {
		windows, errMsg := buildWindows(*lapFlag, fr.Laps, sessionEnd(fr))
		if errMsg != "" {
			globalWarnings = append(globalWarnings, fmt.Sprintf("%s: %s", fr.Path, errMsg))
			continue
		}

		speedCh := findChannelFuzzy(fr.File, "Ground Speed", "Speed over Ground", "GPS Speed", "Vehicle Speed", "Speed")
		throttleCh := findChannelFuzzy(fr.File, "Throttle Pos", "Throttle Position", "Throttle")
		latGCh := findChannelFuzzy(fr.File, "G Force Lat", "G Lat", "Lateral G", "AccelLat", "Lat Accel")

		var fileWarnings []string
		if speedCh == nil {
			fileWarnings = append(fileWarnings, "speed channel not found — cannot detect apexes")
		}
		if throttleCh == nil {
			fileWarnings = append(fileWarnings, "throttle channel not found — cannot measure throttle delay")
		}

		af := throttleAnalysisFile{
			Path:     fr.Path,
			Warnings: fileWarnings,
			Laps:     make([]throttleAnalysisLap, 0, len(windows)),
		}
		if speedCh == nil || throttleCh == nil {
			resp.Files = append(resp.Files, af)
			continue
		}

		for _, win := range windows {
			apexEvents := detectCorners(speedCh, latGCh, win.from, win.to)
			throttleData, throttleTStart := sliceChannel(throttleCh, win.from, win.to)

			maxThrottle := 0.0
			for _, v := range throttleData {
				if v > maxThrottle {
					maxThrottle = v
				}
			}
			tFreq := float64(throttleCh.Freq)

			apexes := make([]throttleApexDelay, 0, len(apexEvents))
			if maxThrottle > 0 && len(throttleData) > 0 && tFreq > 0 {
				maxLookSamples := int(5.0 * tFreq)

				findDelay := func(startIdx int, pct float64) *float64 {
					thresh := maxThrottle * pct / 100.0
					for i := startIdx; i < len(throttleData) && i-startIdx < maxLookSamples; i++ {
						if throttleData[i] >= thresh {
							d := round3(float64(i-startIdx) / tFreq)
							return &d
						}
					}
					return nil
				}

				for _, apex := range apexEvents {
					apexRelT := apex.T - throttleTStart
					startIdx := int(apexRelT * tFreq)
					if startIdx < 0 {
						startIdx = 0
					}
					if startIdx >= len(throttleData) {
						continue
					}
					apexes = append(apexes, throttleApexDelay{
						T:         apex.T,
						ApexSpeed: apex.Speed,
						Delay20:   findDelay(startIdx, 20),
						Delay50:   findDelay(startIdx, 50),
						Delay90:   findDelay(startIdx, 90),
					})
				}
			}

			af.Laps = append(af.Laps, throttleAnalysisLap{
				Number:     win.num,
				StartTime:  round3(win.from),
				EndTime:    round3(win.to),
				LapTime:    round3(win.lapTime),
				Apexes:     apexes,
				SpeedBands: throttleBySpeedBand(apexes),
			})
		}
		resp.Files = append(resp.Files, af)
	}
	resp.Warnings = globalWarnings

	switch *format {
	case "text":
		writeAnalyzeThrottleText(&resp)
	default:
		writeJSON(resp)
	}
}

func throttleBySpeedBand(apexes []throttleApexDelay) []throttleSpeedBand {
	type acc struct {
		count          int
		sum20, n20     float64
		sum50, n50     float64
		sum90, n90     float64
	}
	m := map[string]*acc{}
	for _, a := range apexes {
		b := speedBandLabel(a.ApexSpeed)
		if m[b] == nil {
			m[b] = &acc{}
		}
		m[b].count++
		if a.Delay20 != nil {
			m[b].sum20 += *a.Delay20
			m[b].n20++
		}
		if a.Delay50 != nil {
			m[b].sum50 += *a.Delay50
			m[b].n50++
		}
		if a.Delay90 != nil {
			m[b].sum90 += *a.Delay90
			m[b].n90++
		}
	}
	var bands []throttleSpeedBand
	for _, b := range speedBandOrder {
		a, ok := m[b]
		if !ok {
			continue
		}
		band := throttleSpeedBand{Band: b, Count: a.count}
		if a.n20 > 0 {
			band.MeanDelay20 = round3(a.sum20 / a.n20)
		}
		if a.n50 > 0 {
			band.MeanDelay50 = round3(a.sum50 / a.n50)
		}
		if a.n90 > 0 {
			band.MeanDelay90 = round3(a.sum90 / a.n90)
		}
		bands = append(bands, band)
	}
	return bands
}

func writeAnalyzeThrottleText(resp *throttleAnalysisResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, af := range resp.Files {
		fmt.Fprintf(tw, "=== %s ===\n", af.Path)
		for _, w := range af.Warnings {
			fmt.Fprintf(tw, "WARNING: %s\n", w)
		}
		for _, al := range af.Laps {
			if al.Number != nil {
				fmt.Fprintf(tw, "Lap %d  %d apexes\n", *al.Number, len(al.Apexes))
			} else {
				fmt.Fprintf(tw, "Session  %d apexes\n", len(al.Apexes))
			}
			if len(al.Apexes) > 0 {
				fmt.Fprintln(tw, "t\tapex_spd\tdelay_20%\tdelay_50%\tdelay_90%")
				for _, a := range al.Apexes {
					d20, d50, d90 := "-", "-", "-"
					if a.Delay20 != nil {
						d20 = fmt.Sprintf("%.3f", *a.Delay20)
					}
					if a.Delay50 != nil {
						d50 = fmt.Sprintf("%.3f", *a.Delay50)
					}
					if a.Delay90 != nil {
						d90 = fmt.Sprintf("%.3f", *a.Delay90)
					}
					fmt.Fprintf(tw, "%.3f\t%.1f\t%s\t%s\t%s\n",
						a.T, a.ApexSpeed, d20, d50, d90)
				}
			}
			if len(al.SpeedBands) > 0 {
				fmt.Fprintln(tw, "\nSPEED BANDS (mean delay to throttle threshold)")
				fmt.Fprintln(tw, "band\tcount\tmean_d20%\tmean_d50%\tmean_d90%")
				for _, b := range al.SpeedBands {
					fmt.Fprintf(tw, "%s\t%d\t%.3f\t%.3f\t%.3f\n",
						b.Band, b.Count, b.MeanDelay20, b.MeanDelay50, b.MeanDelay90)
				}
			}
			fmt.Fprintln(tw)
		}
	}
	tw.Flush()
}

// ---------------------------------------------------------------------------
// Response types — analyze tyre
// ---------------------------------------------------------------------------

type tyreCornerStats struct {
	Mean float64 `json:"mean"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Std  float64 `json:"std"`
	N    int     `json:"n"`
}

type tyreBalance struct {
	FrontRearDelta float64 `json:"front_rear_delta"`
	LeftRightDelta float64 `json:"left_right_delta"`
}

type tyreAnalysisLap struct {
	Number  *int                        `json:"number,omitempty"`
	Corners map[string]*tyreCornerStats `json:"corners"`
	Balance *tyreBalance                `json:"balance,omitempty"`
}

type tyreAnalysisFile struct {
	Path     string            `json:"path"`
	Channels map[string]string `json:"channels"`
	Laps     []tyreAnalysisLap `json:"laps"`
	Warnings []string          `json:"warnings,omitempty"`
}

type tyreAnalysisResponse struct {
	Files    []tyreAnalysisFile `json:"files"`
	Warnings []string           `json:"warnings,omitempty"`
}

type tyreChannelMap struct {
	FL, FR, RL, RR *ldparser.Channel
}

// ---------------------------------------------------------------------------
// analyze tyre implementation
// ---------------------------------------------------------------------------

func runAnalyzeTyre(args []string) {
	fs := flag.NewFlagSet("analyze tyre", flag.ExitOnError)
	lapFlag := fs.String("lap", "all", "lap number, 'all', or 'best'")
	format := fs.String("format", "json", "json or text")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)

	if len(filePaths) == 0 {
		fatalJSON("analyze tyre: no file paths provided")
	}

	files := loadFiles(filePaths)
	resp := tyreAnalysisResponse{}

	for _, fr := range files {
		af := tyreAnalysisFile{Path: fr.Path, Channels: map[string]string{}}
		m, names := detectTyreChannels(fr.File)
		for role, name := range names {
			af.Channels[role] = name
		}
		if m.FL == nil && m.FR == nil && m.RL == nil && m.RR == nil {
			af.Warnings = append(af.Warnings, "no tyre temperature channels found")
			resp.Files = append(resp.Files, af)
			continue
		}

		wins, errMsg := buildWindows(*lapFlag, fr.Laps, sessionEnd(fr))
		if errMsg != "" {
			af.Warnings = append(af.Warnings, errMsg)
			resp.Files = append(resp.Files, af)
			continue
		}

		for _, win := range wins {
			corners := computeTyreCorners(m, win.from, win.to)
			lap := tyreAnalysisLap{Number: win.num, Corners: corners}
			lap.Balance = computeTyreBalance(corners)
			af.Laps = append(af.Laps, lap)
		}
		resp.Files = append(resp.Files, af)
	}

	switch *format {
	case "text":
		writeAnalyzeTyreText(&resp)
	default:
		writeJSON(resp)
	}
}

func detectTyreChannels(f *ldparser.File) (*tyreChannelMap, map[string]string) {
	m := &tyreChannelMap{}
	names := map[string]string{}

	type cornerDef struct {
		field    **ldparser.Channel
		role     string
		patterns []string
	}
	corners := []cornerDef{
		{&m.FL, "FL", []string{"Tyre Temp FL", "Tire Temp FL", "Tyre Temp Front Left", "Tire Temp Front Left", "FL Tyre Temp", "TyreTemp_FL", "TireTemp_FL"}},
		{&m.FR, "FR", []string{"Tyre Temp FR", "Tire Temp FR", "Tyre Temp Front Right", "Tire Temp Front Right", "FR Tyre Temp", "TyreTemp_FR", "TireTemp_FR"}},
		{&m.RL, "RL", []string{"Tyre Temp RL", "Tire Temp RL", "Tyre Temp Rear Left", "Tire Temp Rear Left", "RL Tyre Temp", "TyreTemp_RL", "TireTemp_RL"}},
		{&m.RR, "RR", []string{"Tyre Temp RR", "Tire Temp RR", "Tyre Temp Rear Right", "Tire Temp Rear Right", "RR Tyre Temp", "TyreTemp_RR", "TireTemp_RR"}},
	}
	for _, c := range corners {
		ch := findChannelFuzzy(f, c.patterns...)
		if ch != nil {
			*c.field = ch
			names[c.role] = ch.Name
		}
	}
	return m, names
}

func computeTyreCorners(m *tyreChannelMap, from, to float64) map[string]*tyreCornerStats {
	result := map[string]*tyreCornerStats{}
	type entry struct {
		role string
		ch   *ldparser.Channel
	}
	for _, e := range []entry{{"FL", m.FL}, {"FR", m.FR}, {"RL", m.RL}, {"RR", m.RR}} {
		if e.ch == nil {
			continue
		}
		data, _ := sliceChannel(e.ch, from, to)
		if len(data) == 0 {
			continue
		}
		mn, mx, mean, std, _, _, _, n := computeStats(data)
		result[e.role] = &tyreCornerStats{
			Mean: round3(mean),
			Min:  round3(mn),
			Max:  round3(mx),
			Std:  round3(std),
			N:    n,
		}
	}
	return result
}

func computeTyreBalance(corners map[string]*tyreCornerStats) *tyreBalance {
	if len(corners) == 0 {
		return nil
	}
	get := func(k string) (float64, bool) {
		if c, ok := corners[k]; ok {
			return c.Mean, true
		}
		return 0, false
	}
	fl, hasFL := get("FL")
	fr, hasFR := get("FR")
	rl, hasRL := get("RL")
	rr, hasRR := get("RR")

	b := &tyreBalance{}
	frontN, rearN := 0, 0
	frontSum, rearSum := 0.0, 0.0
	if hasFL {
		frontSum += fl
		frontN++
	}
	if hasFR {
		frontSum += fr
		frontN++
	}
	if hasRL {
		rearSum += rl
		rearN++
	}
	if hasRR {
		rearSum += rr
		rearN++
	}
	if frontN > 0 && rearN > 0 {
		b.FrontRearDelta = round3(frontSum/float64(frontN) - rearSum/float64(rearN))
	}
	leftN, rightN := 0, 0
	leftSum, rightSum := 0.0, 0.0
	if hasFL {
		leftSum += fl
		leftN++
	}
	if hasRL {
		leftSum += rl
		leftN++
	}
	if hasFR {
		rightSum += fr
		rightN++
	}
	if hasRR {
		rightSum += rr
		rightN++
	}
	if leftN > 0 && rightN > 0 {
		b.LeftRightDelta = round3(leftSum/float64(leftN) - rightSum/float64(rightN))
	}
	return b
}

func writeAnalyzeTyreText(resp *tyreAnalysisResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, af := range resp.Files {
		fmt.Fprintf(tw, "=== %s ===\n", af.Path)
		if len(af.Channels) > 0 {
			fmt.Fprint(tw, "channels:")
			for role, name := range af.Channels {
				fmt.Fprintf(tw, " %s=%s", role, name)
			}
			fmt.Fprintln(tw)
		}
		for _, w := range af.Warnings {
			fmt.Fprintf(tw, "WARNING: %s\n", w)
		}
		for _, al := range af.Laps {
			if al.Number != nil {
				fmt.Fprintf(tw, "Lap %d\n", *al.Number)
			} else {
				fmt.Fprintln(tw, "Session")
			}
			if len(al.Corners) > 0 {
				fmt.Fprintln(tw, "corner\tmean\tmin\tmax\tstd")
				for _, role := range []string{"FL", "FR", "RL", "RR"} {
					if c, ok := al.Corners[role]; ok {
						fmt.Fprintf(tw, "%s\t%.1f\t%.1f\t%.1f\t%.1f\n",
							role, c.Mean, c.Min, c.Max, c.Std)
					}
				}
			}
			if al.Balance != nil {
				fmt.Fprintf(tw, "balance  front-rear: %+.1f  left-right: %+.1f\n",
					al.Balance.FrontRearDelta, al.Balance.LeftRightDelta)
			}
			fmt.Fprintln(tw)
		}
	}
	tw.Flush()
}
