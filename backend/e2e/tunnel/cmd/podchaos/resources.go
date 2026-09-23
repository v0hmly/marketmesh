package main

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	"github.com/v0hmly/marketmesh/e2e/tunnel/podchaos"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type runtimeResources struct {
	cancel    context.CancelFunc
	frontDoor *podchaos.LocalFrontDoor
	invoker   *probe.FrontDoorInvoker
	forwards  []*podchaos.LedgerPortForward
	clients   []*grpc.ClientConn
	closeOnce sync.Once
	closeErr  error
}

func (resources *runtimeResources) startLedgerCollector(
	ctx context.Context,
	controller *podchaos.KubernetesController,
	runID string,
	kubectlPath string,
) (*probe.LedgerCollector, error) {
	operationCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	pods, err := controller.LedgerPods(operationCtx, runID)
	cancel()
	if err != nil {
		return nil, err
	}
	if len(pods) != 4 {
		return nil, errors.New("ledger pod inventory must contain exactly four replicas")
	}
	tlsConfigurations := map[podchaos.DC]*tls.Config{}
	sources := make([]probe.LedgerSource, 0, len(pods))
	for _, ledgerPod := range pods {
		configuration := tlsConfigurations[ledgerPod.DataCenter]
		if configuration == nil {
			credentialCtx, credentialCancel := context.WithTimeout(ctx, operationTimeout)
			configuration, err = controller.InternalClientTLSConfig(
				credentialCtx,
				runID,
				ledgerPod.DataCenter,
			)
			credentialCancel()
			if err != nil {
				return nil, err
			}
			tlsConfigurations[ledgerPod.DataCenter] = configuration
		}
		forward, err := podchaos.StartLedgerPortForward(ctx, ledgerPod.Pod, kubectlPath)
		if err != nil {
			return nil, err
		}
		resources.forwards = append(resources.forwards, forward)
		connection, err := grpc.NewClient(
			"passthrough:///"+forward.Address(),
			grpc.WithTransportCredentials(credentials.NewTLS(configuration.Clone())),
		)
		if err != nil {
			return nil, errors.New("creating direct ledger client")
		}
		resources.clients = append(resources.clients, connection)
		sources = append(sources, probe.LedgerSource{
			DataCenter: probeDataCenter(ledgerPod.DataCenter),
			Client:     e2ev1.NewFakeInternalServiceClient(connection),
		})
	}
	return probe.NewLedgerCollector(sources, ledgerLimit)
}

func probeDataCenter(dc podchaos.DC) probe.DataCenter {
	if dc == podchaos.DCA {
		return probe.DataCenterA
	}
	if dc == podchaos.DCB {
		return probe.DataCenterB
	}
	return probe.DataCenterUnknown
}

func (resources *runtimeResources) Close() error {
	if resources == nil {
		return nil
	}
	resources.closeOnce.Do(func() {
		var failures []error
		if resources.invoker != nil {
			resources.invoker.Close()
		}
		for _, connection := range resources.clients {
			if err := connection.Close(); err != nil {
				failures = append(failures, errors.New("closing direct ledger client"))
			}
		}
		for _, forward := range resources.forwards {
			if err := forward.Close(); err != nil {
				failures = append(failures, err)
			}
		}
		if resources.frontDoor != nil {
			if err := resources.frontDoor.Close(); err != nil {
				failures = append(failures, err)
			}
		}
		if resources.cancel != nil {
			resources.cancel()
		}
		resources.closeErr = errors.Join(failures...)
	})
	return resources.closeErr
}
