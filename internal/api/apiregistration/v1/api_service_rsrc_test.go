package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	api "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const (
	collection = "/apis/apiregistration.k8s.io/v1/apiservices"
	testName   = "v1.example.test"
	testItem   = collection + "/" + testName
	pemText    = "-----BEGIN CERTIFICATE-----\nY2VydGlmaWNhdGU=\n-----END CERTIFICATE-----\n"
)

var apiServices = schema.GroupResource{Group: "apiregistration.k8s.io", Resource: "apiservices"}

type apiRequest struct {
	method, path, auth string
	body               string
}

type apiServer struct {
	mu       sync.Mutex
	live     *api.APIService
	requests []apiRequest
	failures map[int]*apierrors.StatusError
	get      func(*api.APIService, int)
	sequence int
	retain   bool
}

func newAPIServer(t *testing.T) (*apiServer, *config.Configuration) {
	t.Helper()
	s := &apiServer{failures: map[int]*apierrors.StatusError{}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if err := s.serve(w, r); err != nil {
			t.Error(err)
			writeAPIError(w, apierrors.NewInternalError(err))
		}
	}))
	t.Cleanup(server.Close)
	return s, &config.Configuration{Server: &server.URL, InsecureSkipTLSVerify: new(true)}
}

func writeAPIError(w http.ResponseWriter, err *apierrors.StatusError) {
	status := err.ErrStatus
	status.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}
	w.WriteHeader(int(status.Code))
	_ = json.NewEncoder(w).Encode(status)
}

func (s *apiServer) serve(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	s.requests = append(s.requests, apiRequest{
		method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(body),
	})
	if failure := s.failures[len(s.requests)]; failure != nil {
		writeAPIError(w, failure)
		return nil
	}
	if r.Method == http.MethodPost {
		return s.create(w, r.URL.Path, body)
	}
	if s.live == nil || r.URL.Path != collection+"/"+s.live.Name {
		writeAPIError(w, apierrors.NewNotFound(apiServices, r.URL.Path))
		return nil
	}
	switch r.Method {
	case http.MethodGet:
		response := s.live.DeepCopy()
		if s.get != nil {
			s.get(response, len(s.requests))
		}
		return json.NewEncoder(w).Encode(response)
	case http.MethodPatch:
		if r.Header.Get("Content-Type") != "application/json-patch+json" {
			return fmt.Errorf("unexpected patch content type %q", r.Header.Get("Content-Type"))
		}
		return s.update(w, body)
	case http.MethodDelete:
		return s.delete(w, body)
	default:
		return fmt.Errorf("unexpected method %q", r.Method)
	}
}

func (s *apiServer) create(w http.ResponseWriter, path string, body []byte) error {
	if path != collection {
		return fmt.Errorf("unexpected create path %q", path)
	}
	if s.live != nil {
		writeAPIError(w, apierrors.NewAlreadyExists(apiServices, s.live.Name))
		return nil
	}
	var created api.APIService
	if err := json.Unmarshal(body, &created); err != nil {
		return err
	}
	s.sequence++
	created.UID = types.UID(fmt.Sprintf("uid-%d", s.sequence))
	created.Status.Conditions = []api.APIServiceCondition{
		{Type: api.Available, Status: api.ConditionFalse},
	}
	s.live = &created
	return json.NewEncoder(w).Encode(s.live)
}

func (s *apiServer) update(w http.ResponseWriter, body []byte) error {
	patch, err := jsonpatch.DecodePatch(body)
	if err != nil {
		return err
	}
	data, err := json.Marshal(s.live)
	if err != nil {
		return err
	}
	data, err = patch.Apply(data)
	if err != nil {
		writeAPIError(w, apierrors.NewConflict(apiServices, s.live.Name, err))
		return nil
	}
	var patched api.APIService
	if err := json.Unmarshal(data, &patched); err != nil {
		return err
	}
	s.live = &patched
	return json.NewEncoder(w).Encode(s.live)
}

func (s *apiServer) delete(w http.ResponseWriter, body []byte) error {
	var options metav1.DeleteOptions
	if err := json.Unmarshal(body, &options); err != nil {
		return err
	}
	if options.Preconditions == nil || options.Preconditions.UID == nil {
		return errors.New("delete missing UID precondition")
	}
	if *options.Preconditions.UID != s.live.UID {
		err := fmt.Errorf("UID precondition %s did not match %s", *options.Preconditions.UID, s.live.UID)
		writeAPIError(w, apierrors.NewConflict(apiServices, s.live.Name, err))
		return nil
	}
	if !s.retain {
		s.live = nil
	}
	return json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusSuccess})
}

func (s *apiServer) takeRequests() []apiRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := s.requests
	s.requests = nil
	return requests
}

func (s *apiServer) snapshot() *api.APIService {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live.DeepCopy()
}

func (s *apiServer) change(change func(*api.APIService)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s.live)
}

func (s *apiServer) fail(at int, err *apierrors.StatusError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[at] = err
}

func requireTrace(t *testing.T, requests []apiRequest, expected ...string) {
	t.Helper()
	var operations []string
	for _, r := range requests {
		operations = append(operations, r.method+" "+r.path)
	}
	require.Equal(t, expected, operations)
}

func apiServiceInput() APIServiceResource {
	return APIServiceResource{
		Metadata: APIServiceMetadata{Name: testName},
		Spec: APIServiceSpec{
			Group: "example.test", Version: "v1", GroupPriorityMinimum: 20, VersionPriority: 10,
		},
	}
}

func TestAPIServiceCreate(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec APIServiceSpec
		want string
	}{
		{"local", APIServiceSpec{}, ""},
		{"remote default", APIServiceSpec{
			Service: &APIServiceBackend{Name: "backend", Namespace: "remote"},
		}, `,"service":{"name":"backend","namespace":"remote","port":443}`},
		{"remote alternate", APIServiceSpec{
			Service:  &APIServiceBackend{Name: "other", Namespace: "remote", Port: new(int64(6443))},
			CABundle: new(pemText),
		}, `,"service":{"name":"other","namespace":"remote","port":6443},"caBundle":` +
			`"LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tClkyVnlkR2xtYVdOaGRHVT0K` +
			`LS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo="`},
		{"remote skip", APIServiceSpec{
			Service:               &APIServiceBackend{Name: "backend", Namespace: "remote"},
			InsecureSkipTLSVerify: new(true), CABundle: new(""),
		}, `,"service":{"name":"backend","namespace":"remote","port":443},"insecureSkipTLSVerify":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := newAPIServer(t)
			s.get = func(obj *api.APIService, at int) { obj.Spec.VersionPriority = int32(at) }
			r := apiServiceInput()
			r.Spec.Service, r.Spec.CABundle = tc.spec.Service, tc.spec.CABundle
			r.Spec.InsecureSkipTLSVerify = tc.spec.InsecureSkipTLSVerify
			r.Metadata.Annotations = new(map[string]string{"EXAMPLE.test/Key": "Value"})
			r.Metadata.Labels = new(map[string]string{"example.test/Empty": ""})
			out, err := r.Create(t.Context(), cfg)
			require.NoError(t, err)
			wantSpec := desiredSpec(r.Spec)
			wantSpec.VersionPriority = 3
			require.Equal(t, &APIServiceResourceOutput{
				Server: *cfg.Server, APIVersion: apiServiceVersion, Kind: apiServiceKind,
				Name: testName, UID: "uid-1", Spec: wantSpec,
				Metadata: APIServiceMetadataOutput{
					Name: testName, Annotations: map[string]string{"EXAMPLE.test/Key": "Value"},
					Labels: map[string]string{"example.test/Empty": ""},
				},
			}, out)
			requests := s.takeRequests()
			requireTrace(t, requests, "POST "+collection, "GET "+testItem, "GET "+testItem)
			require.JSONEq(t, `{"kind":"APIService","apiVersion":"apiregistration.k8s.io/v1",
				"metadata":{"name":"v1.example.test","creationTimestamp":null,
					"annotations":{"EXAMPLE.test/Key":"Value"},"labels":{"example.test/Empty":""}},
				"spec":{"group":"example.test","version":"v1","groupPriorityMinimum":20,
					"versionPriority":10`+tc.want+`},"status":{}}`, requests[0].body)
			_, err = r.Create(t.Context(), cfg)
			require.True(t, apierrors.IsAlreadyExists(err), "%v", err)
			require.ErrorContains(t, err, "create APIService")
			requireTrace(t, s.takeRequests(), "POST "+collection)
		})
	}
}

func TestAPIServiceSavedTargetAndObservedPort(t *testing.T) {
	s, cfg := newAPIServer(t)
	other, otherCfg := newAPIServer(t)
	r := apiServiceInput()
	r.Spec.Service = &APIServiceBackend{Name: "backend", Namespace: "remote"}
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	s.takeRequests()

	s.change(func(obj *api.APIService) { obj.Spec.Service.Port = nil })
	otherCfg.Token = new("current-token")
	prior := apiServicePrior{Inputs: r, Outputs: out}
	observed, err := r.Read(t.Context(), otherCfg, prior)
	require.NoError(t, err)
	require.Equal(t, &APIServiceBackend{Name: "backend", Namespace: "remote"}, observed.Spec.Service)
	require.Equal(t, *cfg.Server, observed.Server)
	require.Equal(t, []apiRequest{
		{method: "GET", path: testItem, auth: "Bearer current-token"},
		{method: "GET", path: testItem, auth: "Bearer current-token"},
	}, s.takeRequests())

	prior.Observed = observed
	r.Metadata.Annotations = new(map[string]string{"annotation": "added"})
	updated, err := r.Update(t.Context(), otherCfg, prior)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"annotation": "added"}, updated.Metadata.Annotations)
	requests := s.takeRequests()
	requireTrace(t, requests, "PATCH "+testItem, "GET "+testItem, "GET "+testItem)
	for _, request := range requests {
		require.Equal(t, "Bearer current-token", request.auth)
	}
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"add","path":"/metadata/annotations","value":{"annotation":"added"}}
	]`, requests[0].body)

	require.NoError(t, r.Delete(t.Context(), otherCfg, prior))
	require.Empty(t, other.takeRequests())
	requests = s.takeRequests()
	requireTrace(t, requests, "DELETE "+testItem)
	require.Equal(t, "Bearer current-token", requests[0].auth)
	require.JSONEq(t, `{"kind":"DeleteOptions","apiVersion":"apiregistration.k8s.io/v1",
		"preconditions":{"uid":"uid-1"}}`,
		requests[0].body)
}

func TestAPIServiceRefreshErrors(t *testing.T) {
	denied := apierrors.NewForbidden(apiServices, testName, errors.New("denied"))
	for _, tc := range []struct {
		name    string
		at      int
		status  *apierrors.StatusError
		missing bool
	}{
		{"probe missing", 1, apierrors.NewNotFound(apiServices, testName), true},
		{"second missing", 2, apierrors.NewNotFound(apiServices, testName), false},
		{"probe forbidden", 1, denied, false},
		{"second forbidden", 2, denied, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := newAPIServer(t)
			r := apiServiceInput()
			out, err := r.Create(t.Context(), cfg)
			require.NoError(t, err)
			s.takeRequests()
			s.fail(tc.at, tc.status)
			_, err = r.Read(t.Context(), cfg, apiServicePrior{Inputs: r, Outputs: out})
			require.ErrorContains(t, err, "read APIService")
			require.Equal(t, tc.missing, errors.Is(err, runtime.ErrNotFound))
			if !tc.missing {
				require.Equal(t, tc.status.ErrStatus.Reason, apierrors.ReasonForError(err))
			}
			want := slices.Repeat([]string{"GET " + testItem}, tc.at)
			requireTrace(t, s.takeRequests(), want...)
		})
	}
}

func TestAPIServicePatchTLSAndBackendFields(t *testing.T) {
	s, cfg := newAPIServer(t)
	r := apiServiceInput()
	r.Spec.Service = &APIServiceBackend{Name: "backend", Namespace: "remote"}
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	s.takeRequests()

	for _, tc := range []struct {
		name string
		edit func(*APIServiceSpec)
		want string
		ca   string
	}{
		{"name and CA", func(spec *APIServiceSpec) {
			spec.Service.Name = "changed"
			spec.CABundle = new(pemText)
		}, `"service":{"name":"changed","namespace":"remote","port":443},` +
			`"caBundle":"LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tClkyVnlkR2xtYVdOaGRHVT0K` +
			`LS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo=",`, pemText},
		{"namespace and clear CA", func(spec *APIServiceSpec) {
			spec.Service.Namespace = "changed-namespace"
			spec.CABundle = nil
		}, `"service":{"name":"changed","namespace":"changed-namespace","port":443},`, ""},
		{"port and skip", func(spec *APIServiceSpec) {
			spec.Service.Port = new(int64(1))
			spec.InsecureSkipTLSVerify = new(true)
		}, `"service":{"name":"changed","namespace":"changed-namespace","port":1},` +
			`"insecureSkipTLSVerify":true,`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := apiServicePrior{Inputs: r, Outputs: out}
			next := *r.Spec.Service
			r.Spec.Service = &next
			tc.edit(&r.Spec)
			r.Spec.GroupPriorityMinimum = 20000
			r.Spec.VersionPriority = 1000
			s.mu.Lock()
			s.get = func(obj *api.APIService, at int) {
				if at == 3 {
					obj.Spec.VersionPriority = 999
				}
			}
			s.mu.Unlock()
			out, err = r.Update(t.Context(), cfg, prior)
			require.NoError(t, err)
			require.Equal(t, "uid-1", out.UID)
			require.Equal(t, tc.ca, out.Spec.CABundle)
			require.Equal(t, int64(999), out.Spec.VersionPriority)
			require.Equal(t, []api.APIServiceCondition{{Type: api.Available, Status: api.ConditionFalse}},
				s.snapshot().Status.Conditions)
			requests := s.takeRequests()
			requireTrace(t, requests, "PATCH "+testItem, "GET "+testItem, "GET "+testItem)
			require.JSONEq(t, `[
				{"op":"test","path":"/metadata/uid","value":"uid-1"},
				{"op":"replace","path":"/spec","value":{`+tc.want+`
					"group":"example.test","version":"v1","groupPriorityMinimum":20000,"versionPriority":1000
				}}
			]`, requests[0].body)
		})
	}
}

func TestAPIServiceDeleteAcceptanceAndUID(t *testing.T) {
	s, cfg := newAPIServer(t)
	r := apiServiceInput()
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	s.takeRequests()
	s.change(func(obj *api.APIService) {
		obj.Finalizers = []string{"example.test/finalizing"}
		s.retain = true
	})
	prior := apiServicePrior{Inputs: r, Outputs: out}
	require.NoError(t, r.Delete(t.Context(), cfg, prior))
	requireTrace(t, s.takeRequests(), "DELETE "+testItem)
	require.Equal(t, []string{"example.test/finalizing"}, s.snapshot().Finalizers)

	s.change(func(obj *api.APIService) { obj.UID = "other" })
	err = r.Delete(t.Context(), cfg, prior)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.ErrorContains(t, err, "delete APIService")
	requireTrace(t, s.takeRequests(), "DELETE "+testItem)
	require.Equal(t, types.UID("other"), s.snapshot().UID)

	s.change(func(*api.APIService) { s.live = nil })
	require.NoError(t, r.Delete(t.Context(), cfg, prior))
	requireTrace(t, s.takeRequests(), "DELETE "+testItem)
}

func TestAPIServiceRespectsCallerDeadline(t *testing.T) {
	entered := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	r := apiServiceInput()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := r.Create(ctx, &config.Configuration{
		Server: &server.URL, InsecureSkipTLSVerify: new(true),
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-entered:
	default:
		t.Fatal("deadline expired before the request")
	}
}

func TestAPIServiceReadMetadataSpecialKeys(t *testing.T) {
	s, cfg := newAPIServer(t)
	input := apiServiceInput()
	input.IgnoreAnnotations = new([]string{"^external/"})
	input.IgnoreLabels = new([]string{"^external/"})
	input.Metadata.Annotations = new(map[string]string{"external/explicit": "annotation"})
	input.Metadata.Labels = new(map[string]string{"external/explicit": "label"})
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	s.takeRequests()
	s.change(func(obj *api.APIService) {
		for _, fields := range []map[string]string{obj.Annotations, obj.Labels} {
			fields["external/unmanaged"] = "ignore"
			fields["test.kubernetes.io/default"] = "hide"
			fields["test.deprecated.daemonset.template.generation"] = "hide"
			fields["app.kubernetes.io/Name"] = "preserve"
			fields["service.beta.kubernetes.io/Name"] = "preserve"
		}
	})
	observed, err := input.Read(t.Context(), cfg, apiServicePrior{Inputs: input, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"app.kubernetes.io/Name": "preserve", "service.beta.kubernetes.io/Name": "preserve",
		"external/explicit": "annotation",
	}, observed.Metadata.Annotations)
	require.Equal(t, map[string]string{
		"app.kubernetes.io/Name": "preserve", "service.beta.kubernetes.io/Name": "preserve",
		"external/explicit": "label",
	}, observed.Metadata.Labels)
	requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem)
}
