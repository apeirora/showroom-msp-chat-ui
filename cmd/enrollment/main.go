// chat-ui-enrollment is an opt-in platform process. The default is one read-only
// plan; writes require --apply. It is never run by a tenant workload.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/apeirora/showroom-msp-chat-ui/internal/accesspolicy"
	"github.com/apeirora/showroom-msp-chat-ui/internal/enrollment"
	"github.com/apeirora/showroom-msp-chat-ui/internal/enrollmentkube"
	"github.com/apeirora/showroom-msp-chat-ui/internal/hostnamepolicy"
)

type configuration struct {
	AccessPolicy       json.RawMessage           `json:"accessPolicy"`
	HostnamePolicy     json.RawMessage           `json:"hostnamePolicy"`
	PublicHost         string                    `json:"publicHost"`
	MSPServer          string                    `json:"mspServer"`
	MSPClusterUID      string                    `json:"mspClusterUID"`
	PlatformServer     string                    `json:"platformServer"`
	PlatformClusterUID string                    `json:"platformClusterUID"`
	Clients            []enrollment.ClientConfig `json:"clients"`
}

type options struct {
	config, kcpConfig, mspConfig, platformConfig, healthAddress string
	probeOffset                                                 int
	platformInCluster, apply, once                              bool
	interval                                                    time.Duration
}

func readConfiguration(path string) (configuration, enrollment.Config, error) {
	var file configuration
	reader, err := os.Open(path)
	if err != nil {
		return file, enrollment.Config{}, errors.New("enrollment configuration unavailable")
	}
	defer func() { _ = reader.Close() }()
	if err := accesspolicy.Decode(reader, &file); err != nil {
		return file, enrollment.Config{}, err
	}
	policy, err := accesspolicy.Parse(bytes.NewReader(file.AccessPolicy))
	if err != nil {
		return file, enrollment.Config{}, err
	}
	if policy.Enrollment == nil {
		return file, enrollment.Config{}, errors.New("automatic enrollment is not configured")
	}
	hosts, err := hostnamepolicy.Parse(bytes.NewReader(file.HostnamePolicy))
	if err != nil {
		return file, enrollment.Config{}, errors.New("enrollment hostname policy invalid")
	}
	if hosts.ValidatePublicHost(file.PublicHost) != nil ||
		accesspolicy.HTTPSURL(file.MSPServer, true) != nil || accesspolicy.HTTPSURL(file.PlatformServer, true) != nil ||
		file.MSPClusterUID == "" || file.PlatformClusterUID == "" {
		return file, enrollment.Config{}, errors.New("explicit public host and cluster identities required")
	}
	return file, enrollment.Config{
		Policy: policy, Hostnames: hosts, PublicHost: file.PublicHost, Clients: file.Clients,
	}, nil
}

func reconcile(ctx context.Context, opts options) (enrollment.Result, error) {
	var result enrollment.Result
	file, config, err := readConfiguration(opts.config)
	if err != nil {
		return result, &passError{stage: "configuration", cause: err}
	}
	kcp, err := enrollmentkube.Config(opts.kcpConfig, config.Policy.KCPServer, false)
	if err != nil {
		return result, &passError{stage: "credential_configuration", cause: err}
	}
	mspConfig, err := enrollmentkube.Config(opts.mspConfig, file.MSPServer, false)
	if err != nil {
		return result, &passError{stage: "credential_configuration", cause: err}
	}
	msp, err := enrollmentkube.NewDynamic(mspConfig)
	if err != nil {
		return result, &passError{stage: "api_transport", cause: err}
	}
	platformConfig, err := enrollmentkube.Config(opts.platformConfig, file.PlatformServer, opts.platformInCluster)
	if err != nil {
		return result, &passError{stage: "credential_configuration", cause: err}
	}
	platform, err := enrollmentkube.NewDynamic(platformConfig)
	if err != nil {
		return result, &passError{stage: "api_transport", cause: err}
	}
	if err := enrollmentkube.VerifyCluster(ctx, msp, file.MSPClusterUID); err != nil {
		return result, &passError{stage: "cluster_identity", cause: err}
	}
	if err := enrollmentkube.VerifyCluster(ctx, platform, file.PlatformClusterUID); err != nil {
		return result, &passError{stage: "cluster_identity", cause: err}
	}
	engine := enrollment.Engine{
		Config:  config,
		Sources: enrollmentkube.Sources{Config: kcp},
		Managed: enrollmentkube.Managed{API: msp},
		Store: enrollmentkube.Store{API: msp, Namespace: config.Policy.CredentialsNamespace,
			Name: config.Policy.Enrollment.ConfigMapName},
		Registry:    enrollmentkube.Registry{API: platform},
		Verifier:    enrollment.NewHTTPVerifier(nil, 10*time.Second),
		DryRun:      !opts.apply,
		ProbeOffset: opts.probeOffset,
	}
	result, err = engine.Reconcile(ctx)
	if err != nil {
		return result, &passError{stage: "enrollment", cause: err}
	}
	return result, nil
}

// passError exposes only a fixed stage; API responses may contain private data.
type passError struct {
	stage string
	cause error
}

func (e *passError) Error() string { return "enrollment failed during " + e.stage }
func (e *passError) Unwrap() error { return e.cause }
func failureStage(err error) string {
	if err == nil {
		return ""
	}
	var failure *passError
	if errors.As(err, &failure) {
		return failure.stage
	}
	return "unknown"
}

func healthHandler(ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func run(ctx context.Context, opts options) error {
	if opts.config == "" || opts.kcpConfig == "" || opts.mspConfig == "" ||
		opts.interval < 5*time.Second || opts.interval > 5*time.Minute ||
		(opts.platformInCluster && opts.platformConfig != "") || (!opts.platformInCluster && opts.platformConfig == "") {
		return errors.New("explicit configuration, credential sources and interval between 5s and 5m required")
	}
	var ready atomic.Bool
	if !opts.once && opts.apply && opts.healthAddress != "" {
		listener, err := net.Listen("tcp", opts.healthAddress)
		if err != nil {
			return errors.New("enrollment health listener unavailable")
		}
		server := &http.Server{Handler: healthHandler(&ready), ReadHeaderTimeout: 5 * time.Second}
		defer func() { _ = server.Close() }()
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "enrollment health listener unavailable")
			}
		}()
	}
	for {
		pass, cancel := context.WithTimeout(ctx, 90*time.Second)
		result, err := reconcile(pass, opts)
		cancel()
		opts.probeOffset += enrollment.ProbeBatchSize
		ready.Store(err == nil)
		output := struct {
			At           string            `json:"at"`
			Apply        bool              `json:"apply"`
			Success      bool              `json:"success"`
			Result       enrollment.Result `json:"result"`
			FailureStage string            `json:"failureStage,omitempty"`
		}{time.Now().UTC().Format(time.RFC3339), opts.apply, err == nil, result, failureStage(err)}
		if encodeErr := json.NewEncoder(os.Stdout).Encode(output); encodeErr != nil {
			return encodeErr
		}
		if opts.once || !opts.apply {
			return err
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "enrollment pass incomplete; no new access is reported as accepted")
		}
		timer := time.NewTimer(opts.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func main() {
	var opts options
	flag.StringVar(&opts.config, "config", "", "Explicit non-secret enrollment configuration")
	flag.StringVar(&opts.kcpConfig, "kcp-kubeconfig", "", "Scoped KCP reader kubeconfig")
	flag.StringVar(&opts.mspConfig, "msp-kubeconfig", "", "Scoped MSP reader/enrollment writer kubeconfig")
	flag.StringVar(&opts.platformConfig, "platform-kubeconfig", "",
		"Scoped platform kubeconfig; exclusive with in-cluster")
	flag.BoolVar(&opts.platformInCluster, "platform-in-cluster", false, "Use the platform Pod service account")
	flag.BoolVar(&opts.apply, "apply", false, "Apply registration and mapping changes; otherwise plan without writes")
	flag.BoolVar(&opts.once, "once", false, "Perform only one reconciliation")
	flag.DurationVar(&opts.interval, "interval", 30*time.Second, "Interval between reconciliation passes")
	flag.StringVar(&opts.healthAddress, "health-address", "", "Optional fixed health endpoints for continuous mode")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, opts); err != nil {
		// Kubeconfig/API error payloads are not printed: a transport can include
		// information outside the deliberately bounded operational result.
		fmt.Fprintln(os.Stderr, "enrollment incomplete; verify configuration, target access and ownership")
		os.Exit(1)
	}
}
