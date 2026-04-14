// Example demonstrates parsing .ld race telemetry log files (the de facto standard for sim racing)
// and serving an interactive viewer in the browser with per-lap SVG traces for any channel,
// including lap comparison (primary vs reference lap overlay).
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/mail/go-ldparser"
)

var (
	parsedFile *ldparser.File
	laps       []ldparser.Lap
)

func main() {
	if len(os.Args) >= 2 {
		path := os.Args[1]
		var err error
		parsedFile, err = ldparser.ParseFile(path)
		if err != nil {
			log.Printf("parse error: %v", err)
		} else {
			fmt.Print(parsedFile.Header)
			fmt.Printf("\n%d channels, ", len(parsedFile.Channels))

			laps = parsedFile.DetectLaps()
			fmt.Printf("%d laps detected\n", len(laps))
			for _, lap := range laps {
				mins := int(lap.LapTime) / 60
				secs := lap.LapTime - float64(mins*60)
				fmt.Printf("  Lap %d: %d:%06.3f\n", lap.Number, mins, secs)
			}

			// Generate LDX
			ldx := ldparser.GenerateLDX(parsedFile, laps)
			ldxPath := strings.TrimSuffix(path, ".ld") + "_generated.ldx"
			if err := ldparser.WriteLDX(ldx, ldxPath); err != nil {
				log.Printf("warning: could not write ldx: %v", err)
			} else {
				fmt.Printf("Generated LDX: %s\n", ldxPath)
			}
		}
	} else {
		fmt.Println("Starting without file. Use the UI to upload an .ld file.")
	}

	http.HandleFunc("/", serveDashboard)
	http.HandleFunc("/api/channels", apiChannels)
	http.HandleFunc("/api/trace", apiTrace)
	http.HandleFunc("/api/upload", apiUpload)

	addr := ":8080"
	fmt.Printf("\nOpening http://localhost%s\n", addr)
	go openBrowser("http://localhost" + addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func apiUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, fmt.Sprintf("upload error: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Parse directly from the uploaded stream if possible, or save to temp
	// ldparser.ParseFile takes a path, so we save to a temporary file.
	tempFile, err := os.CreateTemp("", "upload-*.ld")
	if err != nil {
		http.Error(w, fmt.Sprintf("temp file error: %v", err), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err := tempFile.ReadFrom(file); err != nil {
		http.Error(w, fmt.Sprintf("save error: %v", err), http.StatusInternalServerError)
		return
	}

	newParsedFile, err := ldparser.ParseFile(tempFile.Name())
	if err != nil {
		http.Error(w, fmt.Sprintf("parse error: %v", err), http.StatusBadRequest)
		return
	}

	// Update global state
	parsedFile = newParsedFile
	laps = parsedFile.DetectLaps()

	log.Printf("Uploaded and parsed: %s (%d laps)", header.Filename, len(laps))
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func apiChannels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	type chanInfo struct {
		Name      string `json:"name"`
		ShortName string `json:"shortName"`
		Unit      string `json:"unit"`
		Freq      int    `json:"freq"`
		Samples   int    `json:"samples"`
	}
	var out []chanInfo
	if parsedFile != nil {
		for _, ch := range parsedFile.Channels {
			out = append(out, chanInfo{ch.Name, ch.ShortName, ch.Unit, int(ch.Freq), int(ch.DataLen)})
		}
	}
	json.NewEncoder(w).Encode(out)
}

// apiTrace renders an SVG with one or two laps overlaid per channel.
// Query params: ch (repeatable), lap (primary), ref (optional reference lap).
func apiTrace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	if parsedFile == nil {
		svgError(w, "No file loaded")
		return
	}
	chNames := r.URL.Query()["ch"]
	lapIdx, _ := strconv.Atoi(r.URL.Query().Get("lap"))
	refStr := r.URL.Query().Get("ref")
	hasRef := refStr != ""
	refIdx, _ := strconv.Atoi(refStr)

	if lapIdx < 0 || lapIdx >= len(laps) {
		svgError(w, "Invalid lap")
		return
	}
	lap := laps[lapIdx]

	var refLap ldparser.Lap
	if hasRef {
		if refIdx < 0 || refIdx >= len(laps) {
			hasRef = false
		} else {
			refLap = laps[refIdx]
		}
	}

	var channels []*ldparser.Channel
	for _, name := range chNames {
		ch := parsedFile.ChannelByName(name)
		if ch != nil && len(ch.Data) > 0 && ch.Freq > 0 {
			channels = append(channels, ch)
		}
	}

	if len(channels) == 0 {
		svgError(w, "Select channels to plot")
		return
	}

	colors := []string{"#44ff44", "#ff4444", "#4488ff", "#ffaa00", "#ff44ff", "#44ffff", "#ff8844", "#88ff44"}

	svgW, svgH := 1400, 320
	padL, padR, padT, padB := 60, 20, 44, 35
	plotW := svgW - padL - padR
	plotH := svgH - padT - padB

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="100%%" height="%d" style="background:#111">`, svgW, svgH, svgH))

	if len(laps) == 0 {
		sb.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#666" font-size="14" font-family="monospace">No laps detected</text>`, padL, padT+20))
		sb.WriteString(`</svg>`)
		fmt.Fprint(w, sb.String())
		return
	}

	// Use the longer lap for the time axis so both fit
	lapDur := lap.EndTime - lap.StartTime
	if hasRef {
		refDur := refLap.EndTime - refLap.StartTime
		if refDur > lapDur {
			lapDur = refDur
		}
	}
	if lapDur <= 0 {
		lapDur = 1
	}

	// Grid
	for i := 0; i <= 4; i++ {
		y := padT + i*plotH/4
		sb.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#222" stroke-width="0.5"/>`, padL, y, padL+plotW, y))
	}

	// Time axis
	interval := timeAxisInterval(lapDur)
	for s := 0.0; s <= lapDur; s += interval {
		x := padL + int(s/lapDur*float64(plotW))
		sb.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#222" stroke-width="0.5"/>`, x, padT, x, padT+plotH))
		sb.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#555" font-size="10" font-family="monospace" text-anchor="middle">%.0fs</text>`, x, padT+plotH+14, s))
	}

	// For comparison: compute shared min/max across both laps per channel
	// so the Y-axis scale is identical.
	for ci, ch := range channels {
		color := colors[ci%len(colors)]
		minVal, maxVal := chanMinMax(ch, lap)

		if hasRef {
			rMin, rMax := chanMinMax(ch, refLap)
			if rMin < minVal {
				minVal = rMin
			}
			if rMax > maxVal {
				maxVal = rMax
			}
		}

		// Reference lap: dashed, dimmer, drawn first (behind)
		if hasRef {
			path := buildTraceNorm(ch, refLap, lapDur, minVal, maxVal, padL, padT, plotW, plotH)
			if path != "" {
				sb.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="1" opacity="0.35" stroke-dasharray="6,4"/>`, path, color))
			}
		}

		// Primary lap: solid, bright
		path := buildTraceNorm(ch, lap, lapDur, minVal, maxVal, padL, padT, plotW, plotH)
		if path != "" {
			sb.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="1.3" opacity="0.9"/>`, path, color))
			sb.WriteString(fmt.Sprintf(`<path d="%sL%d,%dL%d,%dZ" fill="%s" opacity="0.05"/>`,
				path, padL+plotW, padT+plotH, padL, padT+plotH, color))
		}
	}

	// Legend
	lx := padL + 8
	for ci, ch := range channels {
		color := colors[ci%len(colors)]
		ly := padT + 14 + ci*16
		sb.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="10" height="3" fill="%s" rx="1"/>`, lx, ly-2, color))
		label := ch.Name
		if ch.Unit != "" {
			label += " [" + ch.Unit + "]"
		}
		sb.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="%s" font-size="10" font-family="monospace" opacity="0.9">%s</text>`, lx+14, ly+2, color, label))
	}

	// Comparison legend
	if hasRef {
		ly := padT + 14 + len(channels)*16
		sb.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#888" stroke-width="1" stroke-dasharray="6,4"/>`, lx, ly, lx+10, ly))
		sb.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="#888" font-size="10" font-family="monospace">Ref: Lap %d</text>`, lx+14, ly+3, laps[refIdx].Number))
	}

	sb.WriteString(`</svg>`)
	fmt.Fprint(w, sb.String())
}

func svgError(w http.ResponseWriter, msg string) {
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="100" style="background:#111"><text x="16" y="40" fill="#666" font-size="14" font-family="monospace">%s</text></svg>`, msg)
}

func timeAxisInterval(dur float64) float64 {
	switch {
	case dur > 180:
		return 30
	case dur > 120:
		return 20
	case dur > 60:
		return 15
	case dur > 30:
		return 10
	default:
		return 5
	}
}

func chanMinMax(ch *ldparser.Channel, lap ldparser.Lap) (float64, float64) {
	start := int(lap.StartTime * float64(ch.Freq))
	end := int(lap.EndTime * float64(ch.Freq))
	if start < 0 {
		start = 0
	}
	if end > len(ch.Data) {
		end = len(ch.Data)
	}
	if start >= end {
		return 0, 1
	}
	mn, mx := ch.Data[start], ch.Data[start]
	for i := start; i < end; i++ {
		if ch.Data[i] < mn {
			mn = ch.Data[i]
		}
		if ch.Data[i] > mx {
			mx = ch.Data[i]
		}
	}
	return mn, mx
}

// buildTraceNorm builds an SVG path for a channel within a lap, normalized
// against shared min/max and a shared time axis duration.
func buildTraceNorm(ch *ldparser.Channel, lap ldparser.Lap, axisDur, minVal, maxVal float64, padL, padT, plotW, plotH int) string {
	lapDur := lap.EndTime - lap.StartTime
	if lapDur <= 0 || axisDur <= 0 {
		return ""
	}

	startSample := int(lap.StartTime * float64(ch.Freq))
	endSample := int(lap.EndTime * float64(ch.Freq))
	if startSample < 0 {
		startSample = 0
	}
	if endSample > len(ch.Data) {
		endSample = len(ch.Data)
	}
	if startSample >= endSample {
		return ""
	}

	valRange := maxVal - minVal
	if valRange == 0 {
		valRange = 1
	}

	totalSamples := endSample - startSample
	step := totalSamples / plotW
	if step < 1 {
		step = 1
	}

	var sb strings.Builder
	first := true
	for i := startSample; i < endSample; i += step {
		t := float64(i-startSample) / float64(ch.Freq)
		x := padL + int(t/axisDur*float64(plotW))
		norm := (ch.Data[i] - minVal) / valRange
		y := padT + plotH - int(math.Round(norm*float64(plotH)))
		if y < padT {
			y = padT
		}
		if y > padT+plotH {
			y = padT + plotH
		}
		if first {
			sb.WriteString(fmt.Sprintf("M%d,%d", x, y))
			first = false
		} else {
			sb.WriteString(fmt.Sprintf("L%d,%d", x, y))
		}
	}
	return sb.String()
}

func serveDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var sb strings.Builder

	type lapJSON struct {
		Number    int     `json:"number"`
		LapTime   float64 `json:"lapTime"`
		StartTime float64 `json:"startTime"`
		EndTime   float64 `json:"endTime"`
	}
	lapsData := []lapJSON{}
	for _, l := range laps {
		lapsData = append(lapsData, lapJSON{l.Number, l.LapTime, l.StartTime, l.EndTime})
	}
	lapsJSON, _ := json.Marshal(lapsData)

	sb.WriteString(`<!DOCTYPE html><html><head><title>Telemetry Viewer</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:'Segoe UI',system-ui,-apple-system,sans-serif;background:#0a0a0a;color:#ddd;display:flex;height:100vh;overflow:hidden}

/* Sidebar */
.sidebar{width:320px;min-width:320px;background:#111;border-right:1px solid #222;display:flex;flex-direction:column;overflow:hidden}
.sidebar-header{padding:16px;border-bottom:1px solid #222}
.sidebar-header h1{font-size:1.1em;color:#e44;margin-bottom:4px}
.sidebar-header .meta{font-size:.78em;color:#777;line-height:1.5}
.sidebar-header .meta b{color:#aaa}
.section-title{padding:10px 16px 6px;font-size:.72em;text-transform:uppercase;color:#555;letter-spacing:.5px;border-top:1px solid #1a1a1a}

/* Lap list */
.lap-list{overflow-y:auto;flex-shrink:0;max-height:240px;border-bottom:1px solid #222}
.lap-item{padding:5px 16px;cursor:pointer;font-size:.82em;display:flex;justify-content:space-between;align-items:center;font-variant-numeric:tabular-nums;gap:4px}
.lap-item:hover{background:#1a1a1a}
.lap-item .num{min-width:44px}
.lap-item .time{flex:1;text-align:right}
.lap-item .badges{display:flex;gap:3px;min-width:40px;justify-content:flex-end}
.badge{font-size:.65em;padding:1px 5px;border-radius:3px;text-transform:uppercase;font-weight:600}
.badge-pri{background:#264d26;color:#4f4}
.badge-ref{background:#3a3a1a;color:#cc8}
.lap-item.has-pri{background:#0f1f0f}
.lap-item.has-ref{background:#1a1a0f}
.lap-item.has-pri.has-ref{background:#162016}
.lap-item.fastest .time{color:#4f4;font-weight:700}
.lap-hint{padding:6px 16px;font-size:.7em;color:#555;line-height:1.4}

/* Channel list */
.chan-filter{padding:8px 16px}
.chan-filter input{width:100%;padding:6px 10px;background:#0a0a0a;border:1px solid #333;border-radius:4px;color:#ddd;font-size:.85em;outline:none}
.chan-filter input:focus{border-color:#555}
.chan-list{overflow-y:auto;flex:1}
.chan-item{padding:5px 16px;cursor:pointer;font-size:.78em;display:flex;align-items:center;gap:8px}
.chan-item:hover{background:#1a1a1a}
.chan-item.active{background:#141422}
.chan-dot{width:10px;height:10px;border-radius:50%;border:2px solid #333;flex-shrink:0}
.chan-dot.on{border-color:currentColor;background:currentColor}
.chan-info{flex:1;display:flex;justify-content:space-between}
.chan-info .unit{color:#555;font-size:.9em}

/* Main area */
.main{flex:1;display:flex;flex-direction:column;overflow:hidden}
.toolbar{padding:8px 16px;background:#111;border-bottom:1px solid #222;display:flex;gap:16px;align-items:center;font-size:.82em;flex-wrap:wrap}
.toolbar .label{color:#555}
.toolbar .pri-label{color:#4f4}
.toolbar .ref-label{color:#cc8}
.toolbar .vs{color:#555;font-size:.9em}
.charts{flex:1;overflow-y:auto;padding:16px}
.chart-container{margin-bottom:16px;background:#0d0d0d;border:1px solid #1a1a1a;border-radius:6px;overflow:hidden}
.chart-container h3{padding:8px 14px;font-size:.78em;color:#555;text-transform:uppercase;letter-spacing:.3px;border-bottom:1px solid #1a1a1a;display:flex;justify-content:space-between}
.chart-container h3 .delta{color:#cc8}
.chart-container img{display:block;width:100%}
.empty-state{padding:60px 40px;color:#444;text-align:center;font-size:.95em;line-height:1.6}
.upload-container{margin:10px 0}
.upload-btn{display:inline-block;padding:6px 12px;background:#264d26;color:#4f4;border-radius:4px;font-size:.78em;cursor:pointer;font-weight:600;transition:background .2s}
.upload-btn:hover{background:#2a5a2a}
</style></head><body>
<div class="sidebar">
 <div class="sidebar-header">
  <h1>Telemetry Viewer</h1>
  <div class="upload-container">
   <label for="ld-upload" class="upload-btn">Upload .ld file</label>
   <input type="file" id="ld-upload" accept=".ld" style="display:none" onchange="uploadFile(this)">
  </div>
  <div class="meta">`)

	if parsedFile != nil {
		sb.WriteString(fmt.Sprintf(`<b>%s</b><br>`, parsedFile.Header.DateTime.Format("2006-01-02 15:04:05")))
		if parsedFile.Header.Driver != "" {
			sb.WriteString(fmt.Sprintf(`Driver: <b>%s</b><br>`, parsedFile.Header.Driver))
		}
		if parsedFile.Header.VehicleID != "" {
			sb.WriteString(fmt.Sprintf(`Vehicle: <b>%s</b><br>`, parsedFile.Header.VehicleID))
		}
		sb.WriteString(fmt.Sprintf(`Venue: <b>%s</b><br>`, parsedFile.Header.Venue))
		if parsedFile.Header.Event != nil && parsedFile.Header.Event.Name != "" {
			sb.WriteString(fmt.Sprintf(`Event: <b>%s</b><br>`, parsedFile.Header.Event.Name))
		}
		if parsedFile.Header.Event != nil && parsedFile.Header.Event.Venue != nil && parsedFile.Header.Event.Venue.Vehicle != nil {
			sb.WriteString(fmt.Sprintf(`Car: <b>%s</b><br>`, parsedFile.Header.Event.Venue.Vehicle.ID))
		}
		sb.WriteString(fmt.Sprintf(`Channels: <b>%d</b>`, len(parsedFile.Channels)))
	} else {
		sb.WriteString(`<b>No file loaded</b><br>Upload an .ld file to begin analysis`)
	}

	sb.WriteString(`</div></div>
 <div class="section-title">Laps — click = primary, right-click = reference</div>
 <div class="lap-list" id="lap-list"></div>
 <div class="section-title">Channels</div>
 <div class="chan-filter"><input type="text" id="chan-search" placeholder="Filter channels..."></div>
 <div class="chan-list" id="chan-list"></div>
</div>
<div class="main">
 <div class="toolbar" id="toolbar"></div>
 <div class="charts" id="charts"></div>
</div>

<script>
const LAPS = `)
	sb.Write(lapsJSON)
	sb.WriteString(`;
const COLORS = ['#44ff44','#ff4444','#4488ff','#ffaa00','#ff44ff','#44ffff','#ff8844','#88ff44','#ffff44','#ff88ff'];
let priLap = 0;
let refLap = -1; // -1 = no reference
let selectedChannels = [];
let allChannels = [];

function uploadFile(input) {
 if(!input.files || !input.files[0]) return;
 const formData = new FormData();
 formData.append('file', input.files[0]);
 fetch('/api/upload', {
  method: 'POST',
  body: formData
 }).then(r => {
  if(r.ok) window.location.reload();
  else alert('Upload failed');
 });
}

function fmtTime(s) {
 if(s<=0) return '-';
 const m = Math.floor(s/60);
 const sec = s - m*60;
 return m + ':' + sec.toFixed(3).padStart(6,'0');
}
function fmtDelta(a,b) {
 const d = a - b;
 const sign = d >= 0 ? '+' : '';
 return sign + d.toFixed(3) + 's';
}

let fastestIdx = 0;
if (LAPS.length > 0) {
 LAPS.forEach((l,i)=>{if(l.lapTime>0&&(LAPS[fastestIdx].lapTime<=0||l.lapTime<LAPS[fastestIdx].lapTime))fastestIdx=i});
}

function renderLaps() {
 const el = document.getElementById('lap-list');
 if (LAPS.length === 0) {
  el.innerHTML = '<div class="lap-hint">No data loaded. Use the upload button above.</div>';
  return;
 }
 el.innerHTML = LAPS.map((l,i)=>{
  let cls = 'lap-item';
  if(i===priLap) cls += ' has-pri';
  if(i===refLap) cls += ' has-ref';
  if(i===fastestIdx) cls += ' fastest';
  let badges = '';
  if(i===priLap) badges += '<span class="badge badge-pri">PRI</span>';
  if(i===refLap) badges += '<span class="badge badge-ref">REF</span>';
  return '<div class="'+cls+'" onclick="setPri('+i+')" oncontextmenu="setRef(event,'+i+')">'+
   '<span class="num">Lap '+l.number+'</span>'+
   '<span class="time">'+fmtTime(l.lapTime)+'</span>'+
   '<span class="badges">'+badges+'</span></div>';
 }).join('');
}

function setPri(i) {
 priLap = i;
 if(refLap === i) refLap = -1;
 renderLaps();
 renderToolbar();
 renderCharts();
}
function setRef(e, i) {
 e.preventDefault();
 if(refLap === i) { refLap = -1; }
 else { refLap = i; if(priLap === i && LAPS.length > 1) { priLap = i === 0 ? 1 : 0; } }
 renderLaps();
 renderToolbar();
 renderCharts();
}

function renderToolbar() {
 const el = document.getElementById('toolbar');
 if (LAPS.length === 0) {
  el.innerHTML = '<span class="label">Telemetry Viewer</span>';
  return;
 }
 let html = '<span class="pri-label">Primary: Lap '+LAPS[priLap].number+' ('+fmtTime(LAPS[priLap].lapTime)+')</span>';
 if(refLap >= 0) {
  html += '<span class="vs">vs</span>';
  html += '<span class="ref-label">Ref: Lap '+LAPS[refLap].number+' ('+fmtTime(LAPS[refLap].lapTime)+') ';
  html += fmtDelta(LAPS[priLap].lapTime, LAPS[refLap].lapTime);
  html += '</span>';
 } else {
  html += '<span class="label">Right-click a lap to set as reference for comparison</span>';
 }
 el.innerHTML = html;
}

function renderChannels(filter) {
 const el = document.getElementById('chan-list');
 const f = (filter||'').toLowerCase();
 el.innerHTML = allChannels.filter(c=>!f||c.name.toLowerCase().includes(f)||c.unit.toLowerCase().includes(f)).map(c=>{
  const active = selectedChannels.includes(c.name);
  const ci = selectedChannels.indexOf(c.name);
  const color = ci>=0?COLORS[ci%COLORS.length]:'#333';
  return '<div class="chan-item'+(active?' active':'')+'" onclick="toggleChannel(\''+c.name.replace(/'/g,"\\'")+'\')">'+
   '<div class="chan-dot'+(active?' on':'')+'" style="color:'+color+'"></div>'+
   '<div class="chan-info"><span>'+c.name+'</span><span class="unit">'+c.unit+' '+c.freq+'Hz</span></div></div>';
 }).join('');
}

function toggleChannel(name) {
 const idx = selectedChannels.indexOf(name);
 if(idx>=0) selectedChannels.splice(idx,1);
 else if(selectedChannels.length<10) selectedChannels.push(name);
 renderChannels(document.getElementById('chan-search').value);
 renderCharts();
}

function renderCharts() {
 const el = document.getElementById('charts');
 if (LAPS.length === 0) {
  el.innerHTML = '<div class="empty-state">Welcome. Please upload a telemetry log file (.ld) to view traces.</div>';
  return;
 }
 if(!selectedChannels.length) {
  el.innerHTML = '<div class="empty-state">Select channels from the sidebar to see traces.<br>Click a lap to set it as primary. Right-click for reference (comparison).</div>';
  return;
 }

 const refParam = refLap >= 0 ? '&ref='+refLap : '';
 const chParams = selectedChannels.map(c=>'ch='+encodeURIComponent(c)).join('&');

 // Combined overlay chart
 let html = '';
 const lap = LAPS[priLap];
 let title = 'Lap '+lap.number+' — '+fmtTime(lap.lapTime);
 let delta = '';
 if(refLap >= 0) {
  title = 'Lap '+lap.number+' vs Lap '+LAPS[refLap].number;
  delta = '<span class="delta">'+fmtDelta(lap.lapTime, LAPS[refLap].lapTime)+'</span>';
 }
 html += '<div class="chart-container"><h3><span>'+title+'</span>'+delta+'</h3>'+
  '<img src="/api/trace?lap='+priLap+refParam+'&'+chParams+'" /></div>';

 // Individual channel charts
 selectedChannels.forEach(c=>{
  html += '<div class="chart-container"><h3><span>'+c+'</span></h3>'+
   '<img src="/api/trace?lap='+priLap+refParam+'&ch='+encodeURIComponent(c)+'" /></div>';
 });

 el.innerHTML = html;
}

fetch('/api/channels').then(r=>r.json()).then(data=>{
 allChannels = data;
 renderChannels();
});

document.getElementById('chan-search').addEventListener('input',e=>renderChannels(e.target.value));

renderLaps();
renderToolbar();
renderCharts();
</script>
</body></html>`)

	fmt.Fprint(w, sb.String())
}
