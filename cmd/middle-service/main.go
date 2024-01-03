package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/josephcopenhaver/example-otlp-project-go/internal/clients/endsvc"
	"github.com/josephcopenhaver/example-otlp-project-go/internal/service/router"

	"github.com/josephcopenhaver/xit/xnet/xhttp"
	hostmetric "go.opentelemetry.io/contrib/instrumentation/host"
	runtimemetric "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

var ServiceName = "middle-service"

type syncErr struct {
	rwm sync.RWMutex
	err error
}

func (se *syncErr) Set(err error) {
	if err == nil || se.err != nil {
		return
	}

	se.rwm.Lock()
	defer se.rwm.Unlock()

	if se.err == nil {
		se.err = err
	}
}

func (se *syncErr) Err() error {
	se.rwm.RLock()
	defer se.rwm.RUnlock()

	return se.err
}

func rootContext() (context.Context, func()) {

	ctx, cancel := context.WithCancel(context.Background())

	procDone := make(chan os.Signal, 1)

	signal.Notify(procDone, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		defer cancel()

		done := ctx.Done()

		var requester string
		select {
		case <-procDone:
			requester = "user"
		case <-done:
			requester = "process"
		}
		slog.Warn(
			"shutdown requested",
			"requester", requester,
		)
	}()

	return ctx, cancel
}

type nopShutdownMetricExporter struct {
	sdkmetric.Exporter
}

func (_ nopShutdownMetricExporter) Shutdown(context.Context) error { return nil }

func main() {
	var ctx context.Context
	{
		c, cancel := rootContext()
		defer cancel()
		ctx = c
	}

	defer func() {
		r := recover()
		if r != nil {
			defer panic(r)
			slog.ErrorContext(ctx,
				"graceful shutdown",
				"status", "error",
				"error", r,
			)
			return
		}

		slog.WarnContext(ctx,
			"graceful shutdown",
			"status", "ok",
		)
	}()

	// set log level and log format
	{
		level := slog.LevelInfo
		if s := os.Getenv("LOG_LEVEL"); s != "" {
			var v slog.Level
			if err := v.UnmarshalText([]byte(s)); err == nil {
				level = v
			}
		}

		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			AddSource: true,
			Level:     level,
		})))
	}

	// instrument otel metrics and tracing
	if !otelDisabled() {
		var shutdownWG sync.WaitGroup
		var shutdownSE syncErr

		var metricsWG sync.WaitGroup
		var metricsSE syncErr

		// wait for concurrent goroutines shutdown to finish
		defer func() {
			metricsWG.Wait()

			shutdownWG.Wait()

			if err := metricsSE.Err(); err != nil {
				panic(err)
			}

			if err := shutdownSE.Err(); err != nil {
				panic(err)
			}
		}()

		var metricExp sdkmetric.Exporter
		// initialize the metric exporter
		{
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "")
			enc.SetEscapeHTML(false)

			e, err := stdoutmetric.New(stdoutmetric.WithEncoder(enc))
			if err != nil {
				panic(fmt.Errorf("failed to create stdout default metric exporter: %w", err))
			}

			defer func() {
				shutdownWG.Add(1)
				go func() {
					defer shutdownWG.Done()

					// wait for usage of the metrics exporter to stop
					metricsWG.Wait()

					if err := e.Shutdown(context.Background()); err != nil {
						slog.ErrorContext(ctx,
							"failed to shutdown metric exporter",
							"error", err,
						)

						shutdownSE.Set(err)
					}
				}()
			}()

			metricExp = nopShutdownMetricExporter{e}
		}

		// defined service resource meta used in metric exporters
		svcRes := resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(ServiceName),
		)

		// initialize metrics provider
		{
			mp := sdkmetric.NewMeterProvider(
				sdkmetric.WithResource(svcRes),
				sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
					metricExp,
					// default interval is 60s, default (flush) timeout is 30s
					sdkmetric.WithInterval(10*time.Second),
					sdkmetric.WithTimeout(30*time.Second),
				)),
			)

			defer func() {
				metricsWG.Add(1)
				go func() {
					defer metricsWG.Done()

					err := mp.Shutdown(context.Background())
					if err != nil {
						slog.ErrorContext(ctx,
							"failed to shutdown meter provider",
							"error", err,
						)

						metricsSE.Set(err)
					}
				}()
			}()

			otel.SetMeterProvider(mp)
		}

		// report host metrics every second
		{
			// TODO: host metrics such as inodes, open file handles, open ports, disk, and network util should be handled by the app-mesh
			// this likely can all go away

			// https://github.com/open-telemetry/opentelemetry-go-contrib/blob/main/instrumentation/host/example/main.go

			// Register the exporter with an SDK via a periodic reader.
			mp := sdkmetric.NewMeterProvider(
				sdkmetric.WithResource(svcRes),
				sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
					metricExp,
					sdkmetric.WithInterval(1*time.Second),
				)),
			)

			defer func() {
				metricsWG.Add(1)
				go func() {
					defer metricsWG.Done()

					err := mp.Shutdown(context.Background())
					if err != nil {
						slog.ErrorContext(ctx,
							"failed to gracefully shutdown service host metrics",
							"error", err,
						)

						metricsSE.Set(err)
					}
				}()
			}()

			err := hostmetric.Start(hostmetric.WithMeterProvider(mp))
			if err != nil {
				panic(fmt.Errorf("failed to start host metrics exporter: %w", err))
			}
		}

		// report runtime metrics every second
		{
			// https://github.com/open-telemetry/opentelemetry-go-contrib/blob/main/instrumentation/runtime/example/main.go

			// Register the exporter with an SDK via a periodic reader.
			mp := sdkmetric.NewMeterProvider(
				sdkmetric.WithResource(svcRes),
				sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
					metricExp,
					sdkmetric.WithInterval(1*time.Second),
				)),
			)

			defer func() {
				metricsWG.Add(1)
				go func() {
					defer metricsWG.Done()

					err := mp.Shutdown(context.Background())
					if err != nil {
						slog.ErrorContext(ctx,
							"failed to gracefully shutdown service runtime metrics",
							"error", err,
						)

						metricsSE.Set(err)
					}
				}()
			}()

			err := runtimemetric.Start(runtimemetric.WithMinimumReadMemStatsInterval(1 * time.Second))
			if err != nil {
				panic(fmt.Errorf("failed to start runtime metrics exporter: %w", err))
			}
		}

		// initialize tracer provider
		{
			// TODO: first http.request layer of traces should be provided by the app-mesh, not the container

			// https://github.com/open-telemetry/opentelemetry-go-contrib/blob/main/instrumentation/net/http/otelhttp/example/client/client.go

			// Create stdout exporter to be able to retrieve
			// the collected spans.
			exporter, err := stdouttrace.New()
			if err != nil {
				panic(fmt.Errorf("failed to create tracer provider: %w", err))
			}

			// For the demonstration, use sdktrace.AlwaysSample sampler to sample all traces.
			// In a production application, use sdktrace.ProbabilitySampler with a desired probability.
			tp := sdktrace.NewTracerProvider(
				sdktrace.WithSampler(sdktrace.AlwaysSample()),
				sdktrace.WithBatcher(exporter),
			)

			defer func() {
				shutdownWG.Add(1)
				go func() {
					defer shutdownWG.Done()

					err := tp.Shutdown(context.Background())
					if err != nil {
						slog.ErrorContext(ctx,
							"failed to gracefully shutdown tracer provider",
							"error", err,
						)

						shutdownSE.Set(err)
					}
				}()
			}()

			otel.SetTracerProvider(tp)
			otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
		}
	}

	host := "127.0.0.1"
	if s, ok := os.LookupEnv("LISTEN_HOST"); ok {
		host = s
	}

	port := 8080
	if s := os.Getenv("LISTEN_PORT"); s != "" {
		i, err := strconv.Atoi(s)
		if err != nil {
			panic(fmt.Errorf("bad LISTEN_PORT value: %w", err))
		}
		port = i
	}

	endSvcUrl := "http://end-service"
	if s := os.Getenv("END_SVC_URL"); s != "" {
		_, err := url.Parse(s)
		if err != nil {
			panic(fmt.Errorf("END_SVC_URL is not a valid url: %w", err))
		}
		endSvcUrl = s
	}

	op := xhttp.ClientOpts()
	endSvcClient, err := endsvc.New(
		op.BaseURL(endSvcUrl),
		op.UserAgent("middle-svc"),
	)
	if err != nil {
		panic(err)
	}

	r := router.New(nil, nil, nil, nil)
	r.RegisterCleanup(endSvcClient.CloseIdleConnections)
	defer r.Cleanup()

	r.GetE("/healthcheck", func(w http.ResponseWriter, r *http.Request) error {
		// https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/

		// A common pattern for liveness probes is to use the same low-cost HTTP endpoint as for readiness
		// probes, but with a higher failureThreshold. This ensures that the pod is observed as not-ready
		// for some period of time before it is hard killed.

		h := w.Header()
		h.Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_, ignoredErr := w.Write([]byte(`{"status":"ok"}` + "\n"))
		_ = ignoredErr

		return nil
	})

	r.GetE("/hello", func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()

		resp, err := endSvcClient.Goodbye(ctx)
		if err != nil {
			return err
		}
		tr := resp.ProtocolResponse()

		if tr.StatusCode != http.StatusOK {
			return errors.New("bad response code")
		}

		bytes, err := io.ReadAll(tr.Body)
		if err != nil {
			return err
		}

		if string(bytes) != `{"status":"ok"}`+"\n" {
			return errors.New("bad response body")
		}

		if resp.Status != "ok" {
			return errors.New("bad response unmarshal")
		}

		h := w.Header()
		h.Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_, ignoredErr := w.Write([]byte(`{"status":"ok"}` + "\n"))
		_ = ignoredErr

		return nil
	})

	r.SetHandler()

	srv := &http.Server{
		Addr:              hostPortToAddr(host, port),
		ReadTimeout:       2 * time.Second,
		ReadHeaderTimeout: 0,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       0,
		MaxHeaderBytes:    10 * 1024 * 1024,
		Handler:           r,
	}

	if err := listenAndServe(ctx, srv); err != nil {
		panic(err)
	}
}

func otelDisabled() bool {
	if s := os.Getenv("OTEL_DISABLED"); s != "" {
		if b, err := strconv.ParseBool(s); err == nil {
			return b
		}
	}

	return false
}

const unixSocketUriPrefix = "unix://"

func hostPortToAddr(host string, port int) string {
	if strings.HasPrefix(host, unixSocketUriPrefix) {
		return host
	}

	return net.JoinHostPort(host, strconv.Itoa(port))
}

func listenAndServe(ctx context.Context, srv *http.Server) error {
	// shutdownTimeout must be greater than the go default socket keepalive timeout of 15 seconds
	const shutdownTimeout = 24 * time.Second
	pctx := ctx

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errChan := make(chan error, 2)

	if err := ctx.Err(); err != nil {
		return err
	}

	slog.Warn(
		"starting server",
		"addr", srv.Addr,
		"shutdown-timeout", shutdownTimeout,
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		defer func() {
			slog.Warn(
				"shutting down server",
				"addr", srv.Addr,
				"shutdown-timeout", shutdownTimeout,
			)
		}()

		addr := srv.Addr

		listenCfg := net.ListenConfig{
			KeepAlive: time.Second * 15, // TODO: error if shutdown timeout is less than or equal to KeepAlive
		}

		var network string
		if strings.HasPrefix(addr, unixSocketUriPrefix) {
			network = "unix"
			addr = strings.TrimPrefix(addr, unixSocketUriPrefix)
		} else {
			network = "tcp"
		}

		listener, err := listenCfg.Listen(ctx, network, addr)
		if err != nil {
			errChan <- err
			return
		}

		if err := srv.Serve(listener); err != nil && (!errors.Is(err, http.ErrServerClosed) || pctx.Err() == nil) {
			errChan <- err
		}
	}()

	// wait for server routine to exit prematurely or for the parent context to error
	<-ctx.Done()

	// signal server shutdown and wait for server routine to exit; then write error response to errChan
	{
		sctx, sc := context.WithTimeout(context.Background(), shutdownTimeout)
		defer sc()

		err := srv.Shutdown(sctx)
		wg.Wait()
		errChan <- err
	}

	return <-errChan
}
