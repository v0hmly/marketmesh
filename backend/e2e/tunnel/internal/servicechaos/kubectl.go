package servicechaos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
)

func (manager *Manager) scale(ctx context.Context, cluster Cluster, replicas int) error {
	_, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"scale",
		"deployment/"+workload.FakeInternalDeployment,
		"--namespace="+workload.Namespace,
		fmt.Sprintf("--replicas=%d", replicas),
	)
	if err != nil {
		return fmt.Errorf("service chaos: scaling fake-internal in %s", cluster.DC)
	}

	return nil
}

func (manager *Manager) patchServiceSelector(
	ctx context.Context,
	cluster Cluster,
	selector map[string]string,
) error {
	patch, err := json.Marshal([]jsonPatchOperation{{
		Operation: "replace",
		Path:      "/spec/selector",
		Value:     selector,
	}})
	if err != nil {
		return errors.New("service chaos: encoding Service selector patch")
	}
	_, err = manager.runKubectl(
		ctx,
		cluster,
		nil,
		"patch",
		"service/"+workload.FakeInternalService,
		"--namespace="+workload.Namespace,
		"--type=json",
		"--patch="+string(patch),
	)
	if err != nil {
		return fmt.Errorf("service chaos: patching fake-internal Service in %s", cluster.DC)
	}

	return nil
}

func (manager *Manager) deleteService(ctx context.Context, cluster Cluster) error {
	_, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"delete",
		"service/"+workload.FakeInternalService,
		"--namespace="+workload.Namespace,
		"--wait=true",
		"--timeout="+manager.config.Timeout.String(),
	)
	if err != nil {
		return fmt.Errorf("service chaos: deleting fake-internal Service in %s", cluster.DC)
	}

	return nil
}

func (manager *Manager) recreateService(ctx context.Context, cluster Cluster) error {
	selector, err := json.Marshal(manager.expectedSelector())
	if err != nil {
		return errors.New("service chaos: encoding fake-internal Service selector")
	}
	manifest := kubernetesObject{
		APIVersion: "v1",
		Kind:       "Service",
		Metadata: objectMetadata{
			Name:      workload.FakeInternalService,
			Namespace: workload.Namespace,
			Labels: map[string]string{
				nameLabel:                   fakeInternalValue,
				"app.kubernetes.io/part-of": "marketmesh-tunnel-e2e",
				managedByLabel:              managedByValue,
				taskLabel:                   workloadTaskValue,
				dcLabel:                     cluster.DC,
				zoneLabel:                   internalZoneValue,
				runIDLabel:                  manager.config.RunID,
			},
		},
		Spec: objectSpec{
			Selector: selector,
			Ports: []servicePort{{
				Name: "grpc", Port: 9443, TargetPort: "grpc", Protocol: "TCP",
			}},
		},
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return errors.New("service chaos: encoding fake-internal Service")
	}
	_, err = manager.runKubectl(
		ctx,
		cluster,
		content,
		"apply",
		"--filename=-",
		"--field-manager=marketmesh-e2e-service-chaos",
	)
	if err != nil {
		return fmt.Errorf("service chaos: recreating fake-internal Service in %s", cluster.DC)
	}

	return nil
}

func (manager *Manager) expectedSelector() map[string]string {
	return map[string]string{
		nameLabel:  fakeInternalValue,
		runIDLabel: manager.config.RunID,
	}
}

func (manager *Manager) waitDeploymentReady(
	ctx context.Context,
	cluster Cluster,
	deployment string,
) error {
	_, err := manager.runKubectl(
		ctx,
		cluster,
		nil,
		"rollout",
		"status",
		"deployment/"+deployment,
		"--namespace="+workload.Namespace,
		"--timeout="+manager.config.Timeout.String(),
	)
	if err != nil {
		return fmt.Errorf("service chaos: waiting for %s in %s", deployment, cluster.DC)
	}

	return nil
}

func (manager *Manager) waitDeploymentReplicas(
	ctx context.Context,
	cluster Cluster,
	want int32,
) error {
	return manager.poll(ctx, func(runCtx context.Context) (bool, error) {
		object, err := manager.getObject(
			runCtx,
			cluster,
			"deployment/"+workload.FakeInternalDeployment,
		)
		if err != nil {
			return false, err
		}
		if object.Spec.Replicas == nil {
			return false, nil
		}

		return *object.Spec.Replicas == want && object.Status.AvailableReplicas == want, nil
	})
}

func (manager *Manager) waitEndpointsReady(ctx context.Context, cluster Cluster) error {
	err := manager.poll(ctx, func(runCtx context.Context) (bool, error) {
		output, runErr := manager.runKubectl(
			runCtx,
			cluster,
			nil,
			"get",
			"endpoints/"+workload.FakeInternalService,
			"--namespace="+workload.Namespace,
			"--output=json",
		)
		if runErr != nil {
			return false, nil
		}
		var endpoints endpointsObject
		if decodeErr := json.Unmarshal(output, &endpoints); decodeErr != nil {
			return false, errors.New("service chaos: decoding fake-internal endpoints")
		}
		for _, subset := range endpoints.Subsets {
			if len(subset.Addresses) > 0 {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		return fmt.Errorf("service chaos: waiting for ready endpoints in %s: %w", cluster.DC, err)
	}

	return nil
}

func (manager *Manager) waitEndpointsEmpty(ctx context.Context, cluster Cluster) error {
	err := manager.poll(ctx, func(runCtx context.Context) (bool, error) {
		output, runErr := manager.runKubectl(
			runCtx,
			cluster,
			nil,
			"get",
			"endpoints/"+workload.FakeInternalService,
			"--namespace="+workload.Namespace,
			"--output=json",
		)
		if runErr != nil {
			return false, runErr
		}
		var endpoints endpointsObject
		if decodeErr := json.Unmarshal(output, &endpoints); decodeErr != nil {
			return false, errors.New("service chaos: decoding fake-internal endpoints")
		}
		for _, subset := range endpoints.Subsets {
			if len(subset.Addresses) > 0 {
				return false, nil
			}
		}

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("service chaos: waiting for empty endpoints in %s: %w", cluster.DC, err)
	}

	return nil
}

func (manager *Manager) waitServiceAbsent(ctx context.Context, cluster Cluster) error {
	err := manager.poll(ctx, func(runCtx context.Context) (bool, error) {
		output, runErr := manager.runKubectl(
			runCtx,
			cluster,
			nil,
			"get",
			"service/"+workload.FakeInternalService,
			"--namespace="+workload.Namespace,
			"--ignore-not-found=true",
			"--output=name",
		)
		if runErr != nil {
			return false, runErr
		}

		return strings.TrimSpace(string(output)) == "", nil
	})
	if err != nil {
		return fmt.Errorf("service chaos: waiting for absent Service in %s: %w", cluster.DC, err)
	}

	return nil
}

func (manager *Manager) poll(
	ctx context.Context,
	condition func(context.Context) (bool, error),
) error {
	for {
		ready, err := condition(ctx)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		timer := time.NewTimer(manager.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return context.Cause(ctx)
		case <-timer.C:
		}
	}
}

func (manager *Manager) inspect(ctx context.Context, cluster Cluster, fault Fault) error {
	_, _ = fmt.Fprintf(manager.config.Output, "diagnostics: fault=%s dc=%s\n", fault, cluster.DC)
	selector := runIDLabel + "=" + manager.config.RunID
	commands := [][]string{
		{
			"get", "deployment,pod,service,endpoints,endpointslice,configmap",
			"--namespace=" + workload.Namespace, "--selector=" + selector, "--output=wide",
		},
		{"get", "events", "--namespace=" + workload.Namespace, "--sort-by=.lastTimestamp"},
		{
			"logs", "--namespace=" + workload.Namespace, "--selector=" + selector,
			"--all-containers=true", "--prefix=true", "--tail=200",
		},
	}
	var resultErr error
	for _, command := range commands {
		output, err := manager.runKubectl(ctx, cluster, nil, command...)
		if len(output) > 0 {
			_, _ = manager.config.Output.Write(output)
			if output[len(output)-1] != '\n' {
				_, _ = io.WriteString(manager.config.Output, "\n")
			}
		}
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("service chaos: diagnostics for %s", cluster.DC))
		}
	}

	return resultErr
}

func (manager *Manager) runKubectl(
	ctx context.Context,
	cluster Cluster,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	base := []string{"--kubeconfig=" + cluster.Kubeconfig, "--context=" + cluster.Context}

	return manager.kubectl.Run(ctx, stdin, append(base, arguments...)...)
}

func (runner kubectlRunner) Run(
	ctx context.Context,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, runner.path, arguments...)
	command.Stdin = bytes.NewReader(stdin)
	output := &limitBuffer{remaining: maxCommandOutputBytes}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if output.truncated {
		return output.Bytes(), errors.Join(err, errors.New("service chaos: kubectl output exceeded bounds"))
	}

	return output.Bytes(), err
}

type jsonPatchOperation struct {
	Operation string            `json:"op"`
	Path      string            `json:"path"`
	Value     map[string]string `json:"value"`
}

type endpointsObject struct {
	Subsets []endpointSubset `json:"subsets"`
}

type endpointSubset struct {
	Addresses []struct{} `json:"addresses"`
}

type limitBuffer struct {
	bytes.Buffer
	remaining int
	truncated bool
}

func (buffer *limitBuffer) Write(content []byte) (int, error) {
	original := len(content)
	if len(content) > buffer.remaining {
		content = content[:max(buffer.remaining, 0)]
		buffer.truncated = true
	}
	buffer.remaining -= len(content)
	_, _ = buffer.Buffer.Write(content)

	return original, nil
}
