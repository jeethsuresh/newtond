#!/usr/bin/env bash
set -euo pipefail
python3 "${GRAVITAS_ACTION_PATH:?}/gravitas.py" render --repo jeethsuresh/newtond --image "$1" |
  python3 deploy/udp.py |
  kubectl apply --server-side --field-manager=gravitas-deploy -f -
