#!/usr/bin/env bash
# bench.sh — the one entry point for running and comparing city-finder's Go
# benchmarks. It encodes the measurement protocol in docs/benchmarking.md so
# nobody has to remember it:
#
#   * benchmark binaries are compiled once (go test -c) and run directly, so
#     compilation never lands in a measurement and stderr (index-build logs)
#     never pollutes the benchstat-parseable stdout;
#   * both sides of a comparison are built with ONE pinned toolchain
#     (GOTOOLCHAIN is resolved once and exported), so a Go upgrade can never
#     masquerade as an application change;
#   * A/B runs are INTERLEAVED (base, head, head, base, ...) so thermal and
#     background drift hits both sides equally instead of looking like a
#     regression;
#   * every result directory carries env.txt — toolchain, GOMAXPROCS/GOGC/
#     GOMEMLIMIT/GOEXPERIMENT, CPU, cgroup quota, load — because the machine
#     and runtime configuration are part of the experiment;
#   * the repetition count is fixed up front (default 10 rounds). Do not
#     re-run until benchstat reports a significant p-value: an inconclusive
#     result is a result.
#
# Usage:
#   benchmarks/bench.sh env
#   benchmarks/bench.sh run   [-n count] [-t benchtime] [-r regex] [-o dir] PKG...
#   benchmarks/bench.sh ab    -b BASE_REF [-H HEAD_REF] [-n rounds] [-t benchtime]
#                             [-r regex] [-o dir] PKG
#   benchmarks/bench.sh smoke
#
#   run    capture results for one tree (e.g. a new committed baseline)
#   ab     interleaved comparison of BASE_REF against HEAD_REF (default: the
#          working tree, uncommitted changes included) for one package
#   smoke  run every benchmark exactly once — a correctness check that the
#          benchmarks still build, find their fixtures and pass their own
#          assertions. Its numbers are meaningless; CI runs it.
#
# Environment: BENCHSTAT overrides the benchstat command (default: a pinned
# `go run golang.org/x/perf/cmd/benchstat@...`).
set -euo pipefail

readonly BENCHSTAT_PIN="golang.org/x/perf/cmd/benchstat@v0.0.0-20260929162123-406019bb8b68"
ROOT="$(git rev-parse --show-toplevel)"
readonly ROOT
cd "${ROOT}"

die() { echo "bench.sh: $*" >&2; exit 2; }

benchstat_cmd() {
	if [[ -n "${BENCHSTAT:-}" ]]; then
		# shellcheck disable=SC2086 # BENCHSTAT may carry arguments.
		${BENCHSTAT} "$@"
	else
		go run "${BENCHSTAT_PIN}" "$@"
	fi
}

# pin_toolchain resolves the toolchain the module selects in this tree once
# and exports it, so every later build (both A/B sides) uses that exact
# version even if the base ref's go.mod differs.
pin_toolchain() {
	GOTOOLCHAIN="$(go env GOVERSION)"
	export GOTOOLCHAIN
}

# print_env writes the experiment's configuration as "key: value" lines.
print_env() {
	echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "go: $(go env GOVERSION)"
	echo "goos: $(go env GOOS)"
	echo "goarch: $(go env GOARCH)"
	echo "goamd64: $(go env GOAMD64)"
	echo "goarm64: $(go env GOARM64)"
	echo "goexperiment: $(go env GOEXPERIMENT)"
	echo "goflags: $(go env GOFLAGS)"
	echo "cgo_enabled: $(go env CGO_ENABLED)"
	echo "gomaxprocs_env: ${GOMAXPROCS:-<unset: runtime default>}"
	echo "gogc: ${GOGC:-<unset: 100>}"
	echo "gomemlimit: ${GOMEMLIMIT:-<unset>}"
	echo "pgo: $(find cmd -name default.pgo -print 2>/dev/null | tr '\n' ' ')"
	echo "commit: $(git rev-parse HEAD)$(git diff --quiet HEAD -- || echo '+dirty')"
	echo "os: $(uname -srm)"
	if [[ -r /proc/cpuinfo ]]; then
		echo "cpu: $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
		echo "ncpu: $(nproc)"
		echo "cgroup_cpu_max: $(cat /sys/fs/cgroup/cpu.max 2>/dev/null || echo n/a)"
		echo "governor: $(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null || echo n/a)"
		echo "loadavg: $(cut -d' ' -f1-3 /proc/loadavg)"
	else
		echo "cpu: $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
		echo "ncpu: $(sysctl -n hw.ncpu 2>/dev/null || echo unknown)"
		echo "loadavg: $(sysctl -n vm.loadavg 2>/dev/null || echo unknown)"
	fi
}

# build_test compiles the benchmark binary for pkg in tree into out.
build_test() {
	local tree="$1" pkg="$2" out="$3"
	(cd "${tree}" && go test -c -o "${out}" "${pkg}") || die "build of ${pkg} in ${tree} failed"
	[[ -x "${out}" ]] || die "${pkg} has no test files"
}

# run_test runs one benchmark binary from its package directory (fixtures are
# resolved relative to it), appending results to results and logs to log.
run_test() {
	local bin="$1" dir="$2" regex="$3" count="$4" benchtime="$5" results="$6" log="$7"
	(cd "${dir}" && "${bin}" -test.run '^$' -test.bench "${regex}" -test.benchmem \
		-test.count "${count}" -test.benchtime "${benchtime}") >>"${results}" 2>>"${log}" ||
		die "benchmark run failed; see ${log}"
}

warn_if_noisy() {
	if [[ -r /proc/loadavg ]]; then
		local load ncpu
		load="$(cut -d' ' -f1 /proc/loadavg)"
		ncpu="$(nproc)"
		if awk -v l="${load}" -v n="${ncpu}" 'BEGIN { exit !(l > n * 0.25) }'; then
			echo "bench.sh: WARNING 1-minute load ${load} on ${ncpu} CPUs — results will be noisy" >&2
		fi
	fi
}

cmd_run() {
	local count=10 benchtime=1s regex=. outdir="bench-out/run-$(date +%Y%m%d-%H%M%S)"
	while getopts "n:t:r:o:" opt; do
		case "${opt}" in
		n) count="${OPTARG}" ;; t) benchtime="${OPTARG}" ;;
		r) regex="${OPTARG}" ;; o) outdir="${OPTARG}" ;;
		*) die "unknown flag" ;;
		esac
	done
	shift $((OPTIND - 1))
	(($# > 0)) || die "run: at least one package required"
	pin_toolchain
	mkdir -p "${outdir}"
	outdir="$(cd "${outdir}" && pwd)"
	print_env >"${outdir}/env.txt"
	warn_if_noisy
	local pkg dir bin
	for pkg in "$@"; do
		dir="$(cd "${ROOT}" && go list -f '{{.Dir}}' "${pkg}")"
		bin="${outdir}/$(basename "${dir}").test"
		build_test "${ROOT}" "${pkg}" "${bin}"
		run_test "${bin}" "${dir}" "${regex}" "${count}" "${benchtime}" "${outdir}/results.txt" "${outdir}/stderr.log"
		rm -f "${bin}"
	done
	(cd "${outdir}" && benchstat_cmd results.txt) | tee "${outdir}/summary.txt"
	echo "bench.sh: results in ${outdir}" >&2
}

cmd_ab() {
	local base="" head="" rounds=10 benchtime=1s regex=. outdir="bench-out/ab-$(date +%Y%m%d-%H%M%S)"
	while getopts "b:H:n:t:r:o:" opt; do
		case "${opt}" in
		b) base="${OPTARG}" ;; H) head="${OPTARG}" ;; n) rounds="${OPTARG}" ;;
		t) benchtime="${OPTARG}" ;; r) regex="${OPTARG}" ;; o) outdir="${OPTARG}" ;;
		*) die "unknown flag" ;;
		esac
	done
	shift $((OPTIND - 1))
	[[ -n "${base}" ]] || die "ab: -b BASE_REF required"
	(($# == 1)) || die "ab: exactly one package required"
	local pkg="$1"
	pin_toolchain
	mkdir -p "${outdir}"
	outdir="$(cd "${outdir}" && pwd)"

	local work
	work="$(mktemp -d)"
	# shellcheck disable=SC2064 # expand now: these paths are fixed.
	trap "git -C '${ROOT}' worktree remove --force '${work}/base' >/dev/null 2>&1 || true;
		git -C '${ROOT}' worktree remove --force '${work}/head' >/dev/null 2>&1 || true;
		rm -rf '${work}'" EXIT
	git worktree add --detach "${work}/base" "${base}" >/dev/null
	local head_tree="${ROOT}"
	if [[ -n "${head}" ]]; then
		git worktree add --detach "${work}/head" "${head}" >/dev/null
		head_tree="${work}/head"
	fi

	local reldir
	reldir="$(cd "${ROOT}" && go list -f '{{.Dir}}' "${pkg}")"
	reldir="${reldir#"${ROOT}"/}"
	build_test "${work}/base" "./${reldir}" "${work}/base.test"
	build_test "${head_tree}" "./${reldir}" "${work}/head.test"

	{
		print_env
		echo "base_ref: ${base} ($(git rev-parse "${base}"))"
		echo "head_ref: ${head:-working tree}"
		echo "package: ${pkg}"
		echo "bench_regex: ${regex}"
		echo "rounds: ${rounds}"
		echo "benchtime: ${benchtime}"
	} >"${outdir}/env.txt"
	warn_if_noisy

	local r
	for ((r = 1; r <= rounds; r++)); do
		# Alternate the order each round so neither side always runs first.
		local order=(base head)
		((r % 2 == 0)) && order=(head base)
		local side tree
		for side in "${order[@]}"; do
			tree="${work}/base"
			[[ "${side}" == head ]] && tree="${head_tree}"
			run_test "${work}/${side}.test" "${tree}/${reldir}" "${regex}" 1 "${benchtime}" \
				"${outdir}/${side}.txt" "${outdir}/stderr.log"
		done
		echo "bench.sh: round ${r}/${rounds} done" >&2
	done
	(cd "${outdir}" && benchstat_cmd base.txt head.txt) | tee "${outdir}/summary.txt"
	echo "bench.sh: results in ${outdir} (base.txt, head.txt, env.txt, summary.txt)" >&2
}

cmd_smoke() {
	local log
	log="$(mktemp)"
	if ! go test -run '^$' -bench . -benchtime 1x ./... >"${log}" 2>&1; then
		grep -v '^[0-9]\{4\}/[0-9][0-9]/[0-9][0-9] ' "${log}" >&2 # drop library log lines
		rm -f "${log}"
		die "smoke: a benchmark failed"
	fi
	rm -f "${log}"
	echo "bench.sh: every benchmark ran once and passed its assertions"
}

case "${1:-}" in
env) print_env ;;
run) shift; cmd_run "$@" ;;
ab) shift; cmd_ab "$@" ;;
smoke) cmd_smoke ;;
*) sed -n '/^# Usage:/,/^# Environment/p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac
