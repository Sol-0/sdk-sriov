// Copyright (c) 2021 Nordix Foundation.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package resourcepool

import (
	"context"
	"sync"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/sdk/pkg/networkservice/core/next"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/networkservicemesh/sdk-sriov/pkg/sriov"
	"github.com/networkservicemesh/sdk-sriov/pkg/sriov/config"
)

type resourcePoolClient struct {
	resourcePool *resourcePoolConfig
}

// NewClient returns a new resource pool client chain element
func NewClient(
	driverType sriov.DriverType,
	resourceLock sync.Locker,
	pciPool PCIPool,
	resourcePool ResourcePool,
	cfg *config.Config,
) networkservice.NetworkServiceClient {
	return &resourcePoolClient{resourcePool: &resourcePoolConfig{
		driverType:   driverType,
		resourceLock: resourceLock,
		pciPool:      pciPool,
		resourcePool: resourcePool,
		config:       cfg,
		selectedVFs:  map[string]string{},
	}}
}

func (i *resourcePoolClient) Request(ctx context.Context, request *networkservice.NetworkServiceRequest, opts ...grpc.CallOption) (*networkservice.Connection, error) {
	logger := log.FromContext(ctx).WithField("resourcePoolClient", "Request")

	conn, err := next.Client(ctx).Request(ctx, request, opts...)
	if err != nil {
		return nil, err
	}

	tokenID, ok := conn.GetMechanism().GetParameters()[TokenIDKey]
	if !ok {
		logger.Infof("no token id present for endpoint connection %v", conn)
		return conn, nil
	}

	err = assignVF(ctx, logger, conn, tokenID, i.resourcePool)
	if err != nil {
		_ = i.resourcePool.close(conn)
		if _, closeErr := next.Client(ctx).Close(ctx, conn, opts...); closeErr != nil {
			logger.Errorf("failed to close failed connection: %s %s", conn.GetId(), closeErr.Error())
		}
		return nil, err
	}

	// communicate assigned VF's pci address to endpoint by making another Request and ignore
	// returned connection. this would also need subsequent chain elements to ignore
	// handling of response for 2nd Request.
	request.Connection = conn.Clone()
	if _, err = next.Client(ctx).Request(ctx, request); err != nil {
		// Perform local cleanup in case of second Request failed
		_ = i.resourcePool.close(conn)
		if _, closeErr := next.Client(ctx).Close(ctx, conn, opts...); closeErr != nil {
			logger.Errorf("failed to close failed connection: %s %s", conn.GetId(), closeErr.Error())
		}
	}

	return conn, nil
}

func (i *resourcePoolClient) Close(ctx context.Context, conn *networkservice.Connection, opts ...grpc.CallOption) (*empty.Empty, error) {
	rv, err := next.Client(ctx).Close(ctx, conn, opts...)
	closeErr := i.resourcePool.close(conn)

	if err != nil && closeErr != nil {
		return nil, errors.Wrapf(err, "failed to free VF: %v", closeErr)
	}
	if closeErr != nil {
		return nil, errors.Wrap(closeErr, "failed to free VF")
	}
	return rv, err
}
