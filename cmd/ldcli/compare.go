package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	ldparser "github.com/mail/go-ldparser"
)

// ---------------------------------------------------------------------------
// Response types — compare
// ---------------------------------------------------------------------------

type compareLapEntry struct {
	Path    string  `json:"path"`
	Lap     *int    `json:"lap"`
	LapTime float64 `json:"lap_time,omitempty"`
	Mean    float64 `json:"mean"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Std     float64 `json:"std"`
	P50     float64 `json:"p50"`
	N       int     `json:"n"`
}

type compareChannelDelta struct {
	Mean float64 `json:"mean"`
	Max  float64 `json:"max"`
	Min  float64 `json:"min"`
	Std  float64 `json:"std"`
	P50  float64 `json:"p50"`
}

type compareChannelResult struct {
	Name  string               `json:"name"`
	Unit  string               `json:"unit"`
	Laps  []compareLapEntry    `json:"laps"`
	Delta *compareChannelDelta `json:"delta,omitempty"`
}

type compareResponse struct {
	Channels []compareChannelResult `json:"channels"`
	Warnings []string               `json:"warnings,omitempty"`
}

// ---------------------------------------------------------------------------
// compare command
// ---------------------------------------------------------------------------

func runCompare(args []string) {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	lapFlag := fs.String("lap", "best", "lap number or 'best' (fastest lap)")
	var chFlags multiFlag
	fs.Var(&chFlags, "ch", "channel name (repeatable, default: speed+throttle+brake+gear)")
	format := fs.String("format", "json", "json or text")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)

	if len(filePaths) < 1 || len(filePaths) > 2 {
		fatalJSON("compare: requires 1 or 2 file paths")
	}

	files := loadFiles(filePaths)
	var warnings []string

	type fileWindow struct {
		fr  fileResult
		win lapWindow
	}
	var fw []fileWindow
	for _, fr := range files {
		win, warn := resolveLapWindow(fr, *lapFlag)
		if warn != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s", fr.Path, warn))
			continue
		}
		fw = append(fw, fileWindow{fr, win})
	}
	if len(fw) == 0 {
		fatalJSON("compare: no usable laps found")
	}

	// Collect channel names in encounter order across all files
	seen := map[string]string{} // name → unit
	var chOrder []string
	for _, f := range fw {
		var chs []*ldparser.Channel
		if len(chFlags) > 0 {
			chs = matchChannels(f.fr.File, []string(chFlags))
		}
		if len(chs) == 0 {
			for _, c := range []*ldparser.Channel{
				findChannelFuzzy(f.fr.File, "Ground Speed", "Speed over Ground", "GPS Speed", "Vehicle Speed", "Speed"),
				findChannelFuzzy(f.fr.File, "Throttle Pos", "Throttle Position", "Throttle"),
				findChannelFuzzy(f.fr.File, "Brake Pres Front", "Brake Pressure Front", "Brake Pres", "Brake Pressure", "Brake Pos"),
				findChannelFuzzy(f.fr.File, "Gear", "Gear Position", "Current Gear"),
			} {
				if c != nil {
					chs = append(chs, c)
				}
			}
		}
		for _, ch := range chs {
			if _, ok := seen[ch.Name]; !ok {
				seen[ch.Name] = ch.Unit
				chOrder = append(chOrder, ch.Name)
			}
		}
	}

	results := make([]compareChannelResult, 0, len(chOrder))
	for _, name := range chOrder {
		unit := seen[name]
		var entries []compareLapEntry
		for _, f := range fw {
			ch := f.fr.File.ChannelByName(name)
			if ch == nil {
				ch = findChannelFuzzy(f.fr.File, name)
			}
			if ch != nil {
				unit = ch.Unit
			}
			if ch == nil {
				warnings = append(warnings, fmt.Sprintf("%s: channel %q not found", f.fr.Path, name))
				entries = append(entries, compareLapEntry{Path: f.fr.Path, Lap: f.win.num})
				continue
			}
			data, _ := sliceChannel(ch, f.win.from, f.win.to)
			mn, mx, mean, std, _, p50, _, n := computeStats(data)
			entries = append(entries, compareLapEntry{
				Path:    f.fr.Path,
				Lap:     f.win.num,
				LapTime: round3(f.win.lapTime),
				Mean:    round3(mean),
				Min:     round3(mn),
				Max:     round3(mx),
				Std:     round3(std),
				P50:     round3(p50),
				N:       n,
			})
		}

		cr := compareChannelResult{Name: name, Unit: unit, Laps: entries}
		if len(entries) == 2 && entries[0].N > 0 && entries[1].N > 0 {
			cr.Delta = &compareChannelDelta{
				Mean: round3(entries[1].Mean - entries[0].Mean),
				Max:  round3(entries[1].Max - entries[0].Max),
				Min:  round3(entries[1].Min - entries[0].Min),
				Std:  round3(entries[1].Std - entries[0].Std),
				P50:  round3(entries[1].P50 - entries[0].P50),
			}
		}
		results = append(results, cr)
	}

	resp := compareResponse{Channels: results, Warnings: warnings}
	switch *format {
	case "text":
		writeCompareText(&resp)
	default:
		writeJSON(resp)
	}
}

// resolveLapWindow picks a single lap for compare. lapArg is "best" or a lap number.
func resolveLapWindow(fr fileResult, lapArg string) (lapWindow, string) {
	if lapArg == "best" {
		var best *ldparser.Lap
		for i := range fr.Laps {
			l := &fr.Laps[i]
			if l.LapTime <= 0 {
				continue
			}
			if best == nil || l.LapTime < best.LapTime {
				best = l
			}
		}
		if best == nil {
			return lapWindow{}, "no laps with positive lap time found"
		}
		n := best.Number
		return lapWindow{num: &n, from: best.StartTime, to: best.EndTime, lapTime: best.LapTime}, ""
	}
	wins, errMsg := buildWindows(lapArg, fr.Laps, sessionEnd(fr))
	if errMsg != "" || len(wins) == 0 {
		if errMsg == "" {
			errMsg = fmt.Sprintf("lap %q not found", lapArg)
		}
		return lapWindow{}, errMsg
	}
	return wins[0], ""
}

func writeCompareText(resp *compareResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, cr := range resp.Channels {
		unit := cr.Unit
		if unit != "" {
			unit = " (" + unit + ")"
		}
		fmt.Fprintf(tw, "=== %s%s ===\n", cr.Name, unit)
		fmt.Fprintln(tw, "file\tlap\tlap_time\tmean\tmin\tmax\tstd\tp50")
		for _, e := range cr.Laps {
			ls := "null"
			if e.Lap != nil {
				ls = fmt.Sprintf("%d", *e.Lap)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\n",
				e.Path, ls, fmtLapTime(e.LapTime),
				e.Mean, e.Min, e.Max, e.Std, e.P50)
		}
		if cr.Delta != nil {
			fmt.Fprintf(tw, "DELTA\t\t\t%+.3f\t%+.3f\t%+.3f\t%+.3f\t%+.3f\n",
				cr.Delta.Mean, cr.Delta.Min, cr.Delta.Max, cr.Delta.Std, cr.Delta.P50)
		}
		fmt.Fprintln(tw)
	}
	if len(resp.Warnings) > 0 {
		fmt.Fprintln(tw, "WARNINGS:")
		for _, w := range resp.Warnings {
			fmt.Fprintf(tw, "  %s\n", w)
		}
	}
	tw.Flush()
}
