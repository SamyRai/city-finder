package routes

// heapAllocMetricName is the runtime gauge backing go_heap_alloc_bytes:
// /memory/classes/heap/objects:bytes is maintained continuously by the
// runtime, so reading it needs no stop-the-world.
const heapAllocMetricName = "/memory/classes/heap/objects:bytes"
