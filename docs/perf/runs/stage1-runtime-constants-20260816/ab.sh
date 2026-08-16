#!/usr/bin/env bash
# Alternating-arm A/B for a Go benchmark across two worktrees.
#
# The host is shared with other agents and its load moves by 3x within a
# session, so running all of one arm and then all of the other charges the
# drift to whichever went second. Alternating B/A/B/A puts it on both.
#
#   ab.sh <bench-regex> <reps> <benchtime>
set -uo pipefail
BASE=<base worktree>/runtime-go
NEW=<work worktree>/runtime-go
S=<scratch dir>

RE="${1:?bench regex}"
REPS="${2:-5}"
BT="${3:-500x}"

source "$(git rev-parse --show-toplevel)/scripts/lib/with-timeout.sh"

: > "$S/ab-base.txt"
: > "$S/ab-new.txt"
for rep in $(seq 1 "$REPS"); do
  for arm in base new; do
    dir=$BASE; out=$S/ab-base.txt
    if [ "$arm" = new ]; then dir=$NEW; out=$S/ab-new.txt; fi
    ( cd "$dir" && with_timeout 900 go test ./rt/ -run '^$' -bench "$RE" \
        -benchtime "$BT" -count 1 2>/dev/null ) | grep '^Benchmark' >> "$out"
  done
done

echo "=== median ns/op and allocs/op over $REPS alternating reps ($BT)"
awk '
  { name=$1; sub(/-8$/,"",name)
    ns[name]=ns[name]" "$3; al[name]=al[name]" "$7 }
  END {
    for (n in ns) {
      c=split(ns[n],a," "); asort_n(a,c); printf "%s ns=%s", n, a[int((c+1)/2)]
      c2=split(al[n],b," "); asort_n(b,c2); printf " allocs=%s\n", b[int((c2+1)/2)]
    }
  }
  function asort_n(arr,n,   i,j,t) {
    for(i=1;i<=n;i++) for(j=i+1;j<=n;j++) if(arr[i]+0>arr[j]+0){t=arr[i];arr[i]=arr[j];arr[j]=t}
  }
' "$S/ab-base.txt" | sort > "$S/ab-base-med.txt"
awk '
  { name=$1; sub(/-8$/,"",name)
    ns[name]=ns[name]" "$3; al[name]=al[name]" "$7 }
  END {
    for (n in ns) {
      c=split(ns[n],a," "); asort_n(a,c); printf "%s ns=%s", n, a[int((c+1)/2)]
      c2=split(al[n],b," "); asort_n(b,c2); printf " allocs=%s\n", b[int((c2+1)/2)]
    }
  }
  function asort_n(arr,n,   i,j,t) {
    for(i=1;i<=n;i++) for(j=i+1;j<=n;j++) if(arr[i]+0>arr[j]+0){t=arr[i];arr[i]=arr[j];arr[j]=t}
  }
' "$S/ab-new.txt" | sort > "$S/ab-new-med.txt"

echo "--- BASE"; cat "$S/ab-base-med.txt"
echo "--- NEW ";  cat "$S/ab-new-med.txt"
echo "--- raw spread (base then new)"
sort "$S/ab-base.txt" | awk '{print "  base "$1" "$3" "$4" "$7" "$8}'
sort "$S/ab-new.txt"  | awk '{print "  new  "$1" "$3" "$4" "$7" "$8}'
