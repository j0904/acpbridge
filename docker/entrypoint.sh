#!/bin/sh
set -e

exec bridge-acp --config config.json "$@"
