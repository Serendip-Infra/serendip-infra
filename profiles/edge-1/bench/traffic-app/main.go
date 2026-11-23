// t012-otel: spec 025 T012 criterion 7 and T013's traffic phases. A plain OpenTelemetry Go SDK app:
// otelhttp on the server and the client, the OTLP/HTTP exporter configured
// only by the standard OTEL_* variables. No Serendip-specific code.
//
//	PORT (8080)  ERROR_PCT (0)  DELAY_MS (0)
//	TARGET       if set, GET it every CALL_EVERY_MS (500)
package main

import (
	"context"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func num(k string, d int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return n
	}
	return d
}

func main() {
	ctx := context.Background()
	exp, err := otlptracehttp.New(ctx) // OTEL_EXPORTER_OTLP_ENDPOINT
	if err != nil {
		log.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp)) // OTEL_SERVICE_NAME via resource default
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	errPct, delay := num("ERROR_PCT", 0), num("DELAY_MS", 0)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(delay) * time.Millisecond)
		if rand.IntN(100) < errPct {
			http.Error(w, "boom", 500)
			return
		}
		io.WriteString(w, "ok\n")
	})
	if t := os.Getenv("TARGET"); t != "" {
		// A dependency's address can come without a scheme (host:port).
		if !strings.Contains(t, "://") {
			t = "http://" + t
		}
		every := time.Duration(num("CALL_EVERY_MS", 500)) * time.Millisecond
		c := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 10 * time.Second}
		go func() {
			for range time.Tick(every) {
				ctx, span := otel.Tracer("t012-otel").Start(context.Background(), "call")
				req, _ := http.NewRequestWithContext(ctx, "GET", t, nil)
				if resp, err := c.Do(req); err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				span.End()
			}
		}()
	}
	addr := ":" + strconv.Itoa(num("PORT", 8080))
	log.Printf("t012-otel on %s, errors %d%%, delay %dms, target %q", addr, errPct, delay, os.Getenv("TARGET"))
	log.Fatal(http.ListenAndServe(addr, otelhttp.NewHandler(h, "serve")))
}
