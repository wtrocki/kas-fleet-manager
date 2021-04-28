package workers

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	authv1 "github.com/openshift/api/authorization/v1"
	userv1 "github.com/openshift/api/user/v1"
	"github.com/pkg/errors"

	ingressoperatorv1 "github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/api/ingressoperator/v1"
	storagev1 "k8s.io/api/storage/v1"

	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/config"

	v1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/operator-framework/api/pkg/operators/v1alpha2"

	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/ocm"

	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/api"
	ocmErrors "github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/errors"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/services"
	"github.com/onsi/gomega"
	. "github.com/onsi/gomega"
	clustersmgmtv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"

	projectv1 "github.com/openshift/api/project/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	k8sCoreV1 "k8s.io/api/core/v1"

	apiErrors "github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/errors"
)

var (
	testRegion     = "us-west-1"
	testProvider   = "aws"
	strimziAddonID = "managed-kafka-test"
)

func TestClusterManager_reconcileClusterStatus(t *testing.T) {
	type fields struct {
		ocmClient      ocm.Client
		clusterService services.ClusterService
		timer          *time.Timer
	}
	type args struct {
		cluster *api.Cluster
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *api.Cluster
		wantErr bool
	}{
		{
			name: "error when getting cluster from ocm fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetClusterFunc: func(clusterID string) (*clustersmgmtv1.Cluster, error) {
						return nil, errors.New("test")
					},
				},
			},
			args: args{
				cluster: &api.Cluster{
					ClusterID: "test",
				},
			},
			wantErr: true,
		},
		{
			name: "error when ocm cluster is ready but external ID is not available",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetClusterFunc: func(clusterID string) (*clustersmgmtv1.Cluster, error) {
						clusterStatusBuilder := clustersmgmtv1.NewClusterStatus().State(clustersmgmtv1.ClusterStateReady)
						res, err := clustersmgmtv1.NewCluster().Status(clusterStatusBuilder).Build()
						if err != nil {
							panic(err)
						}
						return res, nil
					},
				},
			},
			args: args{
				cluster: &api.Cluster{
					ClusterID: "test",
					Status:    api.ClusterProvisioning,
				},
			},
			wantErr: true,
		},
		{
			name: "error when updating in database fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetClusterFunc: func(clusterID string) (*clustersmgmtv1.Cluster, error) {
						clusterStatusBuilder := clustersmgmtv1.NewClusterStatus().State(clustersmgmtv1.ClusterStateReady)
						res, err := clustersmgmtv1.NewCluster().Status(clusterStatusBuilder).ExternalID("test-external-id").Build()
						if err != nil {
							panic(err)
						}
						return res, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					UpdateFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return apiErrors.GeneralError("test")
					},
				},
			},
			args: args{
				cluster: &api.Cluster{
					ClusterID: "test",
					Status:    api.ClusterProvisioning,
				},
			},
			wantErr: true,
		},
		{
			name: "database update not invoked when update not needed",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetClusterFunc: func(clusterID string) (*clustersmgmtv1.Cluster, error) {
						clusterStatusBuilder := clustersmgmtv1.NewClusterStatus().State(clustersmgmtv1.ClusterStateInstalling)
						res, err := clustersmgmtv1.NewCluster().Status(clusterStatusBuilder).Build()
						if err != nil {
							panic(err)
						}
						return res, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						// this should never be invoked as the cluster state is already accurate
						return errors.New("test")
					},
				},
			},
			args: args{
				cluster: &api.Cluster{
					ClusterID: "test",
					Status:    api.ClusterProvisioning,
				},
			},
			want: &api.Cluster{
				ClusterID: "test",
				Status:    api.ClusterProvisioning,
			},
		},
		{
			name: "provisioning state is set when internal cluster status is empty",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetClusterFunc: func(clusterID string) (*clustersmgmtv1.Cluster, error) {
						clusterStatusBuilder := clustersmgmtv1.NewClusterStatus().State(clustersmgmtv1.ClusterStatePending)
						res, err := clustersmgmtv1.NewCluster().Status(clusterStatusBuilder).Build()
						if err != nil {
							panic(err)
						}
						return res, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					UpdateFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return nil
					},
				},
			},
			args: args{
				cluster: &api.Cluster{
					ClusterID: "test",
					Status:    "",
				},
			},
			want: &api.Cluster{
				ClusterID: "test",
				Status:    api.ClusterProvisioning,
			},
		},
		{
			name: "state is failed when underlying ocm cluster failed",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetClusterFunc: func(id string) (status *clustersmgmtv1.Cluster, e error) {
						clusterStatusBuilder := clustersmgmtv1.NewClusterStatus().State(clustersmgmtv1.ClusterStateError)
						res, err := clustersmgmtv1.NewCluster().Status(clusterStatusBuilder).Build()
						if err != nil {
							panic(err)
						}
						return res, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					UpdateFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return nil
					},
				},
			},
			args: args{
				cluster: &api.Cluster{
					ClusterID: "test",
					Status:    api.ClusterProvisioning,
				},
			},
			want: &api.Cluster{
				ClusterID: "test",
				Status:    api.ClusterFailed,
			},
		},
		{
			name: "successful reconcile",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetClusterFunc: func(clusterID string) (*clustersmgmtv1.Cluster, error) {
						clusterStatusBuilder := clustersmgmtv1.NewClusterStatus().State(clustersmgmtv1.ClusterStateReady)
						res, err := clustersmgmtv1.NewCluster().Status(clusterStatusBuilder).ExternalID("test-external-id").Build()
						if err != nil {
							panic(err)
						}
						return res, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					UpdateFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return nil
					},
				},
			},
			args: args{
				cluster: &api.Cluster{
					ClusterID: "test",
					Status:    api.ClusterProvisioning,
				},
			},
			want: &api.Cluster{
				ClusterID:  "test",
				ExternalID: "test-external-id",
				Status:     api.ClusterProvisioned,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ClusterManager{
				ocmClient:      tt.fields.ocmClient,
				clusterService: tt.fields.clusterService,
				timer:          tt.fields.timer,
			}
			got, err := c.reconcileClusterStatus(tt.args.cluster)
			if (err != nil) != tt.wantErr {
				t.Errorf("reconcileClusterStatus() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reconcileClusterStatus() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClusterManager_reconcileStrimziOperator(t *testing.T) {
	type fields struct {
		ocmClient      ocm.Client
		timer          *time.Timer
		clusterService services.ClusterService
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "error when getting managed kafka addon from ocm fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						return nil, errors.New("error when getting managed kafka addon from ocm")
					},
				},
			},
			wantErr: true,
		},
		{
			name: "empty state returned when managed kafka addon not found",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						managedKafkaAddon := &clustersmgmtv1.AddOnInstallation{}
						return managedKafkaAddon, nil
					},
					CreateAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						managedKafkaAddon := &clustersmgmtv1.AddOnInstallation{}
						return managedKafkaAddon, nil
					},
				},
			},
			wantErr: false,
		},
		{
			name: "empty state returned when managed kafka addon is found but with no state",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						managedKafkaAddon, err := clustersmgmtv1.NewAddOnInstallation().ID(strimziAddonID).Build()
						if err != nil {
							panic(err)
						}
						return managedKafkaAddon, nil
					},
				},
			},
			wantErr: false,
		},
		{
			name: "failed state returned when managed kafka addon is found but with a AddOnInstallationStateFailed state",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						managedKafkaAddon, err := clustersmgmtv1.NewAddOnInstallation().ID(strimziAddonID).State(clustersmgmtv1.AddOnInstallationStateFailed).Build()
						if err != nil {
							panic(err)
						}
						return managedKafkaAddon, nil
					},
				},
			},
			wantErr: false,
		},
		{
			name: "ready state returned when managed kafka addon is found but with a AddOnInstallationStateReady state",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						managedKafkaAddon, err := clustersmgmtv1.NewAddOnInstallation().ID(strimziAddonID).State(clustersmgmtv1.AddOnInstallationStateReady).Build()
						if err != nil {
							panic(err)
						}
						return managedKafkaAddon, nil
					},
					CreateSyncSetFunc: func(clusterID string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						return &clustersmgmtv1.Syncset{}, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "apps.example.com", nil
					},
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return nil
					},
				},
			},
			wantErr: false,
		},
		{
			name: "error when creating managed kafka addon from ocm fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						managedKafkaAddon := &clustersmgmtv1.AddOnInstallation{}
						return managedKafkaAddon, nil
					},
					CreateAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						return nil, errors.New("error when creating managed kafka addon from ocm")
					},
				},
			},
			wantErr: true,
		},
		{
			name: "strimizi addon id should match",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						if addonId != strimziAddonID {
							return nil, errors.Errorf("addon id %s does not match expected value %s", addonId, strimziAddonID)
						}
						managedKafkaAddon, err := clustersmgmtv1.NewAddOnInstallation().ID(strimziAddonID).State(clustersmgmtv1.AddOnInstallationStateReady).Build()
						if err != nil {
							panic(err)
						}
						return managedKafkaAddon, nil
					},
					CreateSyncSetFunc: func(clusterID string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						return &clustersmgmtv1.Syncset{}, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "apps.example.com", nil
					},
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return nil
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ClusterManager{
				ocmClient:      tt.fields.ocmClient,
				clusterService: tt.fields.clusterService,
				timer:          tt.fields.timer,
				configService: services.NewConfigService(
					config.ApplicationConfig{
						SupportedProviders:         &config.ProviderConfig{},
						AccessControlList:          &config.AccessControlListConfig{},
						ObservabilityConfiguration: &config.ObservabilityConfiguration{},
						OSDClusterConfig:           &config.OSDClusterConfig{},
						OCM:                        &config.OCMConfig{StrimziOperatorAddonID: strimziAddonID},
					},
				),
			}
			_, err := c.reconcileStrimziOperator(api.Cluster{
				ClusterID: "clusterId",
			})
			if (err != nil) != tt.wantErr {
				t.Errorf("reconcileStrimziOperator() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestClusterManager_reconcileAcceptedCluster(t *testing.T) {
	type fields struct {
		providerLst     []string
		clusterService  services.ClusterService
		providersConfig config.ProviderConfig
	}

	tests := []struct {
		name    string
		wantErr bool
		fields  fields
	}{
		{
			name: "reconcile cluster with cluster creation requests",
			fields: fields{
				providerLst: []string{"us-east-1"},
				clusterService: &services.ClusterServiceMock{
					ListGroupByProviderAndRegionFunc: func(providers []string, regions []string, status []string) (m []*services.ResGroupCPRegion, e *ocmErrors.ServiceError) {
						var res []*services.ResGroupCPRegion
						return res, nil
					},
					CreateFunc: func(Cluster *api.Cluster) (cls *v1.Cluster, e *ocmErrors.ServiceError) {
						sample, _ := v1.NewCluster().Build()
						return sample, nil
					},
				},
				providersConfig: config.ProviderConfig{
					ProvidersConfig: config.ProviderConfiguration{
						SupportedProviders: config.ProviderList{
							config.Provider{
								Name: "aws",
								Regions: config.RegionList{
									config.Region{
										Name: "us-east-1",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ClusterManager{
				clusterService: tt.fields.clusterService,
				configService: services.NewConfigService(
					config.ApplicationConfig{
						SupportedProviders:         &tt.fields.providersConfig,
						AccessControlList:          &config.AccessControlListConfig{},
						ObservabilityConfiguration: &config.ObservabilityConfiguration{},
						OSDClusterConfig:           config.NewOSDClusterConfig(),
					}),
			}

			clusterRequest := &api.Cluster{
				Region:        testRegion,
				CloudProvider: testProvider,
				Status:        "cluster_accepted",
			}

			err := c.reconcileAcceptedCluster(clusterRequest)
			if err != nil && !tt.wantErr {
				t.Errorf("reconcileAcceptedCluster() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClusterManager_reconcileClustersForRegions(t *testing.T) {
	type fields struct {
		providerLst          []string
		clusterService       services.ClusterService
		providersConfig      config.ProviderConfig
		dynamicScalingConfig config.DynamicScalingConfig
	}

	tests := []struct {
		name    string
		wantErr bool
		fields  fields
	}{
		{
			name: "creates a missing OSD cluster request automatically",
			fields: fields{
				providerLst: []string{"us-east-1"},
				clusterService: &services.ClusterServiceMock{
					ListGroupByProviderAndRegionFunc: func(providers []string, regions []string, status []string) (m []*services.ResGroupCPRegion, e *ocmErrors.ServiceError) {
						var res []*services.ResGroupCPRegion
						return res, nil
					},
					RegisterClusterJobFunc: func(clusterReq *api.Cluster) *apiErrors.ServiceError {
						return nil
					},
				},
				dynamicScalingConfig: config.DynamicScalingConfig{
					Enabled: true,
				},
				providersConfig: config.ProviderConfig{
					ProvidersConfig: config.ProviderConfiguration{
						SupportedProviders: config.ProviderList{
							config.Provider{
								Name: "aws",
								Regions: config.RegionList{
									config.Region{
										Name: "us-east-1",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "failed to create OSD request",
			fields: fields{
				providerLst: []string{"us-east-1"},
				clusterService: &services.ClusterServiceMock{
					ListGroupByProviderAndRegionFunc: func(providers []string, regions []string, status []string) (m []*services.ResGroupCPRegion, e *ocmErrors.ServiceError) {
						var res []*services.ResGroupCPRegion
						return res, nil
					},
					RegisterClusterJobFunc: func(clusterReq *api.Cluster) *apiErrors.ServiceError {
						return apiErrors.GeneralError("failed to create cluster request")
					},
				},
				dynamicScalingConfig: config.DynamicScalingConfig{
					Enabled: true,
				},
				providersConfig: config.ProviderConfig{
					ProvidersConfig: config.ProviderConfiguration{
						SupportedProviders: config.ProviderList{
							config.Provider{
								Name: "aws",
								Regions: config.RegionList{
									config.Region{
										Name: "us-east-1",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "failed to retrieve OSD cluster info from database",
			fields: fields{
				providerLst: []string{"us-east-1"},
				clusterService: &services.ClusterServiceMock{
					ListGroupByProviderAndRegionFunc: func(providers []string, regions []string, status []string) (m []*services.ResGroupCPRegion, e *ocmErrors.ServiceError) {
						return nil, ocmErrors.New(ocmErrors.ErrorGeneral, "Database retrieval failed")
					},
				},
				dynamicScalingConfig: config.DynamicScalingConfig{
					Enabled: true,
				},
				providersConfig: config.ProviderConfig{
					ProvidersConfig: config.ProviderConfiguration{
						SupportedProviders: config.ProviderList{
							config.Provider{
								Name: "aws",
								Regions: config.RegionList{
									config.Region{
										Name: "us-east-1",
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ClusterManager{
				clusterService: tt.fields.clusterService,
				configService: services.NewConfigService(config.ApplicationConfig{
					SupportedProviders:         &tt.fields.providersConfig,
					AccessControlList:          &config.AccessControlListConfig{},
					ObservabilityConfiguration: &config.ObservabilityConfiguration{},
					OSDClusterConfig:           config.NewOSDClusterConfig(),
				}),
			}
			err := c.reconcileClustersForRegions()
			if err != nil && !tt.wantErr {
				t.Errorf("reconcileClustersForRegions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClusterManager_createSyncSet(t *testing.T) {
	const ingressDNS = "foo.bar.example.com"
	observabilityConfig := buildObservabilityConfig()
	clusterCreateConfig := config.OSDClusterConfig{
		ImagePullDockerConfigContent: "image-pull-secret-test",
		IngressControllerReplicas:    12,
	}

	type fields struct {
		ocmClient           ocm.Client
		timer               *time.Timer
		clusterCreateConfig config.OSDClusterConfig
	}

	type result struct {
		err     error
		syncset func() *clustersmgmtv1.Syncset
	}
	tests := []struct {
		name   string
		fields fields
		want   result
	}{
		{
			name: "throw an error when syncset creation fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					CreateSyncSetFunc: func(clusterId string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						return nil, errors.New("error when creating syncset")
					},
				},
				clusterCreateConfig: clusterCreateConfig,
			},
			want: result{
				err: errors.New("error when creating syncset"),
				syncset: func() *clustersmgmtv1.Syncset {
					return nil
				},
			},
		},
		{
			name: "returns created syncset",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					CreateSyncSetFunc: func(clusterId string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						return syncset, nil
					},
				},
				clusterCreateConfig: clusterCreateConfig,
			},
			want: result{
				err: nil,
				syncset: func() *clustersmgmtv1.Syncset {
					s, _ := buildSyncSet(observabilityConfig, clusterCreateConfig, ingressDNS)
					return s
				},
			},
		},
		{
			name: "check when imagePullSecret is empty",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					CreateSyncSetFunc: func(clusterId string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						return syncset, nil
					},
				},
				clusterCreateConfig: config.OSDClusterConfig{
					ImagePullDockerConfigContent: "",
				},
			},
			want: result{
				err: nil,
				syncset: func() *clustersmgmtv1.Syncset {
					s, _ := buildSyncSet(observabilityConfig, config.OSDClusterConfig{
						ImagePullDockerConfigContent: "",
					}, ingressDNS)
					return s
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterTestingT(t)

			c := &ClusterManager{
				ocmClient: tt.fields.ocmClient,
				timer:     tt.fields.timer,
				configService: services.NewConfigService(config.ApplicationConfig{
					SupportedProviders:         &config.ProviderConfig{},
					AccessControlList:          &config.AccessControlListConfig{},
					ObservabilityConfiguration: &observabilityConfig,
					OSDClusterConfig:           &tt.fields.clusterCreateConfig,
					Kafka:                      &config.KafkaConfig{},
					OCM:                        &config.OCMConfig{StrimziOperatorAddonID: strimziAddonID},
				}),
			}
			wantSyncSet := tt.want.syncset()
			got, err := c.createSyncSet("clusterId", ingressDNS)
			Expect(got).To(Equal(wantSyncSet))
			if err != nil {
				Expect(err.Error()).To(Equal(tt.want.err.Error()))
			}
		})
	}
}

func TestClusterManager_reconcileAddonOperator(t *testing.T) {
	type fields struct {
		ocmClient      ocm.Client
		agentOperator  services.KasFleetshardOperatorAddon
		clusterService services.ClusterService
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "successful strimzi and kas fleetshard operator addon installation",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						return clustersmgmtv1.NewAddOnInstallation().ID(strimziAddonID).State(clustersmgmtv1.AddOnInstallationStateReady).Build()
					},
					CreateAddonFunc: func(clusterId, addonId string) (*clustersmgmtv1.AddOnInstallation, error) {
						return clustersmgmtv1.NewAddOnInstallation().ID(addonId).State(clustersmgmtv1.AddOnInstallationStateInstalling).Build()
					},
				},
				agentOperator: &services.KasFleetshardOperatorAddonMock{
					ProvisionFunc: func(cluster api.Cluster) (bool, *apiErrors.ServiceError) {
						return false, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						if status != api.ClusterWaitingForKasFleetShardOperator {
							t.Errorf("expect status to be %s but got %s", api.ClusterWaitingForKasFleetShardOperator.String(), status)
						}
						return nil
					},
				},
			},
			wantErr: false,
		},
		{
			name: "skip addon installation if addon is already installed",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						return clustersmgmtv1.NewAddOnInstallation().ID(strimziAddonID).State(clustersmgmtv1.AddOnInstallationStateReady).Build()
					},
				},
				agentOperator: &services.KasFleetshardOperatorAddonMock{
					ProvisionFunc: func(cluster api.Cluster) (bool, *apiErrors.ServiceError) {
						return true, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						if status != api.ClusterWaitingForKasFleetShardOperator {
							t.Errorf("expect status to be %s but got %s", api.ClusterWaitingForKasFleetShardOperator.String(), status)
						}
						return nil
					},
				},
			},
			wantErr: false,
		},
		{
			name: "return an error if strimzi installation fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						return &clustersmgmtv1.AddOnInstallation{}, nil
					},
					CreateAddonFunc: func(clusterId, addonId string) (*clustersmgmtv1.AddOnInstallation, error) {
						return &clustersmgmtv1.AddOnInstallation{}, fmt.Errorf("failed to install %s", addonId)
					},
				},
			},
			wantErr: true,
		},
		{
			name: "return an error if kas fleetshard operator installation fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetAddonFunc: func(clusterId string, addonId string) (status *clustersmgmtv1.AddOnInstallation, e error) {
						return clustersmgmtv1.NewAddOnInstallation().ID(strimziAddonID).State(clustersmgmtv1.AddOnInstallationStateReady).Build()
					},
					CreateAddonFunc: func(clusterId, addonId string) (*clustersmgmtv1.AddOnInstallation, error) {
						return clustersmgmtv1.NewAddOnInstallation().ID(addonId).State(clustersmgmtv1.AddOnInstallationStateInstalling).Build()
					},
				},
				agentOperator: &services.KasFleetshardOperatorAddonMock{
					ProvisionFunc: func(cluster api.Cluster) (bool, *apiErrors.ServiceError) {
						return false, apiErrors.GeneralError("failed to provision kas fleetshard operator")
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ClusterManager{
				ocmClient:      tt.fields.ocmClient,
				clusterService: tt.fields.clusterService,
				configService: services.NewConfigService(config.ApplicationConfig{
					OCM: &config.OCMConfig{StrimziOperatorAddonID: strimziAddonID},
				}),
				kasFleetshardOperatorAddon: tt.fields.agentOperator,
			}

			err := c.reconcileAddonOperator(api.Cluster{
				ClusterID: "test-cluster-id",
			})
			if err != nil && !tt.wantErr {
				t.Errorf("reconcileAddonOperator() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClusterManager_reconcileClusterSyncSet(t *testing.T) {
	observabilityConfig := buildObservabilityConfig()
	type fields struct {
		ocmClient      ocm.Client
		clusterService services.ClusterService
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "test should pass and syncset should be created",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetSyncSetFunc: func(clusterID string, syncSetID string) (*clustersmgmtv1.Syncset, error) {
						return nil, apiErrors.NotFound("not found")
					},
					CreateSyncSetFunc: func(clusterID string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						if syncset.ID() == "" {
							return nil, errors.New("syncset ID is empty")
						}
						return &clustersmgmtv1.Syncset{}, nil
					},
					// set to nil deliberately as it should not be called
					UpdateSyncSetFunc: nil,
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
				},
			},
		},
		{
			name: "test should pass and syncset should be updated",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetSyncSetFunc: func(clusterID string, syncSetID string) (*clustersmgmtv1.Syncset, error) {
						syncset, _ := clustersmgmtv1.NewSyncset().Resources(observabilityConfig).Build()
						return syncset, nil
					},
					// set to nil deliberately as it should not be called
					CreateSyncSetFunc: nil,
					UpdateSyncSetFunc: func(clusterID string, syncSetID string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						if syncset.ID() != "" {
							return nil, errors.New("syncset ID is not empty")
						}
						return &clustersmgmtv1.Syncset{}, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
				},
			},
		},
		{
			name: "should receive error when GetClusterDNSFunc returns error",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetSyncSetFunc:    nil,
					CreateSyncSetFunc: nil,
					UpdateSyncSetFunc: nil,
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "", apiErrors.GeneralError("failed")
					},
				},
			},
			wantErr: true,
		},
		{
			name: "should receive error when CreateSyncSetFunc returns error",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetSyncSetFunc: func(clusterID string, syncSetID string) (*clustersmgmtv1.Syncset, error) {
						return nil, apiErrors.NotFound("not found")
					},
					CreateSyncSetFunc: func(clusterID string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						return nil, apiErrors.GeneralError("failed")
					},
					// set to nil deliberately as it should not be called
					UpdateSyncSetFunc: nil,
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
				},
			},
			wantErr: true,
		},
		{
			name: "should receive error when UpdateSyncSetFunc returns error",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetSyncSetFunc: func(clusterID string, syncSetID string) (*clustersmgmtv1.Syncset, error) {
						return nil, nil
					},
					// set to nil deliberately as it should not be called
					CreateSyncSetFunc: nil,
					UpdateSyncSetFunc: func(clusterID string, syncSetID string, syncset *clustersmgmtv1.Syncset) (*clustersmgmtv1.Syncset, error) {
						return nil, apiErrors.GeneralError("failed")
					},
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ClusterManager{
				ocmClient:      tt.fields.ocmClient,
				clusterService: tt.fields.clusterService,
				configService: services.NewConfigService(config.ApplicationConfig{
					SupportedProviders:         &config.ProviderConfig{},
					AccessControlList:          &config.AccessControlListConfig{},
					ObservabilityConfiguration: &observabilityConfig,
					OSDClusterConfig:           &config.OSDClusterConfig{},
					Kafka:                      &config.KafkaConfig{},
				}),
			}

			err := c.reconcileClusterSyncSet(api.Cluster{ClusterID: "test-cluster-id"})
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestClusterManager_reconcileClusterIdentityProvider(t *testing.T) {
	type fields struct {
		ocmClient             ocm.Client
		clusterService        services.ClusterService
		osdIdpKeycloakService services.KeycloakService
	}
	tests := []struct {
		name    string
		fields  fields
		arg     api.Cluster
		wantErr bool
	}{
		{
			name: "should receive error when GetClusterDNSFunc returns error",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					GetSyncSetFunc:    nil,
					CreateSyncSetFunc: nil,
					UpdateSyncSetFunc: nil,
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "", apiErrors.GeneralError("failed")
					},
				},
			},
			wantErr: true,
		},
		{
			name: "should receive an error when creating the the OSD cluster IDP in keycloak fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					CreateIdentityProviderFunc: nil, // setting it to nil because it should be called
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
				},
				osdIdpKeycloakService: &services.KeycloakServiceMock{
					RegisterOSDClusterClientInSSOFunc: func(clusterId, clusterOathCallbackURI string) (string, *apiErrors.ServiceError) {
						return "", apiErrors.FailedToCreateSSOClient("failure")
					},
					GetRealmConfigFunc: nil, // setting it to nill so that it is not called
				},
			},
			arg: api.Cluster{
				Meta: api.Meta{
					ID: "cluster-id",
				},
			},
			wantErr: true,
		},
		{
			name: "should receive error when creating the identity provider throws an error during creation",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					CreateIdentityProviderFunc: func(clusterID string, identityProvider *clustersmgmtv1.IdentityProvider) (*clustersmgmtv1.IdentityProvider, error) {
						return identityProvider, fmt.Errorf("some error")
					},
					GetIdentityProviderListFunc: func(clusterID string) (*clustersmgmtv1.IdentityProviderList, error) {
						return nil, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
				},
				osdIdpKeycloakService: &services.KeycloakServiceMock{
					RegisterOSDClusterClientInSSOFunc: func(clusterId, clusterOathCallbackURI string) (string, *apiErrors.ServiceError) {
						return "secret", nil
					},
					GetRealmConfigFunc: func() *config.KeycloakRealmConfig {
						return &config.KeycloakRealmConfig{
							ValidIssuerURI: "https://foo.bar",
						}
					},
				},
			},
			wantErr: true,
		},
		{
			name: "should create an identity provider when cluster identity provider has not been set",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					CreateIdentityProviderFunc: func(clusterID string, identityProvider *clustersmgmtv1.IdentityProvider) (*clustersmgmtv1.IdentityProvider, error) {
						return identityProvider, nil
					},
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
					UpdateFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return nil
					},
				},
				osdIdpKeycloakService: &services.KeycloakServiceMock{
					RegisterOSDClusterClientInSSOFunc: func(clusterId, clusterOathCallbackURI string) (string, *apiErrors.ServiceError) {
						return "secret", nil
					},
					GetRealmConfigFunc: func() *config.KeycloakRealmConfig {
						return &config.KeycloakRealmConfig{
							ValidIssuerURI: "https://foo.bar",
						}
					},
				},
			},
			arg: api.Cluster{
				Meta: api.Meta{
					ID: "cluster-id",
				},
			},
		},
		{
			name: "should update identity provider from the identity providers list if the identity provider has already been already created in cluster service",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					CreateIdentityProviderFunc: func(clusterID string, identityProvider *clustersmgmtv1.IdentityProvider) (*clustersmgmtv1.IdentityProvider, error) {
						return nil, fmt.Errorf(idpAlreadyCreatedErrorToCheck)
					},
					GetIdentityProviderListFunc: func(clusterID string) (*clustersmgmtv1.IdentityProviderList, error) {
						idp := clustersmgmtv1.NewIdentityProvider().Name(openIDIdentityProviderName).ID("test idp")
						return clustersmgmtv1.NewIdentityProviderList().Items(idp).Build()
					},
				},
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
					UpdateFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return nil
					},
				},
				osdIdpKeycloakService: &services.KeycloakServiceMock{
					RegisterOSDClusterClientInSSOFunc: func(clusterId, clusterOathCallbackURI string) (string, *apiErrors.ServiceError) {
						return "secret", nil
					},
					GetRealmConfigFunc: func() *config.KeycloakRealmConfig {
						return &config.KeycloakRealmConfig{
							ValidIssuerURI: "https://foo.bar",
						}
					},
				},
			},
			arg: api.Cluster{
				Meta: api.Meta{
					ID: "cluster-id",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gomega.RegisterTestingT(t)
			c := &ClusterManager{
				ocmClient:             tt.fields.ocmClient,
				clusterService:        tt.fields.clusterService,
				osdIdpKeycloakService: tt.fields.osdIdpKeycloakService,
			}

			err := c.reconcileClusterIdentityProvider(tt.arg)
			gomega.Expect(err != nil).To(Equal(tt.wantErr))
		})
	}
}

func TestClusterManager_reconcileClusterDNS(t *testing.T) {
	type fields struct {
		clusterService services.ClusterService
	}
	tests := []struct {
		name    string
		fields  fields
		arg     api.Cluster
		wantErr bool
	}{
		{
			name: "should return when clusterDNS is already set",
			arg: api.Cluster{
				ClusterDNS: "my-cluster-dns",
			},
			wantErr: false,
		},
		{
			name: "should receive error when GetClusterDNSFunc returns error",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "", apiErrors.GeneralError("failed")
					},
				},
			},
			wantErr: true,
		},
		{
			name: "should receive error when cluster service Update returns error",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					UpdateFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return apiErrors.GeneralError("failed")
					},
					GetClusterDNSFunc: func(clusterID string) (string, *apiErrors.ServiceError) {
						return "test.com", nil
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gomega.RegisterTestingT(t)
			c := &ClusterManager{
				clusterService: tt.fields.clusterService,
			}

			err := c.reconcileClusterDNS(tt.arg)
			gomega.Expect(err != nil).To(Equal(tt.wantErr))
		})
	}
}

func TestClusterManager_reconcileDeprovisioningCluster(t *testing.T) {
	type fields struct {
		clusterService services.ClusterService
		ocmClient      ocm.Client
		configService  services.ConfigService
	}
	tests := []struct {
		name    string
		fields  fields
		arg     api.Cluster
		wantErr bool
	}{
		{
			name: "should receive error when FindCluster to retrieve sibling cluster returns error",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindClusterFunc: func(criteria services.FindClusterCriteria) (*api.Cluster, *apiErrors.ServiceError) {
						return nil, &apiErrors.ServiceError{}
					},
					UpdateStatusFunc: nil, // set to nil as it should not be called
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: &config.OSDClusterConfig{
						DataPlaneClusterScalingType: "auto",
					},
				}),
			},
			wantErr: true,
		},
		{
			name: "should update the status back to ready when no sibling cluster found",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindClusterFunc: func(criteria services.FindClusterCriteria) (*api.Cluster, *apiErrors.ServiceError) {
						return nil, nil
					},
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return nil
					},
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: &config.OSDClusterConfig{
						DataPlaneClusterScalingType: "auto",
					},
				}),
			},
			wantErr: false,
		},
		{
			name: "recieves an error when delete OCM cluster fails",
			fields: fields{
				ocmClient: &ocm.ClientMock{
					DeleteClusterFunc: func(clusterID string) (int, error) {
						return 500, fmt.Errorf("ocm Error")
					},
				},
				clusterService: &services.ClusterServiceMock{
					FindClusterFunc: func(criteria services.FindClusterCriteria) (*api.Cluster, *apiErrors.ServiceError) {
						return &api.Cluster{ClusterID: "dummy cluster"}, nil
					},
					UpdateStatusFunc: nil,
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: &config.OSDClusterConfig{
						DataPlaneClusterScalingType: "auto",
					},
				}),
			},
			wantErr: true,
		},
		{
			name: "successful deletion of an OSD cluster when auto configuration is enabled",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindClusterFunc: func(criteria services.FindClusterCriteria) (*api.Cluster, *apiErrors.ServiceError) {
						return &api.Cluster{ClusterID: "dummy cluster"}, nil
					},
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return nil
					},
				},
				ocmClient: &ocm.ClientMock{
					DeleteClusterFunc: func(clusterID string) (int, error) {
						return 404, nil
					},
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: &config.OSDClusterConfig{
						DataPlaneClusterScalingType: "auto",
					},
				}),
			},
			wantErr: false,
		},
		{
			name: "successful deletion of an OSD cluster when manual configuration is enabled",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindClusterFunc: nil, // should not be called
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return nil
					},
				},
				ocmClient: &ocm.ClientMock{
					DeleteClusterFunc: func(clusterID string) (int, error) {
						return 404, nil
					},
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: &config.OSDClusterConfig{
						DataPlaneClusterScalingType: "manual",
					},
				}),
			},
			wantErr: false,
		},
		{
			name: "receives an error when the update status fails",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return fmt.Errorf("Some errors")
					},
				},
				ocmClient: &ocm.ClientMock{
					DeleteClusterFunc: func(clusterID string) (int, error) {
						return 404, nil
					},
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: &config.OSDClusterConfig{
						DataPlaneClusterScalingType: "manual",
					},
				}),
			},
			wantErr: true,
		},
		{
			name: "does not update cluster status when cluster has not been fully deleted from ClusterService",
			fields: fields{
				clusterService: nil, // should not be called
				ocmClient: &ocm.ClientMock{
					DeleteClusterFunc: func(clusterID string) (int, error) {
						return 204, nil // deletion request accepted
					},
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: &config.OSDClusterConfig{
						DataPlaneClusterScalingType: "manual",
					},
				}),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gomega.RegisterTestingT(t)
			c := &ClusterManager{
				clusterService: tt.fields.clusterService,
				ocmClient:      tt.fields.ocmClient,
				configService:  tt.fields.configService,
			}

			err := c.reconcileDeprovisioningCluster(tt.arg)
			gomega.Expect(err != nil).To(Equal(tt.wantErr))
		})
	}
}

func TestClusterManager_reconcileCleanupCluster(t *testing.T) {
	type fields struct {
		clusterService             services.ClusterService
		osdIDPKeycloakService      services.KeycloakService
		kasFleetshardOperatorAddon services.KasFleetshardOperatorAddon
	}
	tests := []struct {
		name    string
		fields  fields
		arg     api.Cluster
		wantErr bool
	}{

		{
			name: "recieves an error when deregistering the OSD IDP from keycloak fails",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					DeleteByClusterIDFunc: func(clusterID string) *apiErrors.ServiceError {
						return nil
					},
				},
				osdIDPKeycloakService: &services.KeycloakServiceMock{
					DeRegisterClientInSSOFunc: func(kafkaNamespace string) *apiErrors.ServiceError {
						return &apiErrors.ServiceError{}
					},
				},
			},
			wantErr: true,
		},
		{
			name: "recieves an error when remove kas-fleetshard-operator service account fails",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return nil
					},
				},
				osdIDPKeycloakService: &services.KeycloakServiceMock{
					DeRegisterClientInSSOFunc: func(kafkaNamespace string) *apiErrors.ServiceError {
						return nil
					},
				},
				kasFleetshardOperatorAddon: &services.KasFleetshardOperatorAddonMock{
					RemoveServiceAccountFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return &apiErrors.ServiceError{}
					},
				},
			},
			wantErr: true,
		},
		{
			name: "recieves an error when soft delete cluster from database fails",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					DeleteByClusterIDFunc: func(clusterID string) *apiErrors.ServiceError {
						return &apiErrors.ServiceError{}
					},
				},
				osdIDPKeycloakService: &services.KeycloakServiceMock{
					DeRegisterClientInSSOFunc: func(kafkaNamespace string) *apiErrors.ServiceError {
						return nil
					},
				},
				kasFleetshardOperatorAddon: &services.KasFleetshardOperatorAddonMock{
					RemoveServiceAccountFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return nil
					},
				},
			},
			wantErr: true,
		},
		{
			name: "successfull deletion of an OSD cluster",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					DeleteByClusterIDFunc: func(clusterID string) *apiErrors.ServiceError {
						return nil
					},
				},
				osdIDPKeycloakService: &services.KeycloakServiceMock{
					DeRegisterClientInSSOFunc: func(kafkaNamespace string) *apiErrors.ServiceError {
						return nil
					},
				},
				kasFleetshardOperatorAddon: &services.KasFleetshardOperatorAddonMock{
					RemoveServiceAccountFunc: func(cluster api.Cluster) *apiErrors.ServiceError {
						return nil
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gomega.RegisterTestingT(t)
			c := &ClusterManager{
				clusterService:             tt.fields.clusterService,
				osdIdpKeycloakService:      tt.fields.osdIDPKeycloakService,
				kasFleetshardOperatorAddon: tt.fields.kasFleetshardOperatorAddon,
			}

			err := c.reconcileCleanupCluster(tt.arg)
			gomega.Expect(err != nil).To(Equal(tt.wantErr))
		})
	}
}

func TestClusterManager_reconcileEmptyCluster(t *testing.T) {
	type fields struct {
		clusterService services.ClusterService
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
		want    bool
	}{
		{
			name: "should receive error when FindNonEmptyClusterById returns error",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindNonEmptyClusterByIdFunc: func(clusterId string) (*api.Cluster, *apiErrors.ServiceError) {
						return nil, &apiErrors.ServiceError{}
					},
					UpdateStatusFunc:                 nil, // set to nil as it should not be called
					ListGroupByProviderAndRegionFunc: nil, // set to nil as it should not be called
				},
			},
			wantErr: true,
			want:    false,
		},
		{
			name: "should return false when cluster is not empty",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindNonEmptyClusterByIdFunc: func(clusterId string) (*api.Cluster, *apiErrors.ServiceError) {
						return &api.Cluster{ClusterID: clusterId}, nil
					},
					UpdateStatusFunc:                 nil, // set to nil as it should not be called
					ListGroupByProviderAndRegionFunc: nil, // set to nil as it should not be called
				},
			},
			wantErr: false,
			want:    false,
		},
		{
			name: "receives an error when ListGroupByProviderAndRegion returns an error",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindNonEmptyClusterByIdFunc: func(clusterId string) (*api.Cluster, *apiErrors.ServiceError) {
						return nil, nil
					},
					UpdateStatusFunc: nil, // set to nil as it should not be called
					ListGroupByProviderAndRegionFunc: func(providers, regions, status []string) ([]*services.ResGroupCPRegion, *apiErrors.ServiceError) {
						return nil, &apiErrors.ServiceError{}
					},
				},
			},
			wantErr: true,
			want:    false,
		},
		{
			name: "should not update the cluster status to deprovisioning when no sibling found",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindNonEmptyClusterByIdFunc: func(clusterId string) (*api.Cluster, *apiErrors.ServiceError) {
						return nil, nil
					},
					UpdateStatusFunc: nil, // set to nil as it not be called
					ListGroupByProviderAndRegionFunc: func(providers, regions, status []string) ([]*services.ResGroupCPRegion, *apiErrors.ServiceError) {
						return []*services.ResGroupCPRegion{{Count: 1}}, nil
					},
				},
			},
			wantErr: false,
			want:    false,
		},
		{
			name: "should return false when updating cluster status fails",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindNonEmptyClusterByIdFunc: func(clusterId string) (*api.Cluster, *apiErrors.ServiceError) {
						return nil, nil
					},
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return &apiErrors.ServiceError{}
					},
					ListGroupByProviderAndRegionFunc: func(providers, regions, status []string) ([]*services.ResGroupCPRegion, *apiErrors.ServiceError) {
						return []*services.ResGroupCPRegion{{Count: 2}}, nil
					},
				},
			},
			wantErr: true,
			want:    false,
		},
		{
			name: "should return true and update the cluster status",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					FindNonEmptyClusterByIdFunc: func(clusterId string) (*api.Cluster, *apiErrors.ServiceError) {
						return nil, nil
					},
					UpdateStatusFunc: func(cluster api.Cluster, status api.ClusterStatus) error {
						return nil
					},
					ListGroupByProviderAndRegionFunc: func(providers, regions, status []string) ([]*services.ResGroupCPRegion, *apiErrors.ServiceError) {
						return []*services.ResGroupCPRegion{{Count: 2}}, nil
					},
				},
			},
			wantErr: false,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gomega.RegisterTestingT(t)
			c := &ClusterManager{
				clusterService: tt.fields.clusterService,
			}

			emptyClusterReconciled, err := c.reconcileEmptyCluster(api.Cluster{
				Meta: api.Meta{
					ID: "cluster-id",
				},
			})
			gomega.Expect(err != nil).To(Equal(tt.wantErr))
			gomega.Expect(emptyClusterReconciled).To(Equal(tt.want))
		})
	}
}

func TestSyncsetResourcesChanged(t *testing.T) {
	tests := []struct {
		name              string
		existingResources []interface{}
		newResources      []interface{}
		changed           bool
	}{
		{
			name: "resources should match",
			existingResources: []interface{}{
				map[string]interface{}{
					"kind":       "Project",
					"apiVersion": "project.openshift.io/v1",
					"metadata": map[string]string{
						"name": observabilityNamespace,
					},
				},
				map[string]interface{}{
					"kind":       "OperatorGroup",
					"apiVersion": "operators.coreos.com/v1alpha2",
					"metadata": map[string]string{
						"name":      observabilityOperatorGroupName,
						"namespace": observabilityNamespace,
					},
					"spec": map[string]interface{}{
						"targetNamespaces": []string{observabilityNamespace},
					},
				},
			},
			newResources: []interface{}{
				&projectv1.Project{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "project.openshift.io/v1",
						Kind:       "Project",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: observabilityNamespace,
					},
				},
				&v1alpha2.OperatorGroup{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "operators.coreos.com/v1alpha2",
						Kind:       "OperatorGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      observabilityOperatorGroupName,
						Namespace: observabilityNamespace,
					},
					Spec: v1alpha2.OperatorGroupSpec{
						TargetNamespaces: []string{observabilityNamespace},
					},
				},
			},
			changed: false,
		},
		{
			name: "resources should not match as lengths of resources are different",
			existingResources: []interface{}{
				map[string]interface{}{
					"kind":       "Project",
					"apiVersion": "project.openshift.io/v1",
					"metadata": map[string]string{
						"name": observabilityNamespace,
					},
				},
			},
			newResources: []interface{}{
				&projectv1.Project{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "project.openshift.io/v1",
						Kind:       "Project",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: observabilityNamespace,
					},
				},
				&v1alpha2.OperatorGroup{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "operators.coreos.com/v1alpha2",
						Kind:       "OperatorGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      observabilityOperatorGroupName,
						Namespace: observabilityNamespace,
					},
					Spec: v1alpha2.OperatorGroupSpec{
						TargetNamespaces: []string{observabilityNamespace},
					},
				},
			},
			changed: true,
		},
		{
			name: "resources should not match as some field values are changed",
			existingResources: []interface{}{
				map[string]interface{}{
					"kind":       "OperatorGroup",
					"apiVersion": "operators.coreos.com/v1alpha2",
					"metadata": map[string]string{
						"name":      observabilityOperatorGroupName + "updated",
						"namespace": observabilityNamespace,
					},
					"spec": map[string]interface{}{
						"targetNamespaces": []string{observabilityNamespace},
					},
				},
			},
			newResources: []interface{}{
				&v1alpha2.OperatorGroup{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "operators.coreos.com/v1alpha2",
						Kind:       "OperatorGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      observabilityOperatorGroupName,
						Namespace: observabilityNamespace,
					},
					Spec: v1alpha2.OperatorGroupSpec{
						TargetNamespaces: []string{observabilityNamespace},
					},
				},
			},
			changed: true,
		},
		{
			name: "resources should not match as type can not be converted",
			existingResources: []interface{}{
				map[string]interface{}{
					"kind":       "TestProject",
					"apiVersion": "testproject.openshift.io/v1",
					"metadata": map[string]string{
						"name": observabilityNamespace,
					},
				},
			},
			newResources: []interface{}{
				&projectv1.Project{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "project.openshift.io/v1",
						Kind:       "Project",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: observabilityNamespace,
					},
				},
			},
			changed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existingSyncset, _ := clustersmgmtv1.NewSyncset().Resources(tt.existingResources...).Build()
			newSyncset, _ := clustersmgmtv1.NewSyncset().Resources(tt.newResources...).Build()
			result := syncsetResourcesChanged(existingSyncset, newSyncset)
			if result != tt.changed {
				t.Errorf("result does not match expected value. result = %v and expected = %v", result, tt.changed)
			}
		})
	}
}

// buildObservabilityConfig builds a observability config used for testing
func buildObservabilityConfig() config.ObservabilityConfiguration {
	observabilityConfig := config.ObservabilityConfiguration{
		DexUrl:                         "dex-url",
		DexPassword:                    "dex-password",
		DexUsername:                    "dex-username",
		DexSecret:                      "dex-secret",
		ObservatoriumTenant:            "tenant",
		ObservatoriumGateway:           "gateway",
		ObservabilityConfigRepo:        "obs-config-repo",
		ObservabilityConfigChannel:     "obs-config-channel",
		ObservabilityConfigAccessToken: "obs-config-token",
		ObservabilityConfigTag:         "obs-config-tag",
	}
	return observabilityConfig
}

// buildSyncSet builds a syncset used for testing
func buildSyncSet(observabilityConfig config.ObservabilityConfiguration, clusterCreateConfig config.OSDClusterConfig, ingressDNS string) (*clustersmgmtv1.Syncset, error) {
	reclaimDelete := k8sCoreV1.PersistentVolumeReclaimDelete
	expansion := true
	consumer := storagev1.VolumeBindingWaitForFirstConsumer
	r := int32(clusterCreateConfig.IngressControllerReplicas)
	resources := []interface{}{
		&storagev1.StorageClass{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "storage.k8s.io/v1",
				Kind:       "StorageClass",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: KafkaStorageClass,
			},
			Parameters: map[string]string{
				"encrypted": "false",
				"type":      "gp2",
			},
			Provisioner:          "kubernetes.io/aws-ebs",
			ReclaimPolicy:        &reclaimDelete,
			AllowVolumeExpansion: &expansion,
			VolumeBindingMode:    &consumer,
		},
		&ingressoperatorv1.IngressController{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "operator.openshift.io/v1",
				Kind:       "IngressController",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sharded-nlb",
				Namespace: openshiftIngressNamespace,
			},
			Spec: ingressoperatorv1.IngressControllerSpec{
				Domain: ingressDNS,
				RouteSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						IngressLabelName: IngressLabelValue,
					},
				},
				EndpointPublishingStrategy: &ingressoperatorv1.EndpointPublishingStrategy{
					LoadBalancer: &ingressoperatorv1.LoadBalancerStrategy{
						ProviderParameters: &ingressoperatorv1.ProviderLoadBalancerParameters{
							AWS: &ingressoperatorv1.AWSLoadBalancerParameters{
								Type: ingressoperatorv1.AWSNetworkLoadBalancer,
							},
							Type: ingressoperatorv1.AWSLoadBalancerProvider,
						},
						Scope: ingressoperatorv1.ExternalLoadBalancer,
					},
					Type: ingressoperatorv1.LoadBalancerServiceStrategyType,
				},
				Replicas: &r,
				NodePlacement: &ingressoperatorv1.NodePlacement{
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"node-role.kubernetes.io/worker": "",
						},
					},
				},
			},
		},
		&projectv1.Project{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "project.openshift.io/v1",
				Kind:       "Project",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: observabilityNamespace,
			},
		},
		&k8sCoreV1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: metav1.SchemeGroupVersion.Version,
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      observabilityDexCredentials,
				Namespace: observabilityNamespace,
			},
			Type: k8sCoreV1.SecretTypeOpaque,
			StringData: map[string]string{
				"password": observabilityConfig.DexPassword,
				"secret":   observabilityConfig.DexSecret,
				"username": observabilityConfig.DexUsername,
			},
		},
		&v1alpha1.CatalogSource{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "operators.coreos.com/v1alpha1",
				Kind:       "CatalogSource",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      observabilityCatalogSourceName,
				Namespace: observabilityNamespace,
			},
			Spec: v1alpha1.CatalogSourceSpec{
				SourceType: v1alpha1.SourceTypeGrpc,
				Image:      observabilityCatalogSourceImage,
			},
		},
		&v1alpha2.OperatorGroup{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "operators.coreos.com/v1alpha2",
				Kind:       "OperatorGroup",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      observabilityOperatorGroupName,
				Namespace: observabilityNamespace,
			},
			Spec: v1alpha2.OperatorGroupSpec{
				TargetNamespaces: []string{observabilityNamespace},
			},
		},
		&v1alpha1.Subscription{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "operators.coreos.com/v1alpha1",
				Kind:       "Subscription",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      observabilitySubscriptionName,
				Namespace: observabilityNamespace,
			},
			Spec: &v1alpha1.SubscriptionSpec{
				CatalogSource:          observabilityCatalogSourceName,
				Channel:                "alpha",
				CatalogSourceNamespace: observabilityNamespace,
				StartingCSV:            "observability-operator.v3.0.1",
				InstallPlanApproval:    v1alpha1.ApprovalAutomatic,
				Package:                observabilitySubscriptionName,
			},
		},
		&userv1.Group{
			TypeMeta: metav1.TypeMeta{
				APIVersion: userv1.SchemeGroupVersion.String(),
				Kind:       "Group",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: readOnlyGroupName,
			},
		},
		&authv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: mkReadOnlyRoleBindingName,
			},
			Subjects: []k8sCoreV1.ObjectReference{
				{
					Kind:       "Group",
					APIVersion: "rbac.authorization.k8s.io",
					Name:       readOnlyGroupName,
				},
			},
			RoleRef: k8sCoreV1.ObjectReference{
				Kind:       "ClusterRole",
				Name:       dedicatedReadersRoleBindingName,
				APIVersion: "rbac.authorization.k8s.io",
			},
		},
	}
	if clusterCreateConfig.ImagePullDockerConfigContent != "" {
		resources = append(resources, &k8sCoreV1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: metav1.SchemeGroupVersion.Version,
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      imagePullSecretName,
				Namespace: strimziAddonNamespace,
			},
			Type: k8sCoreV1.SecretTypeDockercfg,
			Data: map[string][]byte{
				k8sCoreV1.DockerConfigKey: []byte(clusterCreateConfig.ImagePullDockerConfigContent),
			},
		},
			&k8sCoreV1.Secret{
				TypeMeta: metav1.TypeMeta{
					APIVersion: metav1.SchemeGroupVersion.Version,
					Kind:       "Secret",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      imagePullSecretName,
					Namespace: kasFleetshardAddonNamespace,
				},
				Type: k8sCoreV1.SecretTypeDockercfg,
				Data: map[string][]byte{
					k8sCoreV1.DockerConfigKey: []byte(clusterCreateConfig.ImagePullDockerConfigContent),
				},
			})
	}

	syncset, err := clustersmgmtv1.NewSyncset().
		ID(syncsetName).
		Resources(resources...).
		Build()
	return syncset, err
}

func TestClusterManager_reconcileClusterWithManualConfig(t *testing.T) {
	type fields struct {
		clusterService services.ClusterService
		configService  services.ConfigService
	}
	testOsdConfig := config.NewOSDClusterConfig()
	testOsdConfig.ClusterConfig = config.NewClusterConfig(config.ClusterList{config.ManualCluster{Schedulable: true, KafkaInstanceLimit: 2}})
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "Successfully applies manually configurated Cluster",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					ListAllClusterIdsFunc: func() ([]api.Cluster, *apiErrors.ServiceError) {
						var list []api.Cluster
						list = append(list, api.Cluster{ClusterID: "test02"})
						return list, nil
					},
					RegisterClusterJobFunc: func(clusterReq *api.Cluster) *apiErrors.ServiceError {
						return nil
					},
					UpdateMultiClusterStatusFunc: func(clusterIds []string, status api.ClusterStatus) *apiErrors.ServiceError {
						return nil
					},
					FindKafkaInstanceCountFunc: func(clusterIDs []string) ([]services.ResKafkaInstanceCount, *apiErrors.ServiceError) {
						return []services.ResKafkaInstanceCount{
							{
								Clusterid: "test02",
								Count:     1,
							},
						}, nil
					},
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: testOsdConfig,
				}),
			},
			wantErr: false,
		},
		{
			name: "Failed to apply manually configurated Cluster",
			fields: fields{
				clusterService: &services.ClusterServiceMock{
					ListAllClusterIdsFunc: func() ([]api.Cluster, *apiErrors.ServiceError) {
						return nil, &apiErrors.ServiceError{}
					},
					RegisterClusterJobFunc: func(clusterReq *api.Cluster) *apiErrors.ServiceError {
						return nil
					},
					UpdateMultiClusterStatusFunc: func(clusterIds []string, status api.ClusterStatus) *apiErrors.ServiceError {
						return nil
					},
					FindKafkaInstanceCountFunc: func(clusterIDs []string) ([]services.ResKafkaInstanceCount, *apiErrors.ServiceError) {
						return []services.ResKafkaInstanceCount{}, nil
					},
				},
				configService: services.NewConfigService(config.ApplicationConfig{
					OSDClusterConfig: testOsdConfig,
				}),
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ClusterManager{
				configService:  tt.fields.configService,
				clusterService: tt.fields.clusterService,
			}
			if err := c.reconcileClusterWithManualConfig(); (err != nil) != tt.wantErr {
				t.Errorf("reconcileClusterWithManualConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
