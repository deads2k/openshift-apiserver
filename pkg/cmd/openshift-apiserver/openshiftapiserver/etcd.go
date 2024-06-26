package openshiftapiserver

import (
	"strconv"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/server/options"
	apiserverstorage "k8s.io/apiserver/pkg/server/storage"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
	"k8s.io/kubernetes/pkg/api/legacyscheme"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/openshift-apiserver/pkg/cmd/openshift-apiserver/openshiftapiserver/configprocessing"
)

func ToEtcdOptions(startingFlags map[string][]string, storageConfig configv1.EtcdStorageConfig) (*options.EtcdOptions, error) {
	var err error
	targetRAMMB := 0
	if targetRamString := startingFlags["target-ram-mb"]; len(targetRamString) == 1 {
		targetRAMMB, err = strconv.Atoi(targetRamString[0])
		if err != nil {
			return nil, err
		}
	}

	return configprocessing.GetEtcdOptions(
		startingFlags,
		storageConfig,
		newHeuristicWatchCacheSizes(targetRAMMB),
	)
}

func NewStorageFactory(etcdOptions *options.EtcdOptions) serverstorage.StorageFactory {
	return apiserverstorage.NewDefaultStorageFactory(
		etcdOptions.StorageConfig,
		etcdOptions.DefaultStorageMediaType,
		legacyscheme.Codecs,
		apiserverstorage.NewDefaultResourceEncodingConfig(legacyscheme.Scheme),
		&serverstorage.ResourceConfig{},
		specialDefaultResourcePrefixes,
	)
}

// newHeuristicWatchCacheSizes returns a map of suggested watch cache sizes based on total
// memory. It reuses the upstream heuristic and adds OpenShift specific resources.
func newHeuristicWatchCacheSizes(expectedRAMCapacityMB int) map[schema.GroupResource]int {
	// TODO: Revisit this heuristic, copied from upstream
	clusterSize := expectedRAMCapacityMB / 60

	// default enable watch caches for resources that will have a high number of clients accessing it
	// and where the write rate may be significant
	watchCacheSizes := make(map[schema.GroupResource]int)
	watchCacheSizes[schema.GroupResource{Group: "network.openshift.io", Resource: "hostsubnets"}] = maxInt(5*clusterSize, 100)
	watchCacheSizes[schema.GroupResource{Group: "network.openshift.io", Resource: "netnamespaces"}] = maxInt(5*clusterSize, 100)
	watchCacheSizes[schema.GroupResource{Group: "network.openshift.io", Resource: "egressnetworkpolicies"}] = maxInt(10*clusterSize, 100)
	return watchCacheSizes
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// specialDefaultResourcePrefixes are prefixes compiled into Kubernetes.
var specialDefaultResourcePrefixes = map[schema.GroupResource]string{
	{Resource: "clusterpolicies"}:                                            "authorization/cluster/policies",
	{Resource: "clusterpolicies", Group: "authorization.openshift.io"}:       "authorization/cluster/policies",
	{Resource: "clusterpolicybindings"}:                                      "authorization/cluster/policybindings",
	{Resource: "clusterpolicybindings", Group: "authorization.openshift.io"}: "authorization/cluster/policybindings",
	{Resource: "policies"}:                                                   "authorization/local/policies",
	{Resource: "policies", Group: "authorization.openshift.io"}:              "authorization/local/policies",
	{Resource: "policybindings"}:                                             "authorization/local/policybindings",
	{Resource: "policybindings", Group: "authorization.openshift.io"}:        "authorization/local/policybindings",

	{Resource: "oauthaccesstokens"}:                                      "oauth/accesstokens",
	{Resource: "oauthaccesstokens", Group: "oauth.openshift.io"}:         "oauth/accesstokens",
	{Resource: "oauthauthorizetokens"}:                                   "oauth/authorizetokens",
	{Resource: "oauthauthorizetokens", Group: "oauth.openshift.io"}:      "oauth/authorizetokens",
	{Resource: "oauthclients"}:                                           "oauth/clients",
	{Resource: "oauthclients", Group: "oauth.openshift.io"}:              "oauth/clients",
	{Resource: "oauthclientauthorizations"}:                              "oauth/clientauthorizations",
	{Resource: "oauthclientauthorizations", Group: "oauth.openshift.io"}: "oauth/clientauthorizations",

	{Resource: "identities"}:                             "useridentities",
	{Resource: "identities", Group: "user.openshift.io"}: "useridentities",

	{Resource: "clusternetworks"}:                                      "registry/sdnnetworks",
	{Resource: "clusternetworks", Group: "network.openshift.io"}:       "registry/sdnnetworks",
	{Resource: "egressnetworkpolicies"}:                                "registry/egressnetworkpolicy",
	{Resource: "egressnetworkpolicies", Group: "network.openshift.io"}: "registry/egressnetworkpolicy",
	{Resource: "hostsubnets"}:                                          "registry/sdnsubnets",
	{Resource: "hostsubnets", Group: "network.openshift.io"}:           "registry/sdnsubnets",
	{Resource: "netnamespaces"}:                                        "registry/sdnnetnamespaces",
	{Resource: "netnamespaces", Group: "network.openshift.io"}:         "registry/sdnnetnamespaces",
}
