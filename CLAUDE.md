# go-ldparser — Race Telemetry (.ld) Parser for Go

## Project Overview

A pure Go library for reading `.ld` race telemetry files and their companion index files (`.ldx`). No CGo, no external dependencies — just the standard library. Includes a command-line tool (`ldcli`) designed for LLM-assisted analysis of racing sessions.

The `.ld` binary format stores high-frequency sensor data from hardware data loggers used across motorsport and sim racing (Le Mans Ultimate, ACC, iRacing, GT7, etc.). Also reads/writes `.ldx` index files. Zero external dependencies. Binary format based on the reference implementation at [github.com/gotzl/ldparser](https://github.com/gotzl/ldparser).

**Important**: Channel names vary widely between games and hardware. The library is game-agnostic — it parses raw channel data and leaves interpretation to the consumer.

**Disclaimer**: This project is not affiliated with, endorsed by, or created by MoTeC i2. It is an independent, open-source implementation created for scientific and educational purposes only.

**Development Constraint**: Do not include references to "MoTeC", "i2", or "motec" in code comments, documentation, or examples. This maintains clear separation from the proprietary i2 software.

## Build & Run

```bash
make build          # build all tools (ldcli + ldviewer)
make test           # run test suite
make install        # install as global binaries
make clean          # clean build artifacts
```

## Architecture

- `file.go`, `parse.go`, `meta.go`, `ldx.go` — Core library. Reads the full .ld binary in one pass into `File{Header, []Channel}`. Each `Channel.Data` is `[]float64` with converted engineering values.
- `cmd/ldcli/` — Command-line tool with subcommands: `info`, `laps`, `inspect`, `summarize`, `events`, `diff`, `data`, `report`, `analyze` (braking/throttle/tyre/median/deviation), `compare`, `map`, `guide`.
- `cmd/ldviewer/` — HTTP server serving an interactive telemetry viewer. Server-side SVG rendering, client-side JS for lap selection, channel picker, and lap comparison.
- `internal/ldx/` — Internal XML parsing for `.ldx` index files.

## Key Design Decisions

- **Game-agnostic**: Channel names vary wildly between hardware loggers, ACC, LMU, iRacing. The library never assumes specific channel names exist. It exposes raw data; interpretation is the consumer's job.
- **All data loaded upfront**: Unlike lazy-load patterns, Go parses all channel data on `ParseFile()`. This is fine — even a 45MB race file with 245 channels parses in <1s.
- **LLM-friendly CLI**: All output is JSON by default. Token counts are documented. Commands are designed for efficient escalation workflows.
- **SVG charts are server-rendered**: The viewer generates SVG on the server as string building. Charts are fetched as `<img>` tags for the static case.

## Binary Format

All little-endian.

### .ld File Structure

Sequential binary blob:

```
┌─────────────────────────────┐  offset 0
│  Header (1762 bytes)        │
│  - LD marker: 0x40          │
│  - meta_ptr → first channel │
│  - data_ptr → channel data  │
│  - event_ptr → event block  │
│  - date, time (ASCII)       │
│  - driver, vehicle, venue   │
│  - short_comment            │
├─────────────────────────────┤  offset 1762
│  Event (1154 bytes)         │
│  - name, session, comment   │
│  - venue_ptr → venue block  │
├─────────────────────────────┤
│  Venue (1100 bytes)         │
│  - name                     │
│  - vehicle_ptr → vehicle    │
├─────────────────────────────┤
│  Vehicle (260 bytes)        │
│  - id (0:64)                │
│  - long_name (64:192) ← LMU │
│  - weight, type, comment    │
├─────────────────────────────┤
│  Channel Meta (linked list) │
│  Each entry: 124 bytes      │
│  - prev_ptr, next_ptr       │
│  - data_ptr, data_len       │
│  - dtype_a, dtype, freq     │
│  - shift, mul, scale, dec   │
│  - name, short_name, unit   │
├─────────────────────────────┤
│  Channel Data               │
│  Raw samples packed by type │
│  int16/int32/float16/32/64  │
└─────────────────────────────┘
```

### Header Layout (1762 bytes)

| Offset | Size  | Field          | Notes                              |
|--------|-------|----------------|------------------------------------|
| 0      | 4     | LD marker      | Always `0x40`                      |
| 8      | 4     | meta_ptr       | Pointer to first channel meta      |
| 12     | 4     | data_ptr       | Pointer to first channel data      |
| 36     | 4     | event_ptr      | Pointer to event block             |
| 70     | 4     | device_serial  | Logger serial number               |
| 74     | 8     | device_type    | e.g. "ADL"                         |
| 82     | 2     | device_version |                                    |
| 86     | 4     | num_channels   |                                    |
| 94     | 16    | date           | ASCII "DD/MM/YYYY"                 |
| 126    | 16    | time           | ASCII "HH:MM:SS" or "HH:MM"       |
| 158    | 64    | driver         | ASCII, null-padded                 |
| 222    | 64    | vehicle_id     |                                    |
| 350    | 64    | venue          |                                    |
| 1572   | 64    | short_comment  |                                    |

### Channel Meta Layout (124 bytes)

| Offset | Size | Field          | Type   |
|--------|------|----------------|--------|
| 0      | 4    | prev_meta_ptr  | uint32 |
| 4      | 4    | next_meta_ptr  | uint32 |
| 8      | 4    | data_ptr       | uint32 |
| 12     | 4    | data_len       | uint32 |
| 16     | 2    | counter        | uint16 |
| 18     | 2    | dtype_a        | uint16 |
| 20     | 2    | dtype          | uint16 |
| 22     | 2    | freq           | uint16 |
| 24     | 2    | shift          | int16  |
| 26     | 2    | mul            | int16  |
| 28     | 2    | scale          | int16  |
| 30     | 2    | dec            | int16  |
| 32     | 32   | name           | ASCII  |
| 64     | 8    | short_name     | ASCII  |
| 72     | 12   | unit           | ASCII  |
| 84     | 40   | (padding)      |        |

### Data Type Resolution

| dtype_a         | dtype | Go type  | Size |
|-----------------|-------|----------|------|
| 0x07            | 2     | float16  | 2B   |
| 0x07            | 4     | float32  | 4B   |
| 0x00/0x03/0x05  | 2     | int16    | 2B   |
| 0x00/0x03/0x05  | 4     | int32    | 4B   |
| 0x08            | 0x08  | float64  | 8B   |

### Sample Data Conversion

```
converted = (raw / scale * 10^(-dec) + shift) * mul
```

### .ldx File Format

XML with both `<String>` and `<Numeric>` entries. Numeric entries are commonly used for car setup data (Le Mans Ultimate, rFactor2):

```xml
<LDXFile Locale="German_Germany.1252" DefaultLocale="C" Version="1.6">
 <Layers>
  <Details>
   <Numeric Id="_Setup_BrakePressure" Value="72" Unit="%" DPS="3"/>
   <String Id="Total Laps" Value="3"/>
   <String Id="Fastest Time" Value="1:54.856"/>
   <String Id="Fastest Lap" Value="1"/>
  </Details>
 </Layers>
</LDXFile>
```

## Lap Detection

Uses "Lap Number" + "Lap Time" channels (present in both hardware loggers and sim exports). Falls back to "Lap Distance" resets. The "Beacon" channel encodes complex marker data — not used for lap detection.

Channel names vary by game/hardware:
- ADL logger: "Lap Number", "Lap Time", "Lap Distance"
- LMU/rFactor2: "Lap Number" (at 50Hz), "Lap Time"
- ACC: varies

## Project Structure

```
go-ldparser/
├── go.mod                  # github.com/mail/go-ldparser
├── file.go                 # Types: File, Header, Channel, Lap, Vehicle
├── parse.go                # Core parser: ParseFile, DetectLaps
├── meta.go                 # Partial parser: ParseMeta, ParseMetaFile
├── ldx.go                  # LDX read/write (String + Numeric entries)
├── cmd/
│   ├── ldcli/              # CLI tool with subcommands
│   │   ├── main.go         # Entry point, guide, info/laps commands
│   │   ├── analyze.go      # analyze braking/throttle/tyre
│   │   ├── median.go       # analyze median + deviation
│   │   ├── map.go          # map command
│   │   ├── report.go       # report generation
│   │   └── README.md       # CLI documentation
│   └── ldviewer/           # HTTP SVG telemetry viewer
├── internal/ldx/           # Internal XML parsing
├── testdata/               # Sanitized test files
└── Sample.ld, Sample.ldx   # Reference data files
```

## Go API

### Full Parsing

```go
// Load all channel data
f, _ := ldparser.ParseFile("telemetry.ld")

// All channels with raw data
for _, ch := range f.Channels {
    fmt.Printf("%s [%s] %dHz\n", ch.Name, ch.Unit, ch.Freq)
    // ch.Data is []float64 with converted values
}

// Lookup by name (case-insensitive)
ch := f.ChannelByName("Throttle Pos")

// Lap detection
laps := f.DetectLaps()

// Vehicle full name (LMU/rFactor2 stores it in LongName)
veh := f.Header.Event.Venue.Vehicle
name := veh.LongName  // "BMW GT3 Custom Team 2025 #397"
if name == "" { name = veh.ID }

// LDX generation
ldx := ldparser.GenerateLDX(f, laps)
ldparser.WriteLDX(ldx, "out.ldx")

// LDX parsing (including setup data)
ldx, _ := ldparser.ParseLDXFile("telemetry.ldx")
```

### Partial Parsing (Fast Metadata-Only)

For scanning many files without loading all sample data:

```go
// Reads ~35KB instead of 45MB for a large race file
// 8–16× faster, 33–52× fewer allocations vs ParseFile
f, _ := ldparser.ParseMetaFile("telemetry.ld")
laps := f.DetectLaps()
// f.Channels has full metadata, Data == nil for non-lap channels
```

## CLI Tool: `ldcli`

Command-line tool in `cmd/ldcli/` designed for LLM-assisted analysis. All output is JSON by default.

### Quick Start

```bash
ldcli laps session.ld                    # Driver, venue, lap times
ldcli info session.ld --brief            # File metadata + channel count
ldcli inspect session.ld                 # Data quality + channel groups
ldcli summarize session.ld --lap 3       # Per-channel stats
ldcli events session.ld --lap 3          # Driving events (braking zones, apexes, etc.)
ldcli analyze median session.ld          # Synthetic median lap
ldcli guide                              # Full JSON reference
```

### All Commands

| Command | What it does |
|---------|-------------|
| `ldcli laps <file>` | Human-readable session summary + lap table |
| `ldcli info <file> [--format json\|text\|laps\|brief]` | File metadata, laps, full channel catalogue |
| `ldcli inspect <files...>` | Data quality, interesting channels, channel groups |
| `ldcli summarize <files...> [--lap N\|all]` | Per-channel statistics (min, max, mean, std, p50) |
| `ldcli events <files...> [--lap N\|all] [--type TYPE]` | Session events (shifts, braking zones, apexes, lockups) |
| `ldcli diff <file(s)> [--ref N --cmp M]` | Lap-to-lap time delta with sector breakdown |
| `ldcli compare <files...> [--lap N\|best]` | Side-by-side channel stats across sessions |
| `ldcli data <files...> [--lap N] [--ch name]` | Time-series channel data (LTTB downsampled) |
| `ldcli analyze braking <file>` | Brake zone classification (clean vs hesitant) |
| `ldcli analyze throttle <file>` | Throttle delay from apex by corner speed band |
| `ldcli analyze tyre <file>` | Tyre temperature balance per lap |
| `ldcli analyze median <file>` | Synthetic median lap — driver's repeatable baseline |
| `ldcli analyze deviation <file>` | Speed deviation diagram — where best laps differ |
| `ldcli map <file>` | SVG track map colored by speed |
| `ldcli report <file> [--lap N] [--ref M]` | HTML or ASCII telemetry report |
| `ldcli guide` | Full JSON reference for LLM workflows |

## Deep Command Reference

### `analyze median` — Synthetic Median Lap

A distance-aligned pointwise median across all complete laps within ±threshold ms of best lap. Reveals what the driver does **on every lap** — suppressing one-off outlier events, amplifying repeating patterns.

**Algorithm:**
1. Select laps within ±threshold of best lap time (default 100ms). Only laps ≥30s qualify.
2. For each lap, integrate speed (kph → m/s × dt) to get cumulative distance.
3. Resample every channel onto `bins` uniform distance points via linear interpolation.
4. At each distance bin, take the pointwise median across all N laps.

Distance alignment is critical: it places every corner at the same distance position regardless of lap time variation, so throttle/brake curves overlap correctly.

**Usage:**
```bash
ldcli analyze median session.ld --threshold 500 --ch "Brake Pos,Ground Speed"
ldcli analyze median session.ld --threshold 300 --bins 2000   # full baseline capture
```

**Flags:**
- `--threshold` (default 100ms): ±ms around best lap time
- `--bins` (default 1000): Distance resampling resolution
- `--ch`: Comma-separated channel names or globs

**Threshold guidelines:**
- Sim hotlap / very consistent: 100–200 ms
- Normal practice: 300–500 ms
- Endurance / varied conditions: 800–1500 ms

### `analyze deviation` — Speed Deviation Diagram

Pointwise standard deviation of speed (and optionally other channels) across the best laps within ±threshold of best lap time. Shows **where on track your best laps differ from each other**.

```bash
ldcli analyze deviation session.ld                          # speed only, 750ms window
ldcli analyze deviation session.ld --threshold 400 --ch "Ground Speed,Brake Pos"
```

Default threshold: **750ms** — wide enough to catch good-but-not-brilliant laps, tight enough to exclude slow laps. Peaks in the deviation trace = inconsistency zones. Flat line on straights is normal; peaks at corner entries = braking point variation; peaks at exits = throttle variation.

### `map` — Track Map SVG

Renders a self-contained SVG track map from position channels, colored by speed.

```bash
ldcli map session.ld                                        # best lap, 800×600
ldcli map session.ld --lap all                              # overlay all laps
ldcli map session.ld --mark "0.12:T1:#e74c3c" --mark "0.31:T2"   # annotated
```

**Position source detection (priority order):**
1. `Car Coord X` / `Car Coord Y` — world metres (Assetto Corsa). No projection needed.
2. `GPS Latitude` / `GPS Longitude` — degrees (GT7, LMU, hardware loggers). Equirectangular projection.

**Mark format:** `dist_frac:label` or `dist_frac:label:color`
- `dist_frac`: 0..1 along lap by distance (0 = start/finish, 0.5 = halfway)
- Arrow points in the driving direction at that point
- Color defaults to yellow (`#f1c40f`). Suggestions: red `#e74c3c`, blue `#3498db`, purple `#9b59b6`

## Channel Name Variations by Game

| Game / Hardware | Speed | Throttle | Brake | Gear |
|----------------|-------|----------|-------|------|
| ADL Logger | `Ground Speed` | `Throttle Pos` | `Brake Pos` | `Gear` |
| LMU / rFactor2 | `Ground Speed` | `Throttle Pos` | `Brake Pos` | `Gear` |
| Assetto Corsa | `Ground Speed` | `Throttle Pos` | `Brake Pos` | `Gear` |
| GT7 | `Ground Speed` | `Throttle Pos` | `Brake Pos` | `Gear` |

**Never guess channel names** — always read from `ldcli info <file>` first. Tyre channels vary wildly.

## Vehicle Name Quirk (LMU / rFactor2)

The `Vehicle.ID` field (offset 0:64 in vehicle block) often contains only a short string like `"GT3"`. The full name (e.g. `"BMW GT3 Custom Team 2025 #397"`) lives at offset 64:192 (`Vehicle.LongName`). The CLI and library prefer `LongName` when non-empty.

## In Progress

- **Interactive hover crosshair**: Currently charts are static `<img>` tags. Need to switch to inline SVGs or add a `/api/data` JSON endpoint for client-side rendering with hover lookup.

## Sample Data Files

| Source           | Channels | Laps | File Size |
|-----------------|----------|------|-----------|
| ADL Logger       | 78       | 5    | 1.2 MB    |
| LMU Practice     | 70       | 3    | 557 KB    |
| LMU Race         | 245      | 24   | 45 MB     |

## Test Files

| File | Source | Channels | Laps | Size |
|------|--------|----------|------|------|
| `ac-tatuusfa1-spa.ld` | Assetto Corsa | 166 | 3 | 3.0 MB |
| `lmu-bmwgt3-spa-q1.ld` | LMU qualifying | 78 | 2 | 3.5 MB |
| `gt7-alsace-mini.ld` | GT7 | 37 | 5 | 1.7 MB |

Driver name zeroed in `gt7-alsace-mini.ld` (privacy).

## Reference

- `README.md` — Project overview and quick start
- `cmd/ldcli/README.md` — CLI user guide and token efficiency
- `github.com/gotzl/ldparser` — Original Python reference implementation
