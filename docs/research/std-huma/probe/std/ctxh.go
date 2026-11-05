package main
import ("context";"net/http";"time")
func context_cause(r *http.Request) error { return context.Cause(r.Context()) }
func ctxWT() (context.Context, context.CancelFunc) { return context.WithTimeout(context.Background(), 200*time.Millisecond) }
