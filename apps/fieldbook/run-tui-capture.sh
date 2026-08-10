#!/bin/sh
# Capture one Sky.Tui frame from Fieldbook.
#
# Sky.Tui hard-refuses a non-TTY stdin (runtime-go/rt/tui_ui.go:262), so the
# app has to run under a pty. `script` allocates one; `q` is fed in after the
# first frame has been painted so the process exits through the runtime's
# normal TTY-restoring shutdown path instead of being killed.
cd "$(dirname "$0")" || exit 1
rm -f dumps/tui-session.raw dumps/tui-frame.txt
{ sleep 4; printf 'q'; sleep 1; } | TERM=xterm-256color script -q dumps/tui-session.raw ./sky-out/app --tui >/dev/null 2>&1
echo "script exit=$?"
LC_ALL=C perl -pe 's/\e\[[0-9;?]*[a-zA-Z]//g; s/\e[()][B0]//g; s/\e[=>]//g; s/\r//g' dumps/tui-session.raw > dumps/tui-frame.txt
wc -c dumps/tui-session.raw dumps/tui-frame.txt
