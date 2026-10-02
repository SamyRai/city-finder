// Package loadgen drives an HTTP service with an OPEN workload model: requests
// are issued at a constant arrival rate, independent of how fast responses
// come back, and every latency is measured from the request's INTENDED send
// time rather than from when the client actually got around to sending it.
//
// That is the difference between a load test and a closed-loop benchmark. A
// closed loop (send, wait, send, wait) stops generating traffic while the
// server stalls, so it records only the one request that saw the stall and
// reports a reassuring distribution — coordinated omission. Real traffic
// keeps arriving during a stall; here, so do requests, and the backlog they
// build is charged to the latency distribution where it belongs.
//
// The unit of measurement is a Step: one offered rate held for a fixed
// window. A sweep of increasing rates yields the saturation curve (offered vs
// achieved throughput, errors, p50…p99.9), whose knee is the capacity figure
// worth reporting — not the maximum throughput of a single run.
package loadgen
