/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"os"
	"time"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"

	uiapiv1alpha1 "github.com/example/chat-ui/api/v1alpha1"
	uictrl "github.com/example/chat-ui/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// version is set at build time via: -ldflags "-X main.version=<version>"
var version string

const (
	defaultAppName   = "chat-ui"
	defaultPartOf    = "chat-ui"
	defaultManagedBy = "chat-ui-operator"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(uiapiv1alpha1.AddToScheme(scheme))
}

func parseJSONMap(s string) map[string]string {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		default:
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			out[k] = string(b)
		}
	}
	return out
}

type labelingClient struct {
	client.Client
	baseLabels map[string]string
}

func newLabelingClient(delegate client.Client, base map[string]string) client.Client {
	return &labelingClient{Client: delegate, baseLabels: base}
}

func (c *labelingClient) ensureLabels(obj client.Object) {
	if obj == nil || c == nil || len(c.baseLabels) == 0 {
		return
	}
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range c.baseLabels {
		if v == "" {
			continue
		}
		if _, exists := labels[k]; !exists {
			labels[k] = v
		}
	}
	obj.SetLabels(labels)
}

func (c *labelingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.ensureLabels(obj)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *labelingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.ensureLabels(obj)
	return c.Client.Update(ctx, obj, opts...)
}

func (c *labelingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.ensureLabels(obj)
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var publicSchemeFlag string
	var ingressExtraAnnotationsJSON string
	var tlsSecretName string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false, "If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false, "If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&publicSchemeFlag, "public-scheme", "", "Public URL scheme for status.url (overrides PUBLIC_SCHEME env)")
	flag.StringVar(&ingressExtraAnnotationsJSON, "ingress-extra-annotations", "", "JSON map of extra Ingress annotations to merge")
	flag.StringVar(&tlsSecretName, "tls-secret-name", "", "Secret name for Ingress TLS (optional)")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if shutdown, err := initOpenTelemetry(context.Background()); err != nil {
		setupLog.Error(err, "opentelemetry init failed")
	} else if shutdown != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = shutdown(ctx)
			cancel()
		}()
	}

	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}
	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{TLSOpts: tlsOpts})
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "6e1e9f7b.example.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	baseLabels := map[string]string{
		"app.kubernetes.io/name":       defaultAppName,
		"app.kubernetes.io/part-of":    defaultPartOf,
		"app.kubernetes.io/managed-by": defaultManagedBy,
	}
	if version != "" {
		baseLabels["app.kubernetes.io/version"] = version
	}
	labeledClient := newLabelingClient(mgr.GetClient(), baseLabels)

	if err = (&uictrl.ChatUIInstanceReconciler{
		Client: labeledClient,
		Scheme: mgr.GetScheme(),
		PublicHost: func() string {
			if v := os.Getenv("PUBLIC_HOST"); v != "" {
				return v
			}
			return "localhost"
		}(),
		PublicScheme: func() string {
			if publicSchemeFlag != "" {
				return publicSchemeFlag
			}
			if v := os.Getenv("PUBLIC_SCHEME"); v != "" {
				return v
			}
			return "http"
		}(),
		ExtraIngressAnnotations: func() map[string]string {
			if ingressExtraAnnotationsJSON != "" {
				if m := parseJSONMap(ingressExtraAnnotationsJSON); len(m) > 0 {
					return m
				}
			}
			if v := os.Getenv("INGRESS_EXTRA_ANNOTATIONS"); v != "" {
				if m := parseJSONMap(v); len(m) > 0 {
					return m
				}
			}
			return nil
		}(),
		TLSSecretName: func() string {
			if tlsSecretName != "" {
				return tlsSecretName
			}
			return os.Getenv("TLS_SECRET_NAME")
		}(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ChatUIInstance")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initOpenTelemetry(ctx context.Context) (func(context.Context) error, error) {
	var (
		exp tracesdk.SpanExporter
		err error
	)
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
	} else {
		exp, err = otlptracehttp.New(ctx)
	}
	if err != nil {
		return nil, err
	}
	res, rerr := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", "chat-ui-operator"),
		),
	)
	if rerr != nil {
		res = resource.Empty()
	}
	tp := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(exp),
		tracesdk.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}
