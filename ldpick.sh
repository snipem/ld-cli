#!/usr/bin/env bash
# ldpick — browse .ld files with fzf; ldcli laps preview on the right.
# Usage: ldpick [directory]   (defaults to current directory)

DIR="${1:-.}"
LDCLI="${LDCLI:-ldcli}"

# Verify ldcli is available
if ! command -v "$LDCLI" &>/dev/null; then
    echo "ldcli not found. Set LDCLI=/path/to/ldcli or put it on PATH." >&2
    exit 1
fi

selected=$(
    find "$DIR" -name "*.ld" -not -name "*.ldx" \
    | sort \
    | fzf \
        --prompt="ld file > " \
        --preview="$LDCLI laps {}" \
        --preview-window="right:55%:wrap" \
        --bind="ctrl-/:toggle-preview" \
        --header="ENTER to select  CTRL-/ toggle preview  ESC quit"
)

[ -n "$selected" ] && echo "$selected"
