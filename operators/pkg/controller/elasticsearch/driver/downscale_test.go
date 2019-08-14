// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package driver

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/elastic/cloud-on-k8s/operators/pkg/apis/elasticsearch/v1alpha1"
	"github.com/elastic/cloud-on-k8s/operators/pkg/controller/common"
	"github.com/elastic/cloud-on-k8s/operators/pkg/controller/common/reconciler"
	esclient "github.com/elastic/cloud-on-k8s/operators/pkg/controller/elasticsearch/client"
	"github.com/elastic/cloud-on-k8s/operators/pkg/controller/elasticsearch/nodespec"
	"github.com/elastic/cloud-on-k8s/operators/pkg/controller/elasticsearch/observer"
	"github.com/elastic/cloud-on-k8s/operators/pkg/controller/elasticsearch/reconcile"
	"github.com/elastic/cloud-on-k8s/operators/pkg/controller/elasticsearch/sset"
	"github.com/elastic/cloud-on-k8s/operators/pkg/utils/k8s"
)

// Sample StatefulSets to use in tests
var (
	ssetMaster3Replicas     = nodespec.CreateTestSset("ssetMaster3Replicas", "7.2.0", 3, true, false)
	podsSsetMaster3Replicas = []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ssetMaster3Replicas.Namespace,
				Name:      sset.PodName(ssetMaster3Replicas.Name, 0),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ssetMaster3Replicas.Namespace,
				Name:      sset.PodName(ssetMaster3Replicas.Name, 1),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ssetMaster3Replicas.Namespace,
				Name:      sset.PodName(ssetMaster3Replicas.Name, 2),
			},
		},
	}
	ssetData4Replicas     = nodespec.CreateTestSset("ssetData4Replicas", "7.2.0", 4, false, true)
	podsSsetData4Replicas = []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ssetMaster3Replicas.Namespace,
				Name:      sset.PodName(ssetData4Replicas.Name, 0),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ssetMaster3Replicas.Namespace,
				Name:      sset.PodName(ssetData4Replicas.Name, 1),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ssetMaster3Replicas.Namespace,
				Name:      sset.PodName(ssetData4Replicas.Name, 2),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ssetMaster3Replicas.Namespace,
				Name:      sset.PodName(ssetData4Replicas.Name, 3),
			},
		},
	}
	runtimeObjs = []runtime.Object{&ssetMaster3Replicas, &ssetData4Replicas,
		&podsSsetMaster3Replicas[0], &podsSsetMaster3Replicas[1], &podsSsetMaster3Replicas[2],
		&podsSsetData4Replicas[0], &podsSsetData4Replicas[1], &podsSsetData4Replicas[2], &podsSsetData4Replicas[3],
	}
	requeueResults = (&reconciler.Results{}).WithResult(defaultRequeue)
	emptyResults   = &reconciler.Results{}
)

// fakeESClient mocks the ES client to register function calls that were made.
type fakeESClient struct { //nolint:maligned
	esclient.Client

	SetMinimumMasterNodesCalled     bool
	SetMinimumMasterNodesCalledWith int

	AddVotingConfigExclusionsCalled     bool
	AddVotingConfigExclusionsCalledWith []string

	ExcludeFromShardAllocationCalled     bool
	ExcludeFromShardAllocationCalledWith string
}

func (f *fakeESClient) SetMinimumMasterNodes(ctx context.Context, n int) error {
	f.SetMinimumMasterNodesCalled = true
	f.SetMinimumMasterNodesCalledWith = n
	return nil
}

func (f *fakeESClient) AddVotingConfigExclusions(ctx context.Context, nodeNames []string, timeout string) error {
	f.AddVotingConfigExclusionsCalled = true
	f.AddVotingConfigExclusionsCalledWith = append(f.AddVotingConfigExclusionsCalledWith, nodeNames...)
	return nil
}

func (f *fakeESClient) ExcludeFromShardAllocation(ctx context.Context, nodes string) error {
	f.ExcludeFromShardAllocationCalled = true
	f.ExcludeFromShardAllocationCalledWith = nodes
	return nil
}

// -- Tests start here

func TestHandleDownscale(t *testing.T) {
	// This test focuses on one code path that visits most functions.
	// Derived paths are individually tested in unit tests of the other functions.

	// We want to downscale 2 StatefulSets (masters 3 -> 1 and data 4 -> 2) in version 7.X,
	// but should only be allowed a partial downscale (3 -> 2 and 4 -> 3).

	k8sClient := k8s.WrapClient(fake.NewFakeClient(runtimeObjs...))
	esClient := &fakeESClient{}
	actualStatefulSets := sset.StatefulSetList{ssetMaster3Replicas, ssetData4Replicas}
	downscaleCtx := downscaleContext{
		k8sClient:      k8sClient,
		expectations:   reconciler.NewExpectations(),
		reconcileState: reconcile.NewState(v1alpha1.Elasticsearch{}),
		observedState: observer.State{
			ClusterState: &esclient.ClusterState{
				ClusterName: "cluster-name",
				Nodes: map[string]esclient.ClusterStateNode{
					// nodes from 1st sset
					"ssetMaster3Replicas-0": {Name: "ssetMaster3Replicas-0"},
					"ssetMaster3Replicas-1": {Name: "ssetMaster3Replicas-1"},
					"ssetMaster3Replicas-2": {Name: "ssetMaster3Replicas-2"},
					// nodes from 2nd sset
					"ssetData4Replicas-0": {Name: "ssetData4Replicas-0"},
					"ssetData4Replicas-1": {Name: "ssetData4Replicas-1"},
					"ssetData4Replicas-2": {Name: "ssetData4Replicas-2"},
					"ssetData4Replicas-3": {Name: "ssetData4Replicas-3"},
				},
				RoutingTable: esclient.RoutingTable{
					Indices: map[string]esclient.Shards{
						"index-1": {
							Shards: map[string][]esclient.Shard{
								"0": {
									// node ssetData4Replicas-2 cannot leave the cluster because of this shard
									{Index: "index-1", Shard: 0, State: esclient.STARTED, Node: "ssetData4Replicas-2"},
								},
							},
						},
					},
				},
			},
		},
		esClient: esClient,
	}

	// request master nodes downscale from 3 to 1 replicas
	ssetMaster3ReplicasDownscaled := *ssetMaster3Replicas.DeepCopy()
	ssetMaster3ReplicasDownscaled.Spec.Replicas = common.Int32(1)
	// request data nodes downscale from 4 to 2 replicas
	ssetData4ReplicasDownscaled := *ssetData4Replicas.DeepCopy()
	ssetData4ReplicasDownscaled.Spec.Replicas = common.Int32(2)
	requestedStatefulSets := sset.StatefulSetList{ssetMaster3ReplicasDownscaled, ssetData4ReplicasDownscaled}

	// do the downscale
	results := HandleDownscale(downscaleCtx, requestedStatefulSets, actualStatefulSets)
	require.False(t, results.HasError())

	// data migration should have been requested for all nodes leaving the cluster
	require.True(t, esClient.ExcludeFromShardAllocationCalled)
	require.Equal(t, "ssetMaster3Replicas-2,ssetMaster3Replicas-1,ssetData4Replicas-3,ssetData4Replicas-2", esClient.ExcludeFromShardAllocationCalledWith)

	// only part of the expected replicas of ssetMaster3Replicas should be updated,
	// since we remove only one master at a time
	ssetMaster3ReplicasExpectedAfterDownscale := *ssetMaster3Replicas.DeepCopy()
	ssetMaster3ReplicasExpectedAfterDownscale.Spec.Replicas = common.Int32(2)
	// only part of the expected replicas of ssetData4Replicas should be updated,
	// since a node still needs to migrate data
	ssetData4ReplicasExpectedAfterDownscale := *ssetData4Replicas.DeepCopy()
	ssetData4ReplicasExpectedAfterDownscale.Spec.Replicas = common.Int32(3)

	expectedAfterDownscale := []appsv1.StatefulSet{ssetMaster3ReplicasExpectedAfterDownscale, ssetData4ReplicasExpectedAfterDownscale}

	// a requeue should be requested since all nodes were not downscaled
	// (2 requeues actually: for data migration & master nodes)
	require.Equal(t, (&reconciler.Results{}).WithResult(defaultRequeue).WithResult(defaultRequeue), results)

	// voting config exclusion should have been added for leaving masters
	require.True(t, esClient.AddVotingConfigExclusionsCalled)
	require.Equal(t, []string{"ssetMaster3Replicas-2"}, esClient.AddVotingConfigExclusionsCalledWith)

	// compare what has been updated in the apiserver with what we would expect
	var actual appsv1.StatefulSetList
	err := k8sClient.List(&client.ListOptions{}, &actual)
	require.NoError(t, err)
	require.Equal(t, expectedAfterDownscale, actual.Items)

	// running the downscale again should requeue since some pods are not terminated yet
	results = HandleDownscale(downscaleCtx, requestedStatefulSets, actual.Items)
	require.False(t, results.HasError())
	require.Equal(t, requeueResults, results)
	// no StatefulSet should have been updated
	err = k8sClient.List(&client.ListOptions{}, &actual)
	require.NoError(t, err)
	require.Equal(t, expectedAfterDownscale, actual.Items)

	// simulate pods deletion that would be done by the StatefulSet controller
	require.NoError(t, k8sClient.Delete(&podsSsetMaster3Replicas[2]))
	require.NoError(t, k8sClient.Delete(&podsSsetData4Replicas[3]))

	// running the downscale again should remove the next master,
	// and also requeue since data migration is still not over for data nodes
	results = HandleDownscale(downscaleCtx, requestedStatefulSets, actual.Items)
	require.False(t, results.HasError())
	require.Equal(t, requeueResults, results)

	// one less master
	ssetMaster3ReplicasExpectedAfterDownscale.Spec.Replicas = common.Int32(1)
	expectedAfterDownscale = []appsv1.StatefulSet{ssetMaster3ReplicasExpectedAfterDownscale, ssetData4ReplicasExpectedAfterDownscale}
	err = k8sClient.List(&client.ListOptions{}, &actual)
	require.NoError(t, err)
	require.Equal(t, expectedAfterDownscale, actual.Items)
	// simulate master pod deletion
	require.NoError(t, k8sClient.Delete(&podsSsetMaster3Replicas[1]))

	// once data migration is over the downscale should continue for next data nodes
	downscaleCtx.observedState.ClusterState.RoutingTable.Indices["index-1"].Shards["0"][0].Node = "ssetData4Replicas-1"
	expectedAfterDownscale[1].Spec.Replicas = common.Int32(2)
	results = HandleDownscale(downscaleCtx, requestedStatefulSets, actual.Items)
	require.False(t, results.HasError())
	require.Equal(t, emptyResults, results)
	err = k8sClient.List(&client.ListOptions{}, &actual)
	require.NoError(t, err)
	require.Equal(t, expectedAfterDownscale, actual.Items)

	// data migration should have been requested for the data node leaving the cluster
	require.True(t, esClient.ExcludeFromShardAllocationCalled)
	require.Equal(t, "ssetData4Replicas-2", esClient.ExcludeFromShardAllocationCalledWith)

	// simulate pod deletion
	require.NoError(t, k8sClient.Delete(&podsSsetData4Replicas[2]))

	// running the downscale again should not remove any new node
	results = HandleDownscale(downscaleCtx, requestedStatefulSets, actual.Items)
	require.False(t, results.HasError())
	require.Equal(t, emptyResults, results)
	err = k8sClient.List(&client.ListOptions{}, &actual)
	require.NoError(t, err)
	require.Equal(t, expectedAfterDownscale, actual.Items)

	// data migration settings should have been cleared
	require.True(t, esClient.ExcludeFromShardAllocationCalled)
	require.Equal(t, "none_excluded", esClient.ExcludeFromShardAllocationCalledWith)
}

func Test_calculateDownscales(t *testing.T) {
	ssets := sset.StatefulSetList{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns",
				Name:      "sset0",
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: common.Int32(3),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns",
				Name:      "sset1",
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: common.Int32(3)},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns",
				Name:      "sset2",
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: common.Int32(3)},
		},
	}
	tests := []struct {
		name                 string
		expectedStatefulSets sset.StatefulSetList
		actualStatefulSets   sset.StatefulSetList
		want                 []ssetDownscale
	}{
		{
			name:               "no actual statefulset: no downscale",
			actualStatefulSets: nil,
			want:               []ssetDownscale{},
		},
		{
			name:                 "expected == actual",
			expectedStatefulSets: ssets,
			actualStatefulSets:   ssets,
			want:                 []ssetDownscale{},
		},
		{
			name:                 "remove all ssets",
			expectedStatefulSets: nil,
			actualStatefulSets:   ssets,
			want: []ssetDownscale{
				{
					statefulSet:     ssets[0],
					initialReplicas: *ssets[0].Spec.Replicas,
					targetReplicas:  0,
				},
				{
					statefulSet:     ssets[1],
					initialReplicas: *ssets[1].Spec.Replicas,
					targetReplicas:  0,
				},
				{
					statefulSet:     ssets[2],
					initialReplicas: *ssets[2].Spec.Replicas,
					targetReplicas:  0,
				},
			},
		},
		{
			name: "downscale 2 out of 3 StatefulSets",
			expectedStatefulSets: sset.StatefulSetList{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns",
						Name:      "sset0",
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: common.Int32(3),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns",
						Name:      "sset1",
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: common.Int32(2)},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns",
						Name:      "sset2",
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: common.Int32(1)},
				},
			},
			actualStatefulSets: ssets,
			want: []ssetDownscale{
				{
					statefulSet:     ssets[1],
					initialReplicas: *ssets[1].Spec.Replicas,
					targetReplicas:  2,
				},
				{
					statefulSet:     ssets[2],
					initialReplicas: *ssets[2].Spec.Replicas,
					targetReplicas:  1,
				},
			},
		},
		{
			name: "upscale: no downscale",
			expectedStatefulSets: sset.StatefulSetList{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns",
						Name:      "sset0",
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: common.Int32(4),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns",
						Name:      "sset1",
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: common.Int32(5)},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns",
						Name:      "sset2",
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: common.Int32(3)},
				},
			},
			actualStatefulSets: ssets,
			want:               []ssetDownscale{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calculateDownscales(tt.expectedStatefulSets, tt.actualStatefulSets); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("calculateDownscales() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_calculatePerformableDownscale(t *testing.T) {
	type args struct {
		ctx             downscaleContext
		downscale       ssetDownscale
		state           *downscaleState
		allLeavingNodes []string
	}
	tests := []struct {
		name string
		args args
		want ssetDownscale
	}{
		{
			name: "no downscale planned",
			args: args{
				ctx: downscaleContext{},
				downscale: ssetDownscale{
					initialReplicas: 3,
					targetReplicas:  3,
				},
				state:           &downscaleState{masterRemovalInProgress: false, runningMasters: 3},
				allLeavingNodes: []string{"node-1", "node-2"},
			},
			want: ssetDownscale{
				initialReplicas: 3,
				targetReplicas:  3,
			},
		},
		{
			name: "downscale possible from 3 to 2",
			args: args{
				ctx: downscaleContext{
					observedState: observer.State{
						// all migrations are over
						ClusterState: &esclient.ClusterState{
							ClusterName: "cluster-name",
						},
					},
				},
				downscale: ssetDownscale{
					initialReplicas: 3,
					targetReplicas:  2,
				},
				state:           &downscaleState{masterRemovalInProgress: false, runningMasters: 3},
				allLeavingNodes: []string{"node-1", "node-2"},
			},
			want: ssetDownscale{
				initialReplicas: 3,
				targetReplicas:  2,
			},
		},
		{
			name: "downscale not possible: one master already removed",
			args: args{
				ctx: downscaleContext{
					observedState: observer.State{
						// all migrations are over
						ClusterState: &esclient.ClusterState{
							ClusterName: "cluster-name",
						},
					},
				},
				downscale: ssetDownscale{
					statefulSet:     ssetMaster3Replicas,
					initialReplicas: 3,
					targetReplicas:  2,
				},
				// a master node has already been removed
				state:           &downscaleState{masterRemovalInProgress: true, runningMasters: 3},
				allLeavingNodes: []string{"node-1", "node-2"},
			},
			want: ssetDownscale{
				statefulSet:     ssetMaster3Replicas,
				initialReplicas: 3,
				targetReplicas:  3,
			},
		},
		{
			name: "downscale only possible from 3 to 2 instead of 3 to 1 (1 master at a time)",
			args: args{
				ctx: downscaleContext{
					observedState: observer.State{
						// all migrations are over
						ClusterState: &esclient.ClusterState{
							ClusterName: "cluster-name",
						},
					},
				},
				downscale: ssetDownscale{
					statefulSet:     ssetMaster3Replicas,
					initialReplicas: 3,
					targetReplicas:  1,
				},
				// invariants limits us to one master node downscale only
				state:           &downscaleState{masterRemovalInProgress: false, runningMasters: 3},
				allLeavingNodes: []string{"node-1", "node-2"},
			},
			want: ssetDownscale{
				statefulSet:     ssetMaster3Replicas,
				initialReplicas: 3,
				targetReplicas:  2,
			},
		},
		{
			name: "downscale not possible: cannot remove the last master",
			args: args{
				ctx: downscaleContext{
					observedState: observer.State{
						// all migrations are over
						ClusterState: &esclient.ClusterState{
							ClusterName: "cluster-name",
						},
					},
				},
				downscale: ssetDownscale{
					statefulSet:     ssetMaster3Replicas,
					initialReplicas: 1,
					targetReplicas:  0,
				},
				// only one master is running
				state:           &downscaleState{masterRemovalInProgress: false, runningMasters: 1},
				allLeavingNodes: []string{"node-1", "node-2"},
			},
			want: ssetDownscale{
				statefulSet:     ssetMaster3Replicas,
				initialReplicas: 1,
				targetReplicas:  1,
			},
		},
		{
			name: "downscale not possible: data migration not ready",
			args: args{
				ctx: downscaleContext{
					observedState: observer.State{
						// cluster state is not populated
						ClusterState: nil,
					},
					reconcileState: reconcile.NewState(v1alpha1.Elasticsearch{}),
				},
				downscale: ssetDownscale{
					statefulSet:     ssetMaster3Replicas,
					initialReplicas: 3,
					targetReplicas:  1,
				},
				state:           &downscaleState{masterRemovalInProgress: false, runningMasters: 3},
				allLeavingNodes: []string{"node-1", "node-2"},
			},
			want: ssetDownscale{
				statefulSet:     ssetMaster3Replicas,
				initialReplicas: 3,
				targetReplicas:  3,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculatePerformableDownscale(tt.args.ctx, tt.args.state, tt.args.downscale, tt.args.allLeavingNodes)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("calculatePerformableDownscale() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_attemptDownscale(t *testing.T) {
	tests := []struct {
		name                 string
		downscale            ssetDownscale
		state                *downscaleState
		statefulSets         sset.StatefulSetList
		expectedStatefulSets []appsv1.StatefulSet
	}{
		{
			name: "1 statefulset should be removed",
			downscale: ssetDownscale{
				statefulSet:     nodespec.CreateTestSset("should-be-removed", "7.1.0", 0, true, true),
				initialReplicas: 0,
				targetReplicas:  0,
			},
			state: &downscaleState{runningMasters: 2, masterRemovalInProgress: false},
			statefulSets: sset.StatefulSetList{
				nodespec.CreateTestSset("should-be-removed", "7.1.0", 0, true, true),
				nodespec.CreateTestSset("should-stay", "7.1.0", 2, true, true),
			},
			expectedStatefulSets: []appsv1.StatefulSet{
				nodespec.CreateTestSset("should-stay", "7.1.0", 2, true, true),
			},
		},
		{
			name: "target replicas == initial replicas",
			downscale: ssetDownscale{
				statefulSet:     nodespec.CreateTestSset("default", "7.1.0", 3, true, true),
				initialReplicas: 3,
				targetReplicas:  3,
			},
			state: &downscaleState{runningMasters: 2, masterRemovalInProgress: false},
			statefulSets: sset.StatefulSetList{
				nodespec.CreateTestSset("default", "7.1.0", 3, true, true),
			},
			expectedStatefulSets: []appsv1.StatefulSet{
				nodespec.CreateTestSset("default", "7.1.0", 3, true, true),
			},
		},
		{
			name: "upscale case",
			downscale: ssetDownscale{
				statefulSet:     nodespec.CreateTestSset("default", "7.1.0", 3, true, true),
				initialReplicas: 3,
				targetReplicas:  4,
			},
			state: &downscaleState{runningMasters: 2, masterRemovalInProgress: false},
			statefulSets: sset.StatefulSetList{
				nodespec.CreateTestSset("default", "7.1.0", 3, true, true),
			},
			expectedStatefulSets: []appsv1.StatefulSet{
				nodespec.CreateTestSset("default", "7.1.0", 3, true, true),
			},
		},
		{
			name: "perform 3 -> 2 downscale",
			downscale: ssetDownscale{
				statefulSet:     nodespec.CreateTestSset("default", "7.1.0", 3, true, true),
				initialReplicas: 3,
				targetReplicas:  2,
			},
			state: &downscaleState{runningMasters: 2, masterRemovalInProgress: false},
			statefulSets: sset.StatefulSetList{
				nodespec.CreateTestSset("default", "7.1.0", 3, true, true),
			},
			expectedStatefulSets: []appsv1.StatefulSet{
				nodespec.CreateTestSset("default", "7.1.0", 2, true, true),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var runtimeObjs []runtime.Object
			for i := range tt.statefulSets {
				runtimeObjs = append(runtimeObjs, &tt.statefulSets[i])
			}
			k8sClient := k8s.WrapClient(fake.NewFakeClient(runtimeObjs...))
			downscaleCtx := downscaleContext{
				k8sClient:      k8sClient,
				expectations:   reconciler.NewExpectations(),
				reconcileState: reconcile.NewState(v1alpha1.Elasticsearch{}),
				observedState: observer.State{
					// all migrations are over
					ClusterState: &esclient.ClusterState{
						ClusterName: "cluster-name",
					},
				},
				esClient: &fakeESClient{},
			}
			// do the downscale
			_, err := attemptDownscale(downscaleCtx, tt.downscale, tt.state, nil, tt.statefulSets)
			require.NoError(t, err)
			// retrieve statefulsets
			var ssets appsv1.StatefulSetList
			err = k8sClient.List(&client.ListOptions{}, &ssets)
			require.NoError(t, err)
			require.Equal(t, tt.expectedStatefulSets, ssets.Items)
		})
	}
}

func Test_doDownscale_updateReplicasAndExpectations(t *testing.T) {
	sset1 := ssetMaster3Replicas
	sset1.Generation = 1
	sset2 := ssetData4Replicas
	sset2.Generation = 1
	k8sClient := k8s.WrapClient(fake.NewFakeClient(&sset1, &sset2))
	downscaleCtx := downscaleContext{
		k8sClient:    k8sClient,
		expectations: reconciler.NewExpectations(),
		esClient:     &fakeESClient{},
	}

	expectedSset1 := *sset1.DeepCopy()
	// simulate sset generation updated during the downscale (not done by the fake client)
	sset1.Generation = 2
	expectedSset1.Generation = 2
	// downscale a StatefulSet from 3 to 2 replicas
	downscale := ssetDownscale{
		statefulSet:     sset1,
		initialReplicas: 3,
		targetReplicas:  2,
	}
	expectedSset1.Spec.Replicas = &downscale.targetReplicas

	// no expectation is currently set
	require.True(t, downscaleCtx.expectations.GenerationExpected(sset1.ObjectMeta))

	// do the downscale
	err := doDownscale(downscaleCtx, downscale, sset.StatefulSetList{sset1, sset2})
	require.NoError(t, err)

	// sset resource should be updated
	var ssets appsv1.StatefulSetList
	err = k8sClient.List(&client.ListOptions{}, &ssets)
	require.NoError(t, err)
	require.Equal(t, []appsv1.StatefulSet{expectedSset1, sset2}, ssets.Items)

	// expectations should have been be registered
	require.True(t, downscaleCtx.expectations.GenerationExpected(sset1.ObjectMeta))
	// not ok for a sset whose generation == 1
	sset1.Generation = 1
	require.False(t, downscaleCtx.expectations.GenerationExpected(sset1.ObjectMeta))
}

func Test_doDownscale_zen2VotingConfigExclusions(t *testing.T) {
	ssetMasters := nodespec.CreateTestSset("masters", "7.1.0", 3, true, false)
	ssetData := nodespec.CreateTestSset("datas", "7.1.0", 3, false, true)
	tests := []struct {
		name               string
		downscale          ssetDownscale
		wantZen2Called     bool
		wantZen2CalledWith []string
	}{
		{
			name: "3 -> 2 master nodes",
			downscale: ssetDownscale{
				statefulSet:     ssetMasters,
				initialReplicas: 3,
				targetReplicas:  2,
			},
			wantZen2Called:     true,
			wantZen2CalledWith: []string{"masters-2"},
		},
		{
			name: "3 -> 2 data nodes",
			downscale: ssetDownscale{
				statefulSet:     ssetData,
				initialReplicas: 3,
				targetReplicas:  2,
			},
			wantZen2Called:     false,
			wantZen2CalledWith: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := k8s.WrapClient(fake.NewFakeClient(&ssetMasters, &ssetData))
			esClient := &fakeESClient{}
			downscaleCtx := downscaleContext{
				k8sClient:      k8sClient,
				expectations:   reconciler.NewExpectations(),
				reconcileState: reconcile.NewState(v1alpha1.Elasticsearch{}),
				esClient:       esClient,
			}
			// do the downscale
			err := doDownscale(downscaleCtx, tt.downscale, sset.StatefulSetList{ssetMasters, ssetData})
			require.NoError(t, err)
			// check call to zen2 is the expected one
			require.Equal(t, tt.wantZen2Called, esClient.AddVotingConfigExclusionsCalled)
			require.Equal(t, tt.wantZen2CalledWith, esClient.AddVotingConfigExclusionsCalledWith)
		})
	}
}

func Test_doDownscale_callsZen1ForMasterNodes(t *testing.T) {
	// TODO: implement with https://github.com/elastic/cloud-on-k8s/issues/1281
	//  to handle the 2->1 masters case
}