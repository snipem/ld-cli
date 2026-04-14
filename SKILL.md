# ldcli — LLM-Friendly Race Telemetry CLI

A command-line tool for analyzing `.ld` race telemetry files from iRacing, Assetto Corsa, Le Mans Ultimate, GT7, and other racing sims. **Designed to work with LLMs** — all output is JSON by default, token counts are documented, and commands are designed for efficient escalation workflows.

**Key idea**: Start with `ldcli info --brief` (~300 tokens), then escalate to deeper analysis (`events`, `summarize`, `analyze`) only when needed.

## Usage

```
ldcli — race telemetry CLI for .ld files

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

  ldcli guide                          Full JSON documentation (for LLMs)
```

## Workflow with LLMs

**Orientation phase** (always start here):
```bash
ldcli info session.ld --brief --format text    # ~300 tokens: driver, venue, lap times
ldcli inspect session.ld --format text         # ~3 KB: channel groups, data quality
```

**Per-lap analysis**:
```bash
ldcli summarize session.ld --lap 3 --ch 'Throttle Pos,Brake Pos,Ground Speed'  # key channel stats
ldcli events session.ld --lap 3 --type braking_zone --format text              # braking events
```

**Comparison**:
```bash
ldcli diff session.ld --ref 2 --cmp 3 --cumulative  # lap-to-lap delta
ldcli compare session1.ld session2.ld --lap best    # cross-session stats
```

**Deep analysis**:
```bash
ldcli analyze braking session.ld --lap 3      # hesitation detection + speed bands
ldcli analyze throttle session.ld --lap 3     # apex-to-throttle delay
ldcli analyze tyre session.ld --lap all       # tyre temperature balance
```

## Token Efficiency

All output costs are documented in `ldcli guide`. Examples:
- `info --brief`: ~300 tokens
- `info` (full channel list): 1–3 KB
- `summarize` per channel: ~80 tokens
- `events` per lap: 0.5–10 KB (depends on event density)
- `data --max-points 150`: ~700 tokens/channel
- `data` full resolution: ⚠️ can be 250k+ tokens — always use `--max-points`

## Key Rules

1. **Never guess channel names** — always read them from `ldcli info` first. Channel names vary wildly across games.
2. **Start cheap** — use `--brief`, `--format text`, `--max-points 150` to keep token usage low.
3. **Escalate incrementally** — start with info/inspect, then add events/summarize, then deep analysis.

## Common Flags

- `--lap N` — Analyze specific lap (default: full session)
- `--lap all` — Analyze all laps (includes trends)
- `--ref N --cmp M` — Compare lap M against reference lap N
- `--ch 'name1,name2'` — Filter specific channels (repeatable)
- `--format json|text|csv` — Output format (JSON default; text is most compact)
- `--max-points N` — Downsample time-series to N points (use for `data`)

## Common Workflows

### Find your repeating mistakes
```bash
ldcli laps session.ld                    # Which lap?
ldcli summarize session.ld --lap 5       # Stats
ldcli events session.ld --lap 5          # What happened?
ldcli analyze median session.ld --threshold 300 --ch "Brake Pos,Throttle Pos"
```
Ask Claude: "Compare the best lap vs median lap. Where does the median brake differently?"

### Is this improvement real?
```bash
ldcli laps monday.ld && ldcli laps friday.ld     # Compare times
ldcli analyze median monday.ld --threshold 500
ldcli analyze median friday.ld --threshold 500
```
Ask Claude: "Are the throttle and braking patterns consistent, or did I just luck into one fast lap?"

### Track consistency
```bash
ldcli analyze deviation session.ld --threshold 400
```
Peaks in stddev = inconsistency zones. "Why is my apex speed all over the place at Turn 3?"

### Tyre management
```bash
ldcli analyze tyre session.ld --lap all
```
Trends: "Tyres are heating up through stint" vs "overheating midway". "Right rear is always hotter".

### Cross-session comparison
```bash
ldcli compare session1.ld session2.ld --lap best
```
Side-by-side: "How much faster is my throttle application in session 2?"

## For Full Documentation

```bash
ldcli guide
```

This outputs a JSON guide with:
- Complete command reference with flags
- Token cost estimates
- Channel names by game
- Recommended workflows
- Escalation strategy for LLM usage
