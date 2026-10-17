/*
Copyright 2026 Herman Hoffman.

Licensed under the MIT License. See LICENSE in the project root.
*/

package controller

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tenancyv1alpha1 "github.com/HOFFMAN-ALTERNATIVE/k8s-operator/api/v1alpha1"
)

var (
	k8s       client.Client
	clientset *kubernetes.Clientset
	testCtx   context.Context
)

// TestMain boots a real kube-apiserver + etcd (envtest), installs the CRD
// from config/crd/bases — the same file that ships — and runs the reconciler
// in a manager exactly as in production.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	logf.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(io.Discard)))
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if dirs, _ := filepath.Glob(filepath.Join("..", "..", "bin", "k8s", "k8s", "*")); len(dirs) > 0 {
			env.BinaryAssetsDirectory = dirs[len(dirs)-1]
		}
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest start:", err, "(run `make envtest` or set KUBEBUILDER_ASSETS)")
		return 1
	}
	defer env.Stop() //nolint:errcheck // best effort

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(scheme))

	k8s, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	clientset = kubernetes.NewForConfigOrDie(cfg)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := (&TenantReconciler{
		Client: mgr.GetClient(), Scheme: scheme,
		Recorder:           mgr.GetEventRecorder("tenant-operator"),
		TerminatingRequeue: 200 * time.Millisecond,
	}).SetupWithManager(mgr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	testCtx = ctx
	go func() {
		if err := mgr.Start(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "manager:", err)
		}
	}()
	return m.Run()
}

// eventually polls fn until it returns nil or the timeout expires.
func eventually(t *testing.T, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = fn(); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("condition not met within timeout: %v", err)
}

// consistently asserts fn keeps returning nil for d.
func consistently(t *testing.T, d time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := fn(); err != nil {
			t.Fatalf("condition broke: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
