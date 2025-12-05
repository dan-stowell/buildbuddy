#!/usr/bin/env bash
set -e

# Prints the latest BuildBuddy version tag, like "v2.12.8"
# If not in a git repo, outputs nothing (caller should handle fallback).
if ! git rev-parse --git-dir >/dev/null 2>&1; then
    exit 0
fi

git tag -l 'v*' --sort=creatordate |
    perl -nle 'if (/^v\d+\.\d+\.\d+$/) { print $_ }' |
    tail -n1

