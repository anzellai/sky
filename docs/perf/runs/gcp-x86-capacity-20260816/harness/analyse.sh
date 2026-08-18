#!/usr/bin/env bash
# analyse.sh — re-derive the per-run system columns from the archived 1 Hz
# samples, correcting two defects in the inline version.
#
# DEFECT 1: the window. `tail -40` of a sampler that runs 40 s PAST the end of
# the load is mostly post-load idle. Here the window is the samples where the
# app actually holds connections -- conn >= 0.8 x max(conn) -- which is the
# loaded plateau by construction.
#
# DEFECT 2: the denominator. CPU share was computed against /proc/stat's total
# jiffies, and on these shared-core e2 instances that total does not tick at
# nproc x 100 Hz: measured on skyperf-small under load it ran at 86 jiffies/s
# where two vCPUs should give 200, while the SAME window had the app's own
# /proc/<pid>/stat accumulating 137 jiffies/s. Dividing one by the other
# produced "the app used 158.9% of a machine that was 58.4% busy". The guest's
# tick accounting stalls when the hypervisor deschedules the vCPU; per-process
# accounting survives it better. So the denominator here is WALL TIME, and the
# unit reported is CORES, which needs no denominator at all:
#
#     app_cores = d_app_jiffies / CLK_TCK / wall_seconds
#
# machine_busy_cores is still reported, from the same /proc/stat, precisely so
# the discrepancy stays visible rather than being quietly normalised away.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
OUT="$BASE/out"
TCK=100

printf 'run\ttarget\tcfg\tblock\tlevel\twin_s\twin_n\tconn_max\tapp_cores\tpg_cores\tmachine_busy_cores\tstat_tick_hz\tload_app_kb\tload_pg_rss_kb\tbackends_max\tmem_avail_min_kb\txact_per_s\twal_rec_per_s\n' >| "$OUT/system.tsv"

for d in "$OUT"/*/; do
  tag=$(basename "$d")
  S="$d/sample.tsv"
  [ -f "$S" ] || continue
  case "$tag" in *smoke*) continue;; esac
  # tag: <target>-<cfg>-n<N>-b<block>r<rep>
  tgt=${tag%%-*}; rest=${tag#*-}; cfg=${rest%%-*}; rest2=${rest#*-}
  lvl=${rest2%%-*}; lvl=${lvl#n}; blk=${rest2#*-b}; blk=${blk%%r*}

  awk -F'\t' -v TCK="$TCK" -v tag="$tag" -v tgt="$tgt" -v cfg="$cfg" -v blk="$blk" -v lvl="$lvl" '
    { ep[NR]=$1; rss[NR]=$2; pgr[NR]=$3; be[NR]=$5; ma[NR]=$6; cn[NR]=$7;
      ct[NR]=$8; ci[NR]=$9; aj[NR]=$10; pj[NR]=$11; xc[NR]=$12; wr[NR]=$13; n=NR
      if($7>mx) mx=$7 }
    END{
      if(n<5 || mx<=0){ printf "%s\t%s\t%s\t%s\t%s\tNA\t0\t%d\tNA\tNA\tNA\tNA\tNA\tNA\tNA\tNA\tNA\tNA\n", tag,tgt,cfg,blk,lvl,mx; exit }
      thr = 0.8*mx
      # first and last sample of the loaded plateau
      for(i=1;i<=n;i++) if(cn[i]>=thr){ lo=i; break }
      for(i=n;i>=1;i--) if(cn[i]>=thr){ hi=i; break }
      # trim one sample each side so a partially-connected edge sample cannot
      # blend ramp-up or tear-down into the plateau
      if(hi-lo>4){ lo++; hi-- }
      w = ep[hi]-ep[lo]
      if(w<=0){ printf "%s\t%s\t%s\t%s\t%s\tNA\t0\t%d\tNA\tNA\tNA\tNA\tNA\tNA\tNA\tNA\tNA\tNA\n", tag,tgt,cfg,blk,lvl,mx; exit }
      ac = (aj[hi]-aj[lo])/TCK/w
      pc = (pj[hi]-pj[lo])/TCK/w
      dt = ct[hi]-ct[lo]; di = ci[hi]-ci[lo]
      mb = (dt-di)/TCK/w
      hz = dt/w
      # median RSS over the plateau
      k=0; for(i=lo;i<=hi;i++){ k++; a[k]=rss[i]; b[k]=pgr[i] }
      asortish(a,k); asortish(b,k)
      mrss=a[int(k/2)+1]; mpg=b[int(k/2)+1]
      bem=0; mam=0
      for(i=lo;i<=hi;i++){ if(be[i]>bem) bem=be[i]; if(mam==0||ma[i]<mam) mam=ma[i] }
      xps="NA"; wps="NA"
      if(xc[lo]>0 && xc[hi]>0){ xps=sprintf("%.1f",(xc[hi]-xc[lo])/w); wps=sprintf("%.1f",(wr[hi]-wr[lo])/w) }
      printf "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%.3f\t%.3f\t%.3f\t%.1f\t%d\t%d\t%d\t%d\t%s\t%s\n",
        tag,tgt,cfg,blk,lvl,w,hi-lo+1,mx,ac,pc,mb,hz,mrss,mpg,bem,mam,xps,wps
    }
    function asortish(arr,len,  i,j,t){ for(i=1;i<len;i++) for(j=1;j<=len-i;j++) if(arr[j]>arr[j+1]){t=arr[j];arr[j]=arr[j+1];arr[j+1]=t} }
  ' "$S" >> "$OUT/system.tsv"
done
column -t -s$'\t' "$OUT/system.tsv"
