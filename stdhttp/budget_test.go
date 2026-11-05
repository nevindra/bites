package stdhttp_test

// The allocations a mounted request costs, measured through the in-memory connection (ADR 0019).
// They are outside the budget of ADR 0003 and the numbers are here so that a rise fails a test.
const (
	allocsGET     = 32
	allocsPOST    = 35
	allocsFlushed = 42 // 31 to 39 measured: the flush hands over between goroutines

	// A typed JSON GET through Handler, from a ResponseRecorder: the in-memory connection, the
	// head written and read back, net/http's parsed answer and the recorder's own (ADR 0019).
	allocsHandlerGET = 64
)
