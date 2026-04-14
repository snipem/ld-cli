package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"text/tabwriter"

	ldparser "github.com/mail/go-ldparser"
)

// ---------------------------------------------------------------------------
// Response types — diff
// ---------------------------------------------------------------------------

type diffResponse struct {
	File            string      `json:"file"`
	File2           string      `json:"file2,omitempty"`
	Reference       lapRef      `json:"reference"`
	Comparison      lapRef      `json:"comparison"`
	DeltaTotal      float64     `json:"delta_total"`
	DeltaNote       string      `json:"delta_note"`
	Alignment       string      `json:"alignment"` // "lap_distance" or "time_fraction"
	Sectors         []secDelta  `json:"sectors"`
	Channels        []chanDelta `json:"channels,omitempty"`
	CumulativeDelta *cumDelta   `json:"cumulative_delta,omitempty"`
	Warnings        []string    `json:"warnings,omitempty"`
}

// cumDelta is a running time-gap trace across N points of a lap.
// delta[i] > 0 means comparison is losing time at that point.
type cumDelta struct {
	Points int       `json:"points"`
	TRef   []float64 `json:"t_ref"`  // session-relative time at each point boundary (from ref lap)
	Delta  []float64 `json:"delta"`  // cumulative time gap at that point (positive = cmp slower)
}

type lapRef struct {
	Lap     int     `json:"lap"`
	LapTime float64 `json:"lap_time"`
}

type secDelta struct {
	Sector   int        `json:"sector"`
	PctRange [2]float64 `json:"pct_range"`
	TRef     [2]float64 `json:"t_ref"`
	Delta    float64    `json:"delta"` // positive = comparison is SLOWER
	Summary  string     `json:"summary"`
}

type chanDelta struct {
	Name      string     `json:"name"`
	Unit      string     `json:"unit"`
	MeanRef   float64    `json:"mean_ref"`
	MeanCmp   float64    `json:"mean_cmp"`
	MeanDelta float64    `json:"mean_delta"` // mean_cmp - mean_ref
	WorstPt   *worstPoint `json:"worst_point,omitempty"`
}

type worstPoint struct {
	TRef  float64 `json:"t_ref"`
	VRef  float64 `json:"v_ref"`
	VCmp  float64 `json:"v_cmp"`
	Delta float64 `json:"delta"`
}

// ---------------------------------------------------------------------------
// diff command
// ---------------------------------------------------------------------------

func runDiff(args []string) {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	refFlag := fs.Int("ref", -1, "")
	cmpFlag := fs.Int("cmp", -1, "")
	sectorsFlag := fs.Int("sectors", 4, "")
	var chFlags multiFlag
	fs.Var(&chFlags, "ch", "")
	format := fs.String("format", "json", "")
	cumulativeFlag := fs.Bool("cumulative", false, "")
	cumPointsFlag := fs.Int("cum-points", 20, "")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)

	if *refFlag < 0 || *cmpFlag < 0 {
		fatalJSON("--ref and --cmp lap numbers are required")
	}

	files := loadFiles(filePaths)
	if len(files) == 0 {
		fatalJSON("no files specified")
	}

	// Support same file or two files
	refFR := files[0]
	cmpFR := files[0]
	if len(files) >= 2 {
		cmpFR = files[1]
	}

	// Find the requested laps
	refLap := findLap(refFR.Laps, *refFlag)
	cmpLap := findLap(cmpFR.Laps, *cmpFlag)
	if refLap == nil {
		fatalJSON(fmt.Sprintf("lap %d not found in %s", *refFlag, refFR.Path))
	}
	if cmpLap == nil {
		fatalJSON(fmt.Sprintf("lap %d not found in %s", *cmpFlag, cmpFR.Path))
	}

	var warnings []string
	deltaTotal := round3(cmpLap.LapTime - refLap.LapTime)

	note := fmt.Sprintf("lap %d is %.3fs %s than lap %d",
		*cmpFlag, math.Abs(deltaTotal),
		map[bool]string{true: "FASTER", false: "SLOWER"}[deltaTotal < 0],
		*refFlag)

	// Determine alignment method
	distChRef := findChannelFuzzy(refFR.File, "Lap Distance", "LapDist", "Distance")
	distChCmp := findChannelFuzzy(cmpFR.File, "Lap Distance", "LapDist", "Distance")
	alignment := "time_fraction"
	if distChRef != nil && distChCmp != nil {
		alignment = "lap_distance"
	} else if distChRef != nil || distChCmp != nil {
		warnings = append(warnings, "Lap Distance channel only found in one file — falling back to time_fraction alignment")
	}

	sectors := computeSectorDeltas(refFR.File, refLap, cmpFR.File, cmpLap,
		distChRef, distChCmp, *sectorsFlag, alignment)

	// Channel deltas
	var chanDeltas []chanDelta
	if len(chFlags) == 0 {
		// Default: speed + throttle + brake
		for _, name := range []string{"Ground Speed", "Throttle Pos", "Brake Pres Front"} {
			chFlags = append(chFlags, name)
		}
	}
	for _, pat := range []string(chFlags) {
		var refCh, cmpCh *ldparser.Channel
		for i := range refFR.File.Channels {
			if matchPattern(refFR.File.Channels[i].Name, pat) {
				refCh = &refFR.File.Channels[i]
				break
			}
		}
		for i := range cmpFR.File.Channels {
			if matchPattern(cmpFR.File.Channels[i].Name, pat) {
				cmpCh = &cmpFR.File.Channels[i]
				break
			}
		}
		if refCh == nil || cmpCh == nil {
			warnings = append(warnings, fmt.Sprintf("channel %q not found in both files — skipped", pat))
			continue
		}
		cd := computeChanDelta(refCh, refLap, cmpCh, cmpLap)
		chanDeltas = append(chanDeltas, cd)
	}

	resp := diffResponse{
		File:       refFR.Path,
		Reference:  lapRef{*refFlag, round3(refLap.LapTime)},
		Comparison: lapRef{*cmpFlag, round3(cmpLap.LapTime)},
		DeltaTotal: deltaTotal,
		DeltaNote:  note,
		Alignment:  alignment,
		Sectors:    sectors,
		Channels:   chanDeltas,
		Warnings:   warnings,
	}
	if len(files) >= 2 {
		resp.File2 = cmpFR.Path
	}
	if *cumulativeFlag {
		n := *cumPointsFlag
		if n < 2 {
			n = 2
		}
		resp.CumulativeDelta = computeCumulativeDelta(refFR.File, refLap, cmpFR.File, cmpLap,
			distChRef, distChCmp, n, alignment)
	}

	switch *format {
	case "text":
		writeDiffText(&resp)
	default:
		writeJSON(resp)
	}
}

// ---------------------------------------------------------------------------
// Sector delta computation
// ---------------------------------------------------------------------------

func findLap(laps []ldparser.Lap, num int) *ldparser.Lap {
	for i := range laps {
		if laps[i].Number == num {
			return &laps[i]
		}
	}
	return nil
}

func computeSectorDeltas(refFile *ldparser.File, refLap *ldparser.Lap,
	cmpFile *ldparser.File, cmpLap *ldparser.Lap,
	distChRef, distChCmp *ldparser.Channel,
	nSectors int, alignment string) []secDelta {

	if nSectors < 1 {
		nSectors = 1
	}

	sectors := make([]secDelta, nSectors)

	if alignment == "lap_distance" {
		// Build distance→time maps for each lap
		refDist, refTime := buildDistanceTimeMap(distChRef, refLap.StartTime, refLap.EndTime)
		cmpDist, cmpTime := buildDistanceTimeMap(distChCmp, cmpLap.StartTime, cmpLap.EndTime)

		if len(refDist) < 2 || len(cmpDist) < 2 {
			// Fall back
			return computeSectorDeltasTimeFraction(refLap, cmpLap, nSectors)
		}

		// For each sector boundary (0%, 25%, 50%, 75%, 100% of distance)
		type boundary struct{ dist, tRef, tCmp float64 }
		bounds := make([]boundary, nSectors+1)
		for i := 0; i <= nSectors; i++ {
			d := float64(i) / float64(nSectors)
			tRef := interpolate(refDist, refTime, d)
			tCmp := interpolate(cmpDist, cmpTime, d)
			bounds[i] = boundary{d, tRef, tCmp}
		}

		for i := 0; i < nSectors; i++ {
			// delta at sector end - delta at sector start (time comparison spent vs reference)
			refDuration := bounds[i+1].tRef - bounds[i].tRef
			cmpDuration := bounds[i+1].tCmp - bounds[i].tCmp
			delta := round3(cmpDuration - refDuration)
			pctStart := float64(i) * 100 / float64(nSectors)
			pctEnd := float64(i+1) * 100 / float64(nSectors)
			sectors[i] = secDelta{
				Sector:   i + 1,
				PctRange: [2]float64{pctStart, pctEnd},
				TRef:     [2]float64{round3(bounds[i].tRef), round3(bounds[i+1].tRef)},
				Delta:    delta,
				Summary:  deltaSummary(delta),
			}
		}
		return sectors
	}

	return computeSectorDeltasTimeFraction(refLap, cmpLap, nSectors)
}

// computeSectorDeltasTimeFraction estimates sector deltas from speed channels
// when no lap distance channel is available. Each sector covers the same
// time fraction of the reference lap. The comparison delta is estimated by
// how much faster/slower the comparison lap covers the implied distance.
func computeSectorDeltasTimeFraction(refLap, cmpLap *ldparser.Lap, nSectors int) []secDelta {
	sectors := make([]secDelta, nSectors)
	refDur := refLap.LapTime
	cmpDur := cmpLap.LapTime
	if refDur <= 0 || cmpDur <= 0 {
		// No lap times — split total delta evenly
		delta := round3(cmpDur - refDur)
		for i := 0; i < nSectors; i++ {
			even := round3(delta / float64(nSectors))
			sectors[i] = secDelta{
				Sector:   i + 1,
				PctRange: [2]float64{float64(i) * 100 / float64(nSectors), float64(i+1) * 100 / float64(nSectors)},
				TRef:     [2]float64{round3(refLap.StartTime + float64(i)*refDur/float64(nSectors)), round3(refLap.StartTime + float64(i+1)*refDur/float64(nSectors))},
				Delta:    even,
				Summary:  deltaSummary(even) + " (estimated, no lap distance channel)",
			}
		}
		return sectors
	}

	for i := 0; i < nSectors; i++ {
		pctStart := float64(i) / float64(nSectors)
		pctEnd := float64(i+1) / float64(nSectors)
		tRefStart := refLap.StartTime + pctStart*refDur
		tRefEnd := refLap.StartTime + pctEnd*refDur
		tCmpStart := cmpLap.StartTime + pctStart*cmpDur
		tCmpEnd := cmpLap.StartTime + pctEnd*cmpDur
		// Sector delta: proportional contribution to total delta
		delta := round3((tCmpEnd - tCmpStart) - (tRefEnd - tRefStart))
		sectors[i] = secDelta{
			Sector:   i + 1,
			PctRange: [2]float64{round3(pctStart * 100), round3(pctEnd * 100)},
			TRef:     [2]float64{round3(tRefStart), round3(tRefEnd)},
			Delta:    delta,
			Summary:  deltaSummary(delta) + " (time_fraction estimate)",
		}
	}
	return sectors
}

func deltaSummary(delta float64) string {
	if math.Abs(delta) < 0.001 {
		return "no change"
	}
	if delta < 0 {
		return fmt.Sprintf("%.3fs faster", -delta)
	}
	return fmt.Sprintf("%.3fs slower", delta)
}

// buildDistanceTimeMap normalises a lap's distance channel to [0,1] and
// returns paired (distanceFraction, sessionTime) slices for interpolation.
func buildDistanceTimeMap(distCh *ldparser.Channel, lapStart, lapEnd float64) (distFrac, timeSec []float64) {
	if distCh == nil {
		return nil, nil
	}
	raw, tStart := sliceChannel(distCh, lapStart, lapEnd)
	if len(raw) < 2 {
		return nil, nil
	}
	maxDist := raw[len(raw)-1]
	if maxDist <= 0 {
		return nil, nil
	}
	freq := float64(distCh.Freq)
	distFrac = make([]float64, len(raw))
	timeSec = make([]float64, len(raw))
	for i, d := range raw {
		distFrac[i] = d / maxDist
		timeSec[i] = tStart + float64(i)/freq
	}
	return
}

// ---------------------------------------------------------------------------
// Channel delta computation
// ---------------------------------------------------------------------------

func computeChanDelta(refCh *ldparser.Channel, refLap *ldparser.Lap,
	cmpCh *ldparser.Channel, cmpLap *ldparser.Lap) chanDelta {

	refData, refTStart := sliceChannel(refCh, refLap.StartTime, refLap.EndTime)
	cmpData, cmpTStart := sliceChannel(cmpCh, cmpLap.StartTime, cmpLap.EndTime)

	if len(refData) == 0 || len(cmpData) == 0 {
		return chanDelta{Name: refCh.Name, Unit: refCh.Unit}
	}

	// Resample comparison to reference time axis (by time fraction)
	refFreq := float64(refCh.Freq)
	cmpFreq := float64(cmpCh.Freq)
	refDur := refLap.LapTime
	cmpDur := cmpLap.LapTime

	// Build time-fraction arrays for comparison channel
	cmpFracTime := make([]float64, len(cmpData))
	for i := range cmpData {
		absT := cmpTStart + float64(i)/cmpFreq
		cmpFracTime[i] = (absT - cmpLap.StartTime) / cmpDur
	}

	// Compute mean of each, and worst single point
	var sumRef, sumCmp float64
	worstDelta := 0.0
	var wp *worstPoint

	for i, rv := range refData {
		fracT := (refTStart + float64(i)/refFreq - refLap.StartTime) / refDur
		cv := interpolate(cmpFracTime, cmpData, fracT)
		sumRef += rv
		sumCmp += cv
		d := cv - rv
		if math.Abs(d) > math.Abs(worstDelta) {
			worstDelta = d
			t := round3(refTStart + float64(i)/refFreq)
			wp = &worstPoint{TRef: t, VRef: round3(rv), VCmp: round3(cv), Delta: round3(d)}
		}
	}
	n := float64(len(refData))
	meanRef := round3(sumRef / n)
	meanCmp := round3(sumCmp / n)

	// Only include worst point if delta is significant (> 5% of range)
	refRange := 0.0
	for _, v := range refData {
		if v > refRange {
			refRange = v
		}
	}
	if refRange > 0 && math.Abs(worstDelta)/refRange < 0.05 {
		wp = nil
	}

	return chanDelta{
		Name:      refCh.Name,
		Unit:      refCh.Unit,
		MeanRef:   meanRef,
		MeanCmp:   meanCmp,
		MeanDelta: round3(meanCmp - meanRef),
		WorstPt:   wp,
	}
}

// ---------------------------------------------------------------------------
// Output formatters — diff
// ---------------------------------------------------------------------------

// computeCumulativeDelta divides both laps into nPoints sectors and returns the
// running cumulative time gap at each sector boundary. Positive delta means the
// comparison lap is losing time relative to the reference at that point.
func computeCumulativeDelta(refFile *ldparser.File, refLap *ldparser.Lap,
	cmpFile *ldparser.File, cmpLap *ldparser.Lap,
	distChRef, distChCmp *ldparser.Channel,
	nPoints int, alignment string) *cumDelta {

	sectors := computeSectorDeltas(refFile, refLap, cmpFile, cmpLap,
		distChRef, distChCmp, nPoints, alignment)

	tRef := make([]float64, len(sectors))
	delta := make([]float64, len(sectors))
	cumSum := 0.0
	for i, s := range sectors {
		tRef[i] = round3(s.TRef[1])
		cumSum = round3(cumSum + s.Delta)
		delta[i] = cumSum
	}
	return &cumDelta{Points: len(sectors), TRef: tRef, Delta: delta}
}

func writeDiffText(resp *diffResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "File:\t%s\n", resp.File)
	if resp.File2 != "" {
		fmt.Fprintf(tw, "File2:\t%s\n", resp.File2)
	}
	fmt.Fprintf(tw, "Reference:\tLap %d  %s\n", resp.Reference.Lap, fmtLapTime(resp.Reference.LapTime))
	fmt.Fprintf(tw, "Comparison:\tLap %d  %s\n", resp.Comparison.Lap, fmtLapTime(resp.Comparison.LapTime))
	fmt.Fprintf(tw, "Total delta:\t%+.3fs  (%s)\n", resp.DeltaTotal, resp.DeltaNote)
	fmt.Fprintf(tw, "Alignment:\t%s\n\n", resp.Alignment)

	fmt.Fprintln(tw, "SECTOR\tPCT\tT_REF\tDELTA\tSUMMARY")
	for _, s := range resp.Sectors {
		fmt.Fprintf(tw, "%d\t%.0f–%.0f%%\t%.3f–%.3f\t%+.3f\t%s\n",
			s.Sector, s.PctRange[0], s.PctRange[1], s.TRef[0], s.TRef[1], s.Delta, s.Summary)
	}
	if len(resp.Channels) > 0 {
		fmt.Fprintln(tw, "\nCHANNEL\tUNIT\tMEAN_REF\tMEAN_CMP\tDELTA")
		for _, c := range resp.Channels {
			fmt.Fprintf(tw, "%s\t%s\t%.3f\t%.3f\t%+.3f\n",
				c.Name, c.Unit, c.MeanRef, c.MeanCmp, c.MeanDelta)
		}
	}
	if cd := resp.CumulativeDelta; cd != nil {
		fmt.Fprintf(tw, "\nCUMULATIVE DELTA (%d points)\n", cd.Points)
		fmt.Fprintln(tw, "T_REF\tCUM_DELTA")
		for i, t := range cd.TRef {
			fmt.Fprintf(tw, "%.3f\t%+.3f\n", t, cd.Delta[i])
		}
	}
	tw.Flush()
}
