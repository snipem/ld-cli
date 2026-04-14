# ld-cli

LLM-friendly cli tool for reading `.ld` race telemetry log files (a.k.a. MoTeC i2 format) — the de facto standard format for sim racing telemetry across iRacing, Assetto Corsa Competizione (ACC), LMU, and many other platforms. Zero external dependencies.

The binary format was documented by the community. This Go port is based on the implementation at [github.com/gotzl/ldparser](https://github.com/gotzl/ldparser).

> **Disclaimer**: This project is not affiliated with, endorsed by, or created by MoTeC i2. It is an independent, open-source implementation created for scientific and educational purposes only.

**Using with Claude/LLMs?** See [`SKILL.md`](SKILL.md) for the LLM-friendly workflow guide.

## What are `.ld` files?

`.ld` (race log) files are binary telemetry records from racing simulators and data acquisition systems. They contain:
- **Session metadata**: driver, vehicle, venue, timestamp
- **Channel data**: 50–245+ channels of time-series samples (speed, throttle, brake, suspension, temperatures, tire data, etc.)
- **Lap boundaries**: automatic detection via lap number or distance resets
- **Sample rates**: 10–20 Hz per channel, some channels at 1–2 Hz for aggregates

## Quick Start

```bash
# Build
make build

# Start here — human-readable lap overview
ldcli laps session.ld

# Browse a folder interactively (requires fzf)
./ldpick.sh ~/telemetry/

# Full analysis workflow
ldcli events session.ld --lap 2 --type braking_zone
ldcli diff session.ld --ref 2 --cmp 3
ldcli report session.ld --lap 2 --out report.html
ldcli guide                                           # JSON docs for LLM usage
```

## Usage with AI Agents / LLMs

Just tell your agent to analyze a ld file using ldcli. Point it to ld-cli guide to get yourself started.

## Features

- **Game-agnostic**: Works with any `.ld` file from any sim (iRacing, ACC, LMU, rFactor2, etc.)
- **Upfront parsing**: Entire file loaded and decoded in one pass (45 MB in <1 second)
- **Multi-format support**: Reads `.ld` binary; writes/reads `.ldx` XML index files
- **Lap detection**: Uses "Lap Number"/"Lap Time" channels or falls back to "Lap Distance" resets
- **CLI tools**:
  - `laps` — Human-readable lap overview: driver, vehicle, venue, lap times, best lap marker
  - `info` — File header, lap boundaries, channel catalog (JSON)
  - `inspect` — Data quality metrics, channel groups, interesting channels
  - `summarize` — Per-channel statistics (min/max/mean/p5/p50/p95/std); per-sector trends; lap trends
  - `events` — Gear shifts, braking zones, corners, full-throttle zones, wheel lockups
  - `diff` — Lap-to-lap time delta with sector breakdown
  - `data` — Time-series samples (LTTB downsampling by default)
  - `report` — HTML (dark SVG + event overlays) or ASCII (unicode blocks) telemetry report
  - `analyze braking/throttle/tyre` — Deep coaching analysis per lap
  - `compare` — Side-by-side channel stats across sessions
  - `guide` — JSON documentation + escalation strategy for LLM usage
- **Example viewer**: Interactive web UI with per-lap traces, lap comparison, channel picker

## Architecture

- `ldparser.go` — Core parser. Binary format reverse-engineered; documented in `AGENTS.md`
- `ldx.go` — XML read/write for `.ldx` index files
- `cmd/ldcli/` — LLM-friendly command-line interface with JSON output
- `cmd/ldviewer/` — HTTP server with interactive telemetry viewer (no external JS/CSS)

## Test Coverage

- **96.2%** coverage of library functions
- Test files include synthetic data for all data types (float16/32/64, int16/32)
- Lap detection test with distance-based lap resets
- LDX round-trip read/write validation

## Build & Install

```bash
make build          # build all tools
make install        # install as global binaries
make test           # run test suite
make coverage       # generate HTML coverage report
make clean          # clean build artifacts
```
## References

- `CLAUDE.md` — Architecture decisions and design rationale
- `AGENTS.md` — Complete binary format documentation with byte-offset tables
- `cmd/ldcli/` — LLM escalation strategy and token-efficient output formats
