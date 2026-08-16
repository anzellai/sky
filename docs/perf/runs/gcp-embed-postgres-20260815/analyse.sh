#!/bin/bash
SD=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/pgbench
echo "=============== PER-LEVEL SUMMARY (mean over repeats; range shown) ==============="
awk -F'\t' 'NR>1 && $22=="true" {
  k=$1"|"$2; n[k]++; tp[k]+=$17; est[k]+=$15; kbs[k]+=$16; be[k]+=$14; p50[k]+=$18; p95[k]+=$19; er[k]+=$21;
  if(tp[k"max"]==""||$17>tpmax[k])tpmax[k]=$17; if(tpmin[k]==""||$17<tpmin[k])tpmin[k]=$17;
  if(kbmax[k]==""||$16>kbmax[k])kbmax[k]=$16; if(kbmin[k]==""||$16<kbmin[k])kbmin[k]=$16;
}
END{
  printf "%-4s %-5s %-3s %-8s %-22s %-24s %-8s %-9s %-8s\n","cfg","n","N","est","tput/s (min-max)","kB/session (min-max)","p50ms","p95ms","backends";
  for(k in n){split(k,a,"|");
   printf "%-4s %-5s %-3d %-8.0f %-22s %-24s %-8.0f %-9.0f %-8.1f\n",a[1],a[2],n[k],est[k]/n[k],
     sprintf("%.1f (%.1f-%.1f)",tp[k]/n[k],tpmin[k],tpmax[k]),
     sprintf("%.0f (%.0f-%.0f)",kbs[k]/n[k],kbmin[k],kbmax[k]),
     p50[k]/n[k],p95[k]/n[k],be[k]/n[k]}
}' $SD/sweep.tsv | sort -k1,1 -k2,2n

echo
echo "=============== REPEAT TREND (credit drain check) ==============="
awk -F'\t' 'NR>1 && $22=="true"{printf "%s n=%s r=%s tput=%.1f\n",$1,$2,$3,$17}' $SD/sweep.tsv | sort -k1,1 -k2,2 -k3,3

echo
echo "=============== OLS: load_app_rss_kb  vs  established  (levels >=25) ==============="
for c in A B C; do
awk -F'\t' -v C="$c" 'NR>1 && $1==C && $22=="true" && $2>=25 {x=$15; y=$10; n++; sx+=x; sy+=y; sxx+=x*x; sxy+=x*y}
END{ if(n>1){ b=(n*sxy-sx*sy)/(n*sxx-sx*sx); a=(sy-b*sx)/n;
  printf "%s: n=%d  slope=%.1f kB/session  intercept=%.2f MB\n", C, n, b, a/1024 } else printf "%s: INSUFFICIENT ACTIVITY (n=%d)\n",C,n+0 }' $SD/sweep.tsv
done

echo
echo "=============== OLS: postgres tree RSS  vs  established ==============="
for c in B C; do
awk -F'\t' -v C="$c" 'NR>1 && $1==C && $22=="true" && $2>=25 {x=$15; y=$11; n++; sx+=x; sy+=y; sxx+=x*x; sxy+=x*y}
END{ if(n>1){ b=(n*sxy-sx*sy)/(n*sxx-sx*sx); a=(sy-b*sx)/n;
  printf "%s: n=%d  slope=%.1f kB/session (pg tree RSS)  intercept=%.2f MB\n", C, n, b, a/1024 } else printf "%s: INSUFFICIENT ACTIVITY\n",C }' $SD/sweep.tsv
done

echo
echo "=============== IDLE (cold, per restart) ==============="
awk -F'\t' 'NR>1{k=$1; n[k]++; a[k]+=$4; p[k]+=$5; q[k]+=$6; m[k]+=$9; np[k]=$8}
END{printf "%-4s %-4s %-14s %-16s %-16s %-14s %s\n","cfg","N","app_rss_MB","pg_treeRSS_MB","pg_treePSS_MB","memAvail_MB","pg_nproc";
for(k in n) printf "%-4s %-4d %-14.2f %-16.2f %-16.2f %-14.1f %s\n",k,n[k],a[k]/n[k]/1024,p[k]/n[k]/1024,q[k]/n[k]/1024,m[k]/n[k]/1024,np[k]}' $SD/sweep.tsv | sort

echo
echo "=============== GENERATOR LOAD (must stay tiny) ==============="
awk -F'\t' 'NR>1{if($23>mx)mx=$23; s+=$23; n++} END{printf "max=%.3f%% of the 8-core generator, mean=%.3f%%, n=%d runs\n",mx,s/n,n}' $SD/sweep.tsv

echo
echo "=============== INVALID / DISCARDED RUNS ==============="
awk -F'\t' 'NR>1 && $22!="true"{printf "%s n=%s r=%s est=%s tput=%s err=%s valid=%s\n",$1,$2,$3,$15,$17,$21,$22}' $SD/sweep.tsv
