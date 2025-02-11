// Copyright 2018 Envoyproxy Authors
//
//   Licensed under the Apache License, Version 2.0 (the "License");
//   you may not use this file except in compliance with the License.
//   You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.

package xds_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	. "github.com/Kong/kuma/pkg/util/xds"

	v2 "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	core "github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	"github.com/envoyproxy/go-control-plane/pkg/cache"
	"github.com/envoyproxy/go-control-plane/pkg/test/resource"
)

const (
	clusterName  = "cluster0"
	routeName    = "route0"
	listenerName = "listener0"
	runtimeName  = "runtime0"
)

var (
	endpoint = resource.MakeEndpoint(clusterName, 8080)
	cluster  = resource.MakeCluster(resource.Ads, clusterName)
	route    = resource.MakeRoute(routeName, clusterName)
	listener = resource.MakeHTTPListener(resource.Ads, listenerName, 80, routeName)
	runtime  = resource.MakeRuntime(runtimeName)
)

type group struct{}

const (
	key = "node"
)

func (group) ID(node *core.Node) string {
	if node != nil {
		return node.Id
	}
	return key
}

type SampleSnapshot struct {
	cache.Snapshot
}

// NewSampleSnapshot creates a snapshot from response types and a version.
func NewSampleSnapshot(version string,
	endpoints []cache.Resource,
	clusters []cache.Resource,
	routes []cache.Resource,
	listeners []cache.Resource,
	runtimes []cache.Resource) *SampleSnapshot {
	return &SampleSnapshot{
		cache.NewSnapshot(version, endpoints, clusters, routes, listeners, runtimes),
	}
}

// GetSupportedTypes returns a list of xDS types supported by this snapshot.
func (s *SampleSnapshot) GetSupportedTypes() []string {
	return []string{
		cache.EndpointType,
		cache.ClusterType,
		cache.RouteType,
		cache.ListenerType,
		cache.SecretType,
		cache.RuntimeType,
	}
}

// WithVersion creates a new snapshot with a different version for a given resource type.
func (s *SampleSnapshot) WithVersion(typ string, version string) Snapshot {
	if s == nil {
		return nil
	}
	if s.GetVersion(typ) == version {
		return s
	}
	new := &SampleSnapshot{
		Snapshot: cache.Snapshot{
			Endpoints: s.Endpoints,
			Clusters:  s.Clusters,
			Routes:    s.Routes,
			Listeners: s.Listeners,
			Secrets:   s.Secrets,
			Runtimes:  s.Runtimes,
		},
	}
	switch typ {
	case cache.EndpointType:
		new.Endpoints = cache.Resources{Version: version, Items: s.Endpoints.Items}
	case cache.ClusterType:
		new.Clusters = cache.Resources{Version: version, Items: s.Clusters.Items}
	case cache.RouteType:
		new.Routes = cache.Resources{Version: version, Items: s.Routes.Items}
	case cache.ListenerType:
		new.Listeners = cache.Resources{Version: version, Items: s.Listeners.Items}
	case cache.SecretType:
		new.Secrets = cache.Resources{Version: version, Items: s.Secrets.Items}
	case cache.RuntimeType:
		new.Runtimes = cache.Resources{Version: version, Items: s.Runtimes.Items}
	}
	return new
}

var (
	version  = "x"
	version2 = "y"

	snapshot = NewSampleSnapshot(version,
		[]cache.Resource{endpoint},
		[]cache.Resource{cluster},
		[]cache.Resource{route},
		[]cache.Resource{listener},
		[]cache.Resource{runtime})

	names = map[string][]string{
		cache.EndpointType: []string{clusterName},
		cache.ClusterType:  nil,
		cache.RouteType:    []string{routeName},
		cache.ListenerType: nil,
		cache.RuntimeType:  nil,
	}

	testTypes = []string{
		cache.EndpointType,
		cache.ClusterType,
		cache.RouteType,
		cache.ListenerType,
		cache.RuntimeType,
	}
)

type logger struct {
	t *testing.T
}

func (log logger) Infof(format string, args ...interface{})  { log.t.Logf(format, args...) }
func (log logger) Errorf(format string, args ...interface{}) { log.t.Logf(format, args...) }

func TestSnapshotCache(t *testing.T) {
	c := NewSnapshotCache(true, group{}, logger{t: t})

	if _, err := c.GetSnapshot(key); err == nil {
		t.Errorf("unexpected snapshot found for key %q", key)
	}

	if err := c.SetSnapshot(key, snapshot); err != nil {
		t.Fatal(err)
	}

	snap, err := c.GetSnapshot(key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap, snapshot) {
		t.Errorf("expect snapshot: %v, got: %v", snapshot, snap)
	}

	// try to get endpoints with incorrect list of names
	// should not receive response
	value, _ := c.CreateWatch(v2.DiscoveryRequest{TypeUrl: cache.EndpointType, ResourceNames: []string{"none"}})
	select {
	case out := <-value:
		t.Errorf("watch for endpoints and mismatched names => got %v, want none", out)
	case <-time.After(time.Second / 4):
	}

	for _, typ := range testTypes {
		t.Run(typ, func(t *testing.T) {
			value, _ := c.CreateWatch(v2.DiscoveryRequest{TypeUrl: typ, ResourceNames: names[typ]})
			select {
			case out := <-value:
				if out.Version != version {
					t.Errorf("got version %q, want %q", out.Version, version)
				}
				if !reflect.DeepEqual(cache.IndexResourcesByName(out.Resources), snapshot.GetResources(typ)) {
					t.Errorf("get resources %v, want %v", out.Resources, snapshot.GetResources(typ))
				}
			case <-time.After(time.Second):
				t.Fatal("failed to receive snapshot response")
			}
		})
	}
}

func TestSnapshotCacheFetch(t *testing.T) {
	c := NewSnapshotCache(true, group{}, logger{t: t})
	if err := c.SetSnapshot(key, snapshot); err != nil {
		t.Fatal(err)
	}

	for _, typ := range testTypes {
		t.Run(typ, func(t *testing.T) {
			resp, err := c.Fetch(context.Background(), v2.DiscoveryRequest{TypeUrl: typ, ResourceNames: names[typ]})
			if err != nil || resp == nil {
				t.Fatal("unexpected error or null response")
			}
			if resp.Version != version {
				t.Errorf("got version %q, want %q", resp.Version, version)
			}
		})
	}

	// no response for missing snapshot
	if resp, err := c.Fetch(context.Background(),
		v2.DiscoveryRequest{TypeUrl: cache.ClusterType, Node: &core.Node{Id: "oof"}}); resp != nil || err == nil {
		t.Errorf("missing snapshot: response is not nil %q", resp)
	}

	// no response for latest version
	if resp, err := c.Fetch(context.Background(),
		v2.DiscoveryRequest{TypeUrl: cache.ClusterType, VersionInfo: version}); resp != nil || err == nil {
		t.Errorf("latest version: response is not nil %q", resp)
	}
}

func TestSnapshotCacheWatch(t *testing.T) {
	c := NewSnapshotCache(true, group{}, logger{t: t})
	watches := make(map[string]chan cache.Response)
	for _, typ := range testTypes {
		watches[typ], _ = c.CreateWatch(v2.DiscoveryRequest{TypeUrl: typ, ResourceNames: names[typ]})
	}
	if err := c.SetSnapshot(key, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, typ := range testTypes {
		t.Run(typ, func(t *testing.T) {
			select {
			case out := <-watches[typ]:
				if out.Version != version {
					t.Errorf("got version %q, want %q", out.Version, version)
				}
				if !reflect.DeepEqual(cache.IndexResourcesByName(out.Resources), snapshot.GetResources(typ)) {
					t.Errorf("get resources %v, want %v", out.Resources, snapshot.GetResources(typ))
				}
			case <-time.After(time.Second):
				t.Fatal("failed to receive snapshot response")
			}
		})
	}

	// open new watches with the latest version
	for _, typ := range testTypes {
		watches[typ], _ = c.CreateWatch(v2.DiscoveryRequest{TypeUrl: typ, ResourceNames: names[typ], VersionInfo: version})
	}
	if count := c.GetStatusInfo(key).GetNumWatches(); count != len(testTypes) {
		t.Errorf("watches should be created for the latest version: %d", count)
	}

	// set partially-versioned snapshot
	snapshot2 := snapshot
	snapshot2.Endpoints = cache.NewResources(version2, []cache.Resource{resource.MakeEndpoint(clusterName, 9090)})
	if err := c.SetSnapshot(key, snapshot2); err != nil {
		t.Fatal(err)
	}
	if count := c.GetStatusInfo(key).GetNumWatches(); count != len(testTypes)-1 {
		t.Errorf("watches should be preserved for all but one: %d", count)
	}

	// validate response for endpoints
	select {
	case out := <-watches[cache.EndpointType]:
		if out.Version != version2 {
			t.Errorf("got version %q, want %q", out.Version, version2)
		}
		if !reflect.DeepEqual(cache.IndexResourcesByName(out.Resources), snapshot2.Endpoints.Items) {
			t.Errorf("get resources %v, want %v", out.Resources, snapshot2.Endpoints.Items)
		}
	case <-time.After(time.Second):
		t.Fatal("failed to receive snapshot response")
	}
}

func TestConcurrentSetWatch(t *testing.T) {
	c := NewSnapshotCache(false, group{}, logger{t: t})
	for i := 0; i < 50; i++ {
		func(i int) {
			t.Run(fmt.Sprintf("worker%d", i), func(t *testing.T) {
				t.Parallel()
				id := fmt.Sprintf("%d", i%2)
				var cancel func()
				if i < 25 {
					_ = c.SetSnapshot(id, &SampleSnapshot{cache.Snapshot{
						Endpoints: cache.NewResources(fmt.Sprintf("v%d", i), []cache.Resource{resource.MakeEndpoint(clusterName, uint32(i))}),
					}})
				} else {
					if cancel != nil {
						cancel()
					}
					_, _ = c.CreateWatch(v2.DiscoveryRequest{
						Node:    &core.Node{Id: id},
						TypeUrl: cache.EndpointType,
					})
				}
			})
		}(i)
	}
}

func TestSnapshotCacheWatchCancel(t *testing.T) {
	c := NewSnapshotCache(true, group{}, logger{t: t})
	for _, typ := range testTypes {
		_, cancel := c.CreateWatch(v2.DiscoveryRequest{TypeUrl: typ, ResourceNames: names[typ]})
		cancel()
	}
	// should be status info for the node
	if keys := c.GetStatusKeys(); len(keys) == 0 {
		t.Error("got 0, want status info for the node")
	}

	for _, typ := range testTypes {
		if count := c.GetStatusInfo(key).GetNumWatches(); count > 0 {
			t.Errorf("watches should be released for %s", typ)
		}
	}

	if empty := c.GetStatusInfo("missing"); empty != nil {
		t.Errorf("should not return a status for unknown key: got %#v", empty)
	}
}

func TestSnapshotClear(t *testing.T) {
	c := NewSnapshotCache(true, group{}, logger{t: t})
	if err := c.SetSnapshot(key, snapshot); err != nil {
		t.Fatal(err)
	}
	c.ClearSnapshot(key)
	if empty := c.GetStatusInfo(key); empty != nil {
		t.Errorf("cache should be cleared")
	}
	if keys := c.GetStatusKeys(); len(keys) != 0 {
		t.Errorf("keys should be empty")
	}
}
