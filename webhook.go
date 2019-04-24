package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()
	defaulter     = runtime.ObjectDefaulter(runtimeScheme)
)

const (
	exporterDockerImage = "oliver006/redis_exporter:v0.33.0-alpine"
	statusKey           = "redis-exporter-sidecar.nais.io/status"
	injectKey           = "redis-exporter-sidecar.nais.io/inject"
	exporterPortKey     = "redis-exporter-sidecar.nais.io/port"
	prometheusScrapeKey = "prometheus.io/scrape"
	prometheusPortKey   = "prometheus.io/port"
	prometheusPathKey   = "prometheus.io/path"
)

type WebhookServer struct {
	server *http.Server
}

type Parameters struct {
	certFile  string
	keyFile   string
	LogFormat string
	LogLevel  string
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func getDefaultSidecar() corev1.Container {
	return corev1.Container{
		Name:            "exporter",
		Image:           exporterDockerImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: int32(9121),
				Name:          "http",
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			},
		},
	}
}

func addSidecar() patchOperation {
	return patchOperation{
		Op:    "add",
		Path:  "/spec/containers/-",
		Value: getDefaultSidecar(),
	}
}

func updateAnnotation(target map[string]string) patchOperation {
	if target == nil || target[injectKey] == "" {
		target = map[string]string{}
		return patchOperation{
			Op:   "add",
			Path: "/metadata/annotations",
			Value: map[string]string{
				injectKey:           "injected",
				prometheusScrapeKey: "true",
				prometheusPortKey:   "",
				prometheusPathKey:   "/metrics",
			},
		}
	}

	return patchOperation{
		Op:    "replace",
		Path:  "/metadata/annotations/" + injectKey,
		Value: "injected",
	}
}

func createPatch(pod *corev1.Pod) ([]byte, error) {
	var patch []patchOperation
	patch = append(patch, addSidecar())
	patch = append(patch, updateAnnotation(pod.Annotations))
	return json.Marshal(patch)
}

func mutationRequired(metadata *metav1.ObjectMeta) bool {
	annotations := metadata.GetAnnotations()
	if annotations == nil {
		return false
	}

	status := annotations[statusKey]
	var required bool
	if strings.ToLower(status) == "injected" {
		required = false;
	} else {
		switch strings.ToLower(annotations[injectKey]) {
		default:
			required = false
		case "y", "yes", "true", "on":
			required = true
		}
	}

	log.Infof("Mutation policy for %v/%v: status: %q required:%v", metadata.Namespace, metadata.Name, status, required)
	return required
}

func (server *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	request := ar.Request
	var pod corev1.Pod
	err := json.Unmarshal(request.Object.Raw, &pod)
	if err != nil {
		log.Errorf("Couldn't unmarshal raw pod object: %v", err)
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	log.Infof("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		request.Kind, request.Namespace, request.Name, pod.Name, request.UID, request.Operation, request.UserInfo)

	if !mutationRequired(&pod.ObjectMeta) {
		log.Info("Skipping mutation for %s/%s due to policy check", pod.Namespace, pod.Name)
		return &v1beta1.AdmissionResponse{
			Allowed: true,
		}
	}

	patchBytes, err := createPatch(&pod)
	if err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	log.Infof("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1beta1.AdmissionResponse{
		Allowed: true,
		Patch:   patchBytes,
		PatchType: func() *v1beta1.PatchType {
			pt := v1beta1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

func (server *WebhookServer) serve(responseWriter http.ResponseWriter, request *http.Request) {
	var body []byte
	if request.Body != nil {
		if data, err := ioutil.ReadAll(request.Body); err == nil {
			body = data
		}
	}

	if len(body) == 0 {
		log.Error("empty body")
		http.Error(responseWriter, "empty body", http.StatusBadRequest)
		return
	}

	contentType := request.Header.Get("Content-Type")
	if contentType != "application/json" {
		log.Errorf("Content-Type=%s, expected application/json", contentType)
		http.Error(responseWriter, "invalid Content-Type, expected `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		log.Errorf("Can't decode body: %v", err)
		admissionResponse = &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		admissionResponse = server.mutate(&ar)
	}

	admissionReview := v1beta1.AdmissionReview{}
	if admissionResponse != nil {
		admissionReview.Response = admissionResponse
		if ar.Request != nil {
			admissionReview.Response.UID = ar.Request.UID
		}
	}

	resp, err := json.Marshal(admissionReview)
	if err != nil {
		log.Errorf("Can't encode response: %v", err)
		http.Error(responseWriter, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
	}

	if _, err := responseWriter.Write(resp); err != nil {
		log.Errorf("Can't write response: %v", err)
		http.Error(responseWriter, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
	}
}
