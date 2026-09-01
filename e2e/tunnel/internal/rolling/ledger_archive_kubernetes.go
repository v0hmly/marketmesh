package rolling

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	ledgerPort                 = 9443
	maximumPortForwardOutput   = 64 * 1024
	portForwardShutdownTimeout = 5 * time.Second
	fakeInternalServerName     = "mm29-fake-internal.marketmesh-e2e-tunnel.svc"
)

var portForwardReadyPattern = regexp.MustCompile(
	`(?m)^Forwarding from 127\.0\.0\.1:([0-9]{4,5}) -> 9443\r?$`,
)

type kubernetesArchiveRuntime struct {
	runID   string
	kubectl kubectlRunner
	guard   *kubernetes
}

func newLedgerArchiveRuntime(
	config LedgerArchiveConfig,
) (archiveRuntime, []Cluster, error) {
	if err := validateRunID(config.RunID); err != nil {
		return nil, nil, err
	}
	if len(config.Clusters) != 2 {
		return nil, nil, errors.New("rolling: ledger archive requires two clusters")
	}
	clusters := slices.Clone(config.Clusters)
	for index, cluster := range clusters {
		if err := validateClusterHandoff(cluster); err != nil {
			return nil, nil, err
		}
		if cluster.Zone != "internal" {
			return nil, nil, errors.New("rolling: ledger archive cluster must be internal")
		}
		if err := validateContext(cluster.Context); err != nil {
			return nil, nil, err
		}
		absolute, err := regularAbsolutePath(cluster.Kubeconfig)
		if err != nil {
			return nil, nil, errors.New("rolling: ledger kubeconfig is not a regular file")
		}
		cluster.Kubeconfig = absolute
		clusters[index] = cluster
	}
	slices.SortFunc(clusters, func(left, right Cluster) int {
		return compareString(left.DC, right.DC)
	})
	if clusters[0].DC != "dc-a" || clusters[1].DC != "dc-b" {
		return nil, nil, errors.New("rolling: ledger archive requires dc-a and dc-b")
	}
	path, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, nil, errors.New("rolling: kubectl is required for ledger archive")
	}
	runner := kubectlRunner{path: path}
	clusterMap := make(map[string]Cluster, len(clusters))
	for _, cluster := range clusters {
		clusterMap[cluster.DC+"/"+cluster.Zone] = cluster
	}
	guard := &kubernetes{
		config:   KubernetesConfig{RunID: config.RunID},
		clusters: clusterMap,
		kubectl:  runner,
	}

	return &kubernetesArchiveRuntime{
		runID: config.RunID, kubectl: runner, guard: guard,
	}, clusters, nil
}

func (runtime *kubernetesArchiveRuntime) ListPods(
	ctx context.Context,
	cluster Cluster,
) ([]archivePod, error) {
	if ctx == nil {
		return nil, errors.New("rolling: pod list context must not be nil")
	}
	if _, err := runtime.guard.clusterIdentity(ctx, cluster); err != nil {
		return nil, err
	}
	if err := runtime.guard.verifyTopologyOwnership(ctx, cluster); err != nil {
		return nil, err
	}
	if err := runtime.guard.verifyNamespaceAndOwner(ctx, cluster); err != nil {
		return nil, err
	}
	selector := "app.kubernetes.io/name=fake-internal,marketmesh.io/run-id=" + runtime.runID
	output, err := runtime.run(
		ctx,
		cluster,
		nil,
		"get",
		"pods",
		"--namespace="+Namespace,
		"--selector="+selector,
		"--output=json",
	)
	if err != nil {
		return nil, errors.New("rolling: listing fake internal Pods")
	}
	var list podList
	if err := json.Unmarshal(output, &list); err != nil {
		return nil, errors.New("rolling: decoding fake internal Pods")
	}
	if list.APIVersion != "v1" || list.Kind != "List" {
		return nil, errors.New("rolling: unexpected fake internal Pod list identity")
	}
	pods := make([]archivePod, 0, len(list.Items))
	seen := make(map[string]struct{}, len(list.Items))
	for _, item := range list.Items {
		pod, err := runtime.validatePod(cluster, item)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[pod.Name]; exists {
			return nil, errors.New("rolling: duplicate fake internal Pod")
		}
		seen[pod.Name] = struct{}{}
		pods = append(pods, pod)
	}
	slices.SortFunc(pods, func(left, right archivePod) int {
		return compareString(left.Name, right.Name)
	})

	return pods, nil
}

func (runtime *kubernetesArchiveRuntime) Open(
	ctx context.Context,
	cluster Cluster,
	pod archivePod,
) (archiveConnection, error) {
	if ctx == nil {
		return nil, errors.New("rolling: ledger open context must not be nil")
	}
	if err := runtime.revalidatePodOwnership(ctx, cluster, pod); err != nil {
		return nil, err
	}
	tlsConfig, err := runtime.readClientTLS(ctx, cluster)
	if err != nil {
		return nil, err
	}
	forward, err := startPortForward(
		ctx,
		runtime.kubectl.path,
		cluster,
		pod.Name,
	)
	if err != nil {
		return nil, err
	}
	if err := runtime.revalidatePodOwnership(ctx, cluster, pod); err != nil {
		_ = forward.Close()
		return nil, err
	}
	connection, err := grpcgo.NewClient(
		"passthrough:///"+forward.address,
		grpcgo.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpcgo.WithDisableRetry(),
		grpcgo.WithDefaultCallOptions(
			grpcgo.MaxCallRecvMsgSize(4<<20),
			grpcgo.MaxCallSendMsgSize(4<<20),
		),
	)
	if err != nil {
		_ = forward.Close()
		return nil, errors.New("rolling: creating direct ledger client")
	}

	return &kubernetesLedgerConnection{
		client:     e2ev1.NewFakeInternalServiceClient(connection),
		connection: connection,
		forward:    forward,
	}, nil
}

func (runtime *kubernetesArchiveRuntime) validatePod(
	cluster Cluster,
	pod podObject,
) (archivePod, error) {
	labels := pod.Metadata.Labels
	if pod.APIVersion != "v1" || pod.Kind != "Pod" ||
		pod.Metadata.Namespace != Namespace ||
		!strings.HasPrefix(pod.Metadata.Name, FakeInternalDeployment+"-") ||
		!revisionPattern.MatchString(pod.Metadata.Name) ||
		!isSafeUID(pod.Metadata.UID) ||
		labels["app.kubernetes.io/name"] != "fake-internal" ||
		labels["app.kubernetes.io/managed-by"] != "marketmesh-e2e-tunnel" ||
		labels["marketmesh.io/task"] != "MM-29" ||
		labels["marketmesh.io/run-id"] != runtime.runID ||
		labels["marketmesh.io/dc"] != cluster.DC ||
		labels["marketmesh.io/zone"] != "internal" {
		return archivePod{}, errors.New("rolling: refusing foreign fake internal Pod")
	}
	controllerCount := 0
	for _, owner := range pod.Metadata.OwnerReferences {
		if owner.Controller {
			controllerCount++
			if owner.APIVersion != "apps/v1" || owner.Kind != "ReplicaSet" ||
				!strings.HasPrefix(owner.Name, FakeInternalDeployment+"-") ||
				!isSafeUID(owner.UID) {
				return archivePod{}, errors.New("rolling: fake internal Pod owner is invalid")
			}
		}
	}
	if controllerCount != 1 {
		return archivePod{}, errors.New("rolling: fake internal Pod must have one controller")
	}
	controller := ownerReference{}
	for _, owner := range pod.Metadata.OwnerReferences {
		if owner.Controller {
			controller = owner
			break
		}
	}
	if pod.Status.Phase != "Pending" && pod.Status.Phase != "Running" {
		return archivePod{}, errors.New("rolling: fake internal Pod phase is invalid")
	}
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == "Ready" {
			ready = condition.Status == "True"
		}
	}
	if pod.Status.Phase == "Running" {
		if len(pod.Status.ContainerStatuses) != 1 ||
			pod.Status.ContainerStatuses[0].Name != "fake-internal" ||
			pod.Status.ContainerStatuses[0].Ready != ready {
			return archivePod{}, errors.New("rolling: fake internal container status is invalid")
		}
	}

	return archivePod{
		Name: pod.Metadata.Name, UID: pod.Metadata.UID,
		OwnerName: controller.Name, OwnerUID: controller.UID,
		Running: pod.Status.Phase == "Running", Ready: ready,
		Terminating: pod.Metadata.DeletionTimestamp != "",
	}, nil
}

func (runtime *kubernetesArchiveRuntime) revalidatePodOwnership(
	ctx context.Context,
	cluster Cluster,
	expected archivePod,
) error {
	output, err := runtime.run(
		ctx,
		cluster,
		nil,
		"get",
		"pod",
		expected.Name,
		"--namespace="+Namespace,
		"--output=json",
	)
	if err != nil {
		return errors.New("rolling: revalidating direct ledger Pod")
	}
	var object podObject
	if err := json.Unmarshal(output, &object); err != nil {
		return errors.New("rolling: decoding revalidated direct ledger Pod")
	}
	actual, err := runtime.validatePod(cluster, object)
	if err != nil {
		return err
	}
	if actual.Name != expected.Name || actual.UID != expected.UID ||
		actual.OwnerName != expected.OwnerName || actual.OwnerUID != expected.OwnerUID ||
		!actual.Running {
		return errors.New("rolling: direct ledger Pod changed before connection")
	}

	target, found := targetFor(cluster.DC, ComponentFakeInternal)
	if !found {
		return errors.New("rolling: direct ledger target is missing")
	}
	deployment, err := runtime.guard.readDeployment(ctx, target)
	if err != nil {
		return err
	}
	if err := runtime.guard.validateDeployment(target, deployment); err != nil {
		return err
	}
	output, err = runtime.run(
		ctx,
		cluster,
		nil,
		"get",
		"replicaset",
		expected.OwnerName,
		"--namespace="+Namespace,
		"--output=json",
	)
	if err != nil {
		return errors.New("rolling: reading direct ledger ReplicaSet")
	}
	var replicaSet replicaSetObject
	if err := json.Unmarshal(output, &replicaSet); err != nil {
		return errors.New("rolling: decoding direct ledger ReplicaSet")
	}
	if err := runtime.validateReplicaSet(cluster, expected, deployment, replicaSet); err != nil {
		return err
	}

	return nil
}

func (runtime *kubernetesArchiveRuntime) validateReplicaSet(
	cluster Cluster,
	pod archivePod,
	deployment deploymentObject,
	replicaSet replicaSetObject,
) error {
	labels := replicaSet.Metadata.Labels
	if replicaSet.APIVersion != "apps/v1" || replicaSet.Kind != "ReplicaSet" ||
		replicaSet.Metadata.Name != pod.OwnerName ||
		replicaSet.Metadata.Namespace != Namespace ||
		replicaSet.Metadata.UID != pod.OwnerUID ||
		labels["app.kubernetes.io/name"] != "fake-internal" ||
		labels["app.kubernetes.io/managed-by"] != "marketmesh-e2e-tunnel" ||
		labels["marketmesh.io/task"] != "MM-29" ||
		labels["marketmesh.io/run-id"] != runtime.runID ||
		labels["marketmesh.io/dc"] != cluster.DC ||
		labels["marketmesh.io/zone"] != "internal" {
		return errors.New("rolling: refusing foreign direct ledger ReplicaSet")
	}
	controllerCount := 0
	for _, owner := range replicaSet.Metadata.OwnerReferences {
		if !owner.Controller {
			continue
		}
		controllerCount++
		if owner.APIVersion != "apps/v1" || owner.Kind != "Deployment" ||
			owner.Name != FakeInternalDeployment ||
			owner.UID != deployment.Metadata.UID {
			return errors.New("rolling: direct ledger ReplicaSet owner is invalid")
		}
	}
	if controllerCount != 1 {
		return errors.New("rolling: direct ledger ReplicaSet must have one controller")
	}

	return nil
}

func (runtime *kubernetesArchiveRuntime) readClientTLS(
	ctx context.Context,
	cluster Cluster,
) (*tls.Config, error) {
	output, err := runtime.run(
		ctx,
		cluster,
		nil,
		"get",
		"secret",
		workload.GatewayOutInternalTLSSecret,
		"--namespace="+Namespace,
		"--output=json",
	)
	if err != nil {
		return nil, errors.New("rolling: reading direct ledger TLS material")
	}
	var secret secretObject
	defer clear(output)
	if err := json.Unmarshal(output, &secret); err != nil {
		return nil, errors.New("rolling: decoding direct ledger TLS material")
	}
	labels := secret.Metadata.Labels
	if secret.APIVersion != "v1" || secret.Kind != "Secret" ||
		secret.Metadata.Name != workload.GatewayOutInternalTLSSecret ||
		secret.Metadata.Namespace != Namespace || secret.Type != "Opaque" ||
		labels["app.kubernetes.io/managed-by"] != "marketmesh-e2e-tunnel" ||
		labels["marketmesh.io/task"] != "MM-29" ||
		labels["marketmesh.io/run-id"] != runtime.runID ||
		labels["marketmesh.io/dc"] != cluster.DC ||
		labels["marketmesh.io/zone"] != "internal" ||
		len(secret.Data) != 3 {
		return nil, errors.New("rolling: refusing foreign direct ledger TLS material")
	}
	certificatePEM := secret.Data["tls.crt"]
	privateKeyPEM := secret.Data["tls.key"]
	caPEM := secret.Data["ca.crt"]
	defer func() {
		clear(certificatePEM)
		clear(privateKeyPEM)
		clear(caPEM)
	}()
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil || len(certificate.Certificate) != 1 {
		return nil, errors.New("rolling: parsing direct ledger client key pair")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, errors.New("rolling: parsing direct ledger client certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("rolling: parsing direct ledger root CA")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, errors.New("rolling: verifying direct ledger client certificate")
	}
	expectedClient := workloadIdentity(runtime.runID, cluster.DC, "gateway-out")
	if !hasExactURI(leaf, expectedClient) {
		return nil, errors.New("rolling: direct ledger client identity mismatch")
	}
	expectedServer := workloadIdentity(runtime.runID, cluster.DC, "fake-internal")

	return &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: fakeInternalServerName,
		RootCAs: roots, Certificates: []tls.Certificate{certificate},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 ||
				!hasExactURI(state.VerifiedChains[0][0], expectedServer) {
				return errors.New("rolling: direct ledger server identity mismatch")
			}
			return nil
		},
	}, nil
}

func (runtime *kubernetesArchiveRuntime) run(
	ctx context.Context,
	cluster Cluster,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	base := []string{"--kubeconfig=" + cluster.Kubeconfig, "--context=" + cluster.Context}
	return runtime.kubectl.Run(ctx, stdin, append(base, arguments...)...)
}

type kubernetesLedgerConnection struct {
	client     e2ev1.FakeInternalServiceClient
	connection *grpcgo.ClientConn
	forward    *portForwardProcess
	closeOnce  sync.Once
	closeErr   error
}

func (connection *kubernetesLedgerConnection) Ledger(
	ctx context.Context,
	request *e2ev1.LedgerRequest,
	options ...grpcgo.CallOption,
) (*e2ev1.LedgerResponse, error) {
	return connection.client.Ledger(ctx, request, options...)
}

func (connection *kubernetesLedgerConnection) Close() error {
	connection.closeOnce.Do(func() {
		connection.closeErr = errors.Join(
			connection.connection.Close(),
			connection.forward.Close(),
		)
	})
	return connection.closeErr
}

type portForwardProcess struct {
	address   string
	cancel    context.CancelFunc
	done      <-chan error
	closeOnce sync.Once
	closeErr  error
}

func startPortForward(
	ctx context.Context,
	kubectlPath string,
	cluster Cluster,
	podName string,
) (*portForwardProcess, error) {
	if ctx == nil {
		return nil, errors.New("rolling: port-forward context must not be nil")
	}
	processCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	output := newPortForwardOutput(cancel)
	command := portForwardCommand(
		processCtx,
		kubectlPath,
		cluster,
		podName,
		output,
	)
	if err := command.Start(); err != nil {
		cancel()
		return nil, errors.New("rolling: starting direct ledger port-forward")
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()

	select {
	case <-ctx.Done():
		cancel()
		_ = waitProcessStop(done)
		return nil, errors.Join(errors.New("rolling: waiting for direct ledger port-forward"), ctx.Err())
	case err := <-done:
		cancel()
		return nil, errors.Join(errors.New("rolling: direct ledger port-forward exited"), err)
	case port := <-output.ready:
		return &portForwardProcess{
			address: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			cancel:  cancel, done: done,
		}, nil
	}
}

func portForwardCommand(
	ctx context.Context,
	kubectlPath string,
	cluster Cluster,
	podName string,
	output io.Writer,
) *exec.Cmd {
	// #nosec G204 -- kubectlPath comes from exec.LookPath; kubeconfig/context,
	// Pod name/UID and the complete ownership chain are validated before use.
	command := exec.CommandContext(
		ctx,
		kubectlPath,
		"--kubeconfig="+cluster.Kubeconfig,
		"--context="+cluster.Context,
		"port-forward",
		"--namespace="+Namespace,
		"--address=127.0.0.1",
		"pod/"+podName,
		":"+strconv.Itoa(ledgerPort),
	)
	command.Env = []string{"KUBECONFIG="}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.Stdout = output
	command.Stderr = output

	return command
}

func (process *portForwardProcess) Close() error {
	process.closeOnce.Do(func() {
		process.cancel()
		process.closeErr = waitProcessStop(process.done)
	})
	return process.closeErr
}

func waitProcessStop(done <-chan error) error {
	timer := time.NewTimer(portForwardShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.New("rolling: direct ledger port-forward stop timeout")
	}
}

type portForwardOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	ready     chan int
	readyOnce sync.Once
	cancel    context.CancelFunc
	invalid   bool
}

func newPortForwardOutput(cancel context.CancelFunc) *portForwardOutput {
	return &portForwardOutput{ready: make(chan int, 1), cancel: cancel}
}

func (output *portForwardOutput) Write(content []byte) (int, error) {
	original := len(content)
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.invalid {
		return original, nil
	}
	if output.buffer.Len()+len(content) > maximumPortForwardOutput {
		output.invalid = true
		output.cancel()
		return original, nil
	}
	_, _ = output.buffer.Write(content)
	match := portForwardReadyPattern.FindSubmatch(output.buffer.Bytes())
	if len(match) != 2 {
		return original, nil
	}
	port, err := strconv.Atoi(string(match[1]))
	if err != nil || port < 1024 || port > 65535 {
		output.invalid = true
		output.cancel()
		return original, nil
	}
	output.readyOnce.Do(func() {
		output.ready <- port
	})

	return original, nil
}

func workloadIdentity(runID string, dataCenter string, workloadName string) string {
	return "spiffe://marketmesh.test/e2e/" + runID + "/" + dataCenter + "/" + workloadName
}

func hasExactURI(certificate *x509.Certificate, expected string) bool {
	if certificate == nil || len(certificate.URIs) != 1 || certificate.URIs[0] == nil {
		return false
	}
	parsed, err := url.Parse(expected)
	if err != nil {
		return false
	}
	return certificate.URIs[0].String() == parsed.String()
}

type podList struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Items      []podObject `json:"items"`
}

type podObject struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   podMetadata `json:"metadata"`
	Status     podStatus   `json:"status"`
}

type podMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	Labels            map[string]string `json:"labels"`
	OwnerReferences   []ownerReference  `json:"ownerReferences"`
	DeletionTimestamp string            `json:"deletionTimestamp,omitempty"`
}

type ownerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller bool   `json:"controller"`
}

type podStatus struct {
	Phase             string            `json:"phase"`
	Conditions        []podCondition    `json:"conditions"`
	ContainerStatuses []containerStatus `json:"containerStatuses"`
}

type podCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type containerStatus struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}

type secretObject struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   objectMetadata    `json:"metadata"`
	Type       string            `json:"type"`
	Data       map[string][]byte `json:"data"`
}

type replicaSetObject struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   podMetadata `json:"metadata"`
}

var _ io.Writer = (*portForwardOutput)(nil)
var _ archiveConnection = (*kubernetesLedgerConnection)(nil)
var _ probe.InstanceResolver = (*LedgerArchive)(nil)
