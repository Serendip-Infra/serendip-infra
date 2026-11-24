#!/usr/bin/env bash
# Spec 026 T010: same-host A/B of serendipd idle working set on serendip-t018.
# A = main (spec 025 merged), B = spec-026. Each run on a fresh copy of the
# t026 store (it holds a Component whose Deployment uses secret variables),
# read 4.5 min after start, 6 tasklist reads 10 s apart. Order: A B A B.
set -u
S=$(cd "$(dirname "$0")" && pwd)
D=/c/Users/malit/.serendip-t018
OUT=$S/out; mkdir -p $OUT
keys=$(awk -F= '{printf "%s%s:%s", (NR>1?",":""), $2, $1}' $D/t023-keys.env)
K="kubectl --context kind-serendip-t018"

pods() { $K get pods -A --no-headers -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,PHASE:.status.phase > "$1"; }

run() { # $1 label, $2 binary name
  local label=$1 img=$2 db=$OUT/$1.db
  rm -f $db $db-wal $db-shm
  cp $D/t026/t026.db $db; cp $D/t026/t026.db-wal $db-wal 2>/dev/null; cp $D/t026/t026.db-shm $db-shm 2>/dev/null
  echo "== $label ($img) start $(date +%T)" | tee -a $OUT/ab.txt
  SERENDIP_KUBECONFIG=$D/t018.kubeconfig SERENDIP_DB_PATH=$db SERENDIP_ADDR=127.0.0.1:8088 \
  SERENDIP_API_KEYS="$keys" SERENDIP_OPENBAO_CREDENTIALS_PATH=$D/openbao-init.json \
    "$S/$img" > $OUT/$label.log 2>&1 &
  sleep 270
  curl -s -o /dev/null -w "healthz %{http_code}\n" http://127.0.0.1:8088/healthz | tee -a $OUT/ab.txt
  for i in 1 2 3 4 5 6; do
    tasklist //FI "IMAGENAME eq $img" //FO CSV //NH | tee -a $OUT/ab.txt
    sleep 10
  done
  pods $OUT/$label.pods
  $K get secrets -A --no-headers | grep secret-env | tee -a $OUT/ab.txt
  taskkill //F //IM $img > /dev/null
  sleep 5
}

: > $OUT/ab.txt
taskkill //F //IM serendipd.exe > /dev/null 2>&1; sleep 5
pods $OUT/before.pods
run A1 serendipd-main.exe
run B1 serendipd-026.exe
run A2 serendipd-main.exe
run B2 serendipd-026.exe
echo "== done $(date +%T)" | tee -a $OUT/ab.txt
