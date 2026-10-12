#!/usr/bin/env bash
# Run as root after the benchmark. Never open special files for comparison.
set -euo pipefail
work="${1:?usage: validate.sh BENCH_WORK RESULTS_DIR}"
results="${2:?}"
script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
mkdir -p "$results" "$results/base-mount" "$results/candidate-mount"
base_mounted=false
candidate_mounted=false
cleanup() {
    if "$candidate_mounted"; then umount "$results/candidate-mount"; fi
    if "$base_mounted"; then umount "$results/base-mount"; fi
}
trap cleanup EXIT
# e2fsck failures are evidence, not reasons to skip tree comparisons.
set +e
e2fsck -fn "$work/baseline.ext4" > "$results/baseline.fsck.txt" 2>&1
base_check=$?
set -e
printf '%s\n' "$base_check" > "$results/baseline.fsck.status"
mount -o loop,ro "$work/baseline.ext4" "$results/base-mount"
base_mounted=true
find "$results/base-mount" -printf '%P\t%y\t%m\t%U:%G\t%T@\t%l\n' | LC_ALL=C sort > "$results/baseline.find.txt"
for image in "$work"/*.ext4; do
    variant="$(basename "$image" .ext4)"
    if [[ "$variant" == baseline ]]; then continue; fi
    set +e
    e2fsck -fn "$image" > "$results/$variant.fsck.txt" 2>&1
    check_status=$?
    set -e
    printf '%s\n' "$check_status" > "$results/$variant.fsck.status"
    du -B1 "$image" > "$results/$variant.du.txt"
    du -B1 --apparent-size "$image" >> "$results/$variant.du.txt"
    mount -o loop,ro "$image" "$results/candidate-mount"
    candidate_mounted=true
    find "$results/candidate-mount" -printf '%P\t%y\t%m\t%U:%G\t%T@\t%l\n' | LC_ALL=C sort > "$results/$variant.find.txt"
    set +e
    python3 "$script_dir/compare.py" "$results/base-mount" "$results/candidate-mount" > "$results/$variant.compare.json"
    compare_status=$?
    set -e
    printf '%s\n' "$compare_status" > "$results/$variant.compare.status"
    # GNU diff follows dangling symlinks and tries to read devices; the Python
    # checker above is the authoritative content and metadata comparison.
    umount "$results/candidate-mount"
    candidate_mounted=false
done
du -B1 "$work/baseline.ext4" > "$results/baseline.du.txt"
du -B1 --apparent-size "$work/baseline.ext4" >> "$results/baseline.du.txt"
