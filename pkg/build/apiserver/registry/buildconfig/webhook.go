package buildconfig

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	kubetypedclient "k8s.io/client-go/kubernetes/typed/core/v1"
	kapi "k8s.io/kubernetes/pkg/apis/core"

	"github.com/openshift/api/build"
	buildv1 "github.com/openshift/api/build/v1"
	buildclienttyped "github.com/openshift/client-go/build/clientset/versioned/typed/build/v1"

	buildapi "github.com/openshift/openshift-apiserver/pkg/build/apis/build"
	buildv1helpers "github.com/openshift/openshift-apiserver/pkg/build/apis/build/v1"
	"github.com/openshift/openshift-apiserver/pkg/build/apiserver/webhook"
)

var (
	webhookEncodingScheme       = runtime.NewScheme()
	webhookEncodingCodecFactory = serializer.NewCodecFactory(webhookEncodingScheme)
)

func init() {
	// TODO eventually we shouldn't deal in internal versions, but for now decode into one.
	utilruntime.Must(buildv1helpers.Install(webhookEncodingScheme))
	webhookEncodingCodecFactory = serializer.NewCodecFactory(webhookEncodingScheme)
}

var _ rest.Storage = &WebHook{}

type WebHook struct {
	groupVersion      schema.GroupVersion
	buildConfigClient buildclienttyped.BuildConfigsGetter
	secretsClient     kubetypedclient.SecretsGetter
	instantiator      buildclienttyped.BuildConfigsGetter
	plugins           map[string]webhook.Plugin
}

// NewWebHookREST returns the webhook handler
func NewWebHookREST(buildConfigClient buildclienttyped.BuildV1Interface, secretsClient kubetypedclient.SecretsGetter, groupVersion schema.GroupVersion, plugins map[string]webhook.Plugin) *WebHook {
	return newWebHookREST(buildConfigClient, secretsClient, groupVersion, plugins)
}

// this supports simple unit testing
func newWebHookREST(buildConfigClient buildclienttyped.BuildConfigsGetter, secretsClient kubetypedclient.SecretsGetter, groupVersion schema.GroupVersion,
	plugins map[string]webhook.Plugin) *WebHook {
	return &WebHook{
		groupVersion:      groupVersion,
		buildConfigClient: buildConfigClient,
		instantiator:      buildConfigClient,
		secretsClient:     secretsClient,
		plugins:           plugins,
	}
}

// New() responds with the status object.
func (h *WebHook) New() runtime.Object {
	return &buildapi.Build{}
}

func (h *WebHook) Destroy() {}

// Connect responds to connections with a ConnectHandler
func (h *WebHook) Connect(ctx context.Context, name string, options runtime.Object, responder rest.Responder) (http.Handler, error) {
	return &WebHookHandler{
		ctx:               ctx,
		name:              name,
		options:           options.(*kapi.PodProxyOptions),
		responder:         responder,
		groupVersion:      h.groupVersion,
		plugins:           h.plugins,
		buildConfigClient: h.buildConfigClient,
		secretsClient:     h.secretsClient,
		instantiator:      h.instantiator,
	}, nil
}

// NewConnectionOptions identifies the options that should be passed to this hook
func (h *WebHook) NewConnectOptions() (runtime.Object, bool, string) {
	return &kapi.PodProxyOptions{}, true, "path"
}

// ConnectMethods returns the supported web hook types.
func (h *WebHook) ConnectMethods() []string {
	return []string{"POST"}
}

// WebHookHandler responds to web hook requests from the master.
type WebHookHandler struct {
	ctx               context.Context
	name              string
	options           *kapi.PodProxyOptions
	responder         rest.Responder
	groupVersion      schema.GroupVersion
	plugins           map[string]webhook.Plugin
	buildConfigClient buildclienttyped.BuildConfigsGetter
	secretsClient     kubetypedclient.SecretsGetter
	instantiator      buildclienttyped.BuildConfigsGetter
}

// ServeHTTP implements the standard http.Handler
func (h *WebHookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.ProcessWebHook(w, r, h.ctx, h.name, h.options.Path); err != nil {
		h.responder.Error(err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ProcessWebHook does the actual work of processing the webhook request
func (w *WebHookHandler) ProcessWebHook(writer http.ResponseWriter, req *http.Request, ctx context.Context, name, subpath string) error {
	parts := strings.Split(strings.TrimPrefix(subpath, "/"), "/")
	if len(parts) != 2 {
		return errors.NewBadRequest(fmt.Sprintf("unexpected hook subpath %s", subpath))
	}
	secret, hookType := parts[0], parts[1]

	plugin, ok := w.plugins[hookType]
	if !ok {
		return errors.NewNotFound(build.Resource("buildconfighook"), hookType)
	}

	config, err := w.buildConfigClient.BuildConfigs(apirequest.NamespaceValue(ctx)).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// clients should not be able to find information about build configs in
		// the system unless the config exists and the secret matches
		return errors.NewUnauthorized(fmt.Sprintf("the webhook %q for %q did not accept your secret", hookType, name))
	}

	triggers, err := plugin.GetTriggers(config)
	if err != nil {
		return errors.NewUnauthorized(fmt.Sprintf("the webhook %q for %q did not accept your secret", hookType, name))
	}

	klog.V(4).Infof("checking secret for %q webhook trigger of buildconfig %s/%s", hookType, config.Namespace, config.Name)
	trigger, err := webhook.CheckSecret(ctx, config.Namespace, secret, triggers, w.secretsClient)
	if err != nil {
		return errors.NewUnauthorized(fmt.Sprintf("the webhook %q for %q did not accept your secret", hookType, name))
	}

	revision, envvars, dockerStrategyOptions, proceed, err := plugin.Extract(config, trigger, req)
	if !proceed {
		switch err {
		case webhook.ErrSecretMismatch, webhook.ErrHookNotEnabled:
			return errors.NewUnauthorized(fmt.Sprintf("the webhook %q for %q did not accept your secret", hookType, name))
		case webhook.MethodNotSupported:
			return errors.NewMethodNotSupported(build.Resource("buildconfighook"), req.Method)
		}
		if _, ok := err.(*errors.StatusError); !ok && err != nil {
			return errors.NewInternalError(fmt.Errorf("hook failed: %v", err))
		}
		return err
	}
	warning := err

	buildTriggerCauses := webhook.GenerateBuildTriggerInfo(revision, hookType)

	request := &buildv1.BuildRequest{
		TriggeredBy:           buildTriggerCauses,
		ObjectMeta:            metav1.ObjectMeta{Name: name},
		Revision:              revision,
		Env:                   envvars,
		DockerStrategyOptions: dockerStrategyOptions,
	}

	newBuild, err := w.instantiator.BuildConfigs(config.Namespace).Instantiate(ctx, config.Namespace, request, metav1.CreateOptions{})
	if err != nil {
		return errors.NewInternalError(fmt.Errorf("could not generate a build: %v", err))
	}

	// Send back the build name so that the client can alert the user.
	if newBuildEncoded, err := runtime.Encode(webhookEncodingCodecFactory.LegacyCodec(w.groupVersion), newBuild); err != nil {
		utilruntime.HandleError(err)
	} else {
		writer.Write(newBuildEncoded)
	}

	return warning
}
