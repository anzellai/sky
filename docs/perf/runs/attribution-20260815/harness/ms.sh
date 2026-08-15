#!/usr/bin/env bash
# ms.sh <file.json> <key>  -> value
tr ',' '\n' < "$1" | tr -d '{}" ' | awk -F: -v k="$2" '$1==k{print $2}'
