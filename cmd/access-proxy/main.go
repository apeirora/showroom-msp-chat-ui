package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accessgate"
	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
)

func main() {
	path := flag.String("config", "", "Required operator-generated access configuration")
	flag.Parse()
	if err := run(*path); err != nil {
		fmt.Fprintln(os.Stderr, "Chat UI access proxy failed:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	if path == "" {
		return errors.New("--config is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("access configuration cannot be opened")
	}
	defer func() { _ = f.Close() }()
	var config accessgate.Config
	if err := accesspolicy.Decode(f, &config); err != nil {
		return err
	}
	gate, err := accessgate.New(config)
	if err != nil {
		return err
	}
	// Never expose the authorization proxy on the pod's network interface.
	proxy := &http.Server{Addr: "127.0.0.1:8082", Handler: gate,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 65 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 96 << 10}
	health := &http.Server{Addr: "0.0.0.0:8083", Handler: http.HandlerFunc(gate.Health),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- proxy.ListenAndServe() }()
	go func() { errorsCh <- health.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err = <-errorsCh:
	}
	_ = proxy.Close()
	_ = health.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
