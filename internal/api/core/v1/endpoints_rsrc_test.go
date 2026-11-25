package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const endpointsPath = "/api/v1/namespaces/default/endpoints"

func TestEndpointsCreateRefreshesGeneratedIdentity(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == http.MethodPost {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.JSONEq(t, `{"apiVersion":"v1","kind":"Endpoints",
				"metadata":{"generateName":"subject-","namespace":"default","creationTimestamp":null},
				"subsets":[{"addresses":[{"ip":"192.0.2.1"}],"ports":[{"port":80,"protocol":"TCP"}]}]}
			`, string(body))
			respond(t, w, &endpointsObject{ObjectMeta: metav1.ObjectMeta{
				Name: "subject-actual", Namespace: "default", UID: "uid",
			}})
			return
		}
		respond(t, w, &endpointsObject{ObjectMeta: metav1.ObjectMeta{
			Name: "subject-actual", GenerateName: "subject-", Namespace: "default", UID: "uid",
		}, Subsets: []endpointsAPISubset{{
			Addresses: []corev1.EndpointAddress{{IP: "192.0.2.1"}},
			Ports:     []corev1.EndpointPort{{Port: 80, Protocol: corev1.ProtocolUDP}},
		}}})
	})
	resource := &EndpointsResource{
		Metadata: EndpointsMetadata{GenerateName: new("subject-")},
		Subset: new([]EndpointsSubset{{
			Address: new([]EndpointsAddress{{IP: "192.0.2.1"}}),
			Port:    new([]EndpointsPort{{Port: 80}}),
		}}),
	}
	out, err := resource.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &EndpointsResourceOutput{
		Server: *c.Server, APIVersion: "v1", Kind: "Endpoints",
		Name: "subject-actual", Namespace: "default", UID: "uid",
		Metadata: EndpointsMetadataOutput{Name: "subject-actual", GenerateName: "subject-",
			Namespace: "default", Annotations: map[string]string{}, Labels: map[string]string{}},
		Subset: []EndpointsSubsetOutput{{
			Address:         []EndpointsAddressOutput{{IP: "192.0.2.1"}},
			NotReadyAddress: []EndpointsAddressOutput{},
			Port:            []EndpointsPortOutput{{Port: 80, Protocol: "UDP"}},
		}},
	}, out)
	require.Equal(t, []string{"POST " + endpointsPath, "GET " + endpointsPath + "/subject-actual"},
		calls)
}

func TestEndpointsCreateNamespaceAndEmptyMembers(t *testing.T) {
	empty := new([]EndpointsSubset{})
	emptyMember := new([]EndpointsSubset{{
		Address: new([]EndpointsAddress{}), Port: new([]EndpointsPort{}),
	}})
	for _, test := range []struct {
		name      string
		namespace *string
		subset    *[]EndpointsSubset
		path      string
	}{
		{"omitted", nil, nil, endpointsPath},
		{"explicit-default", new("default"), empty, endpointsPath},
		{"custom", new("custom"), emptyMember, "/api/v1/namespaces/custom/endpoints"},
		{"empty", new(""), nil, "/api/v1/endpoints"},
		{"invalid-path", new("bad/namespace"), nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				require.Empty(t, req.URL.RawQuery)
				live := endpointsObject{ObjectMeta: metav1.ObjectMeta{
					Name: "subject", Namespace: endpointsNamespace(test.namespace), UID: "uid",
				}}
				if req.Method == http.MethodPost {
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					wantMetadata := map[string]any{"name": "subject", "creationTimestamp": nil,
						"annotations": map[string]string{"example.com/empty": ""},
						"labels":      map[string]string{"name": "value"}}
					if live.Namespace != "" {
						wantMetadata["namespace"] = live.Namespace
					}
					expected, err := json.Marshal(map[string]any{
						"apiVersion": "v1", "kind": "Endpoints", "metadata": wantMetadata,
					})
					require.NoError(t, err)
					require.JSONEq(t, string(expected), string(body))
					if live.Namespace == "" {
						endpointsHTTPError(t, w, 405, metav1.StatusReasonMethodNotAllowed)
						return
					}
				}
				respond(t, w, &live)
			})
			resource := &EndpointsResource{Metadata: EndpointsMetadata{
				Name: new("subject"), Namespace: test.namespace,
				Annotations: new(map[string]*string{"example.com/empty": new(""), "nil": nil}),
				Labels:      new(map[string]*string{"name": new("value"), "nil": nil}),
			}, Subset: test.subset}
			out, err := resource.Create(t.Context(), c)
			switch test.name {
			case "invalid-path":
				require.ErrorContains(t, err, "invalid namespace")
				require.Empty(t, calls)
			case "empty":
				require.True(t, apierrors.IsMethodNotSupported(err), "%v", err)
				require.Equal(t, []string{"POST " + test.path}, calls)
			default:
				require.NoError(t, err)
				require.Equal(t, []string{"POST " + test.path, "GET " + test.path + "/subject"},
					calls)
				require.Equal(t, []EndpointsSubsetOutput{}, out.Subset)
			}
		})
	}
}

func TestEndpointsAPIDomainConversion(t *testing.T) {
	badProtocol := "unsupported"
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"apiVersion":"v1","kind":"Endpoints","metadata":{
			"name":"subject","namespace":"default","creationTimestamp":null},
			"subsets":[
				{"ports":[{"port":80,"protocol":"unsupported"}]},
				{"addresses":[{"ip":"192.0.2.1"},{"ip":"2001:db8::4","hostname":"host",
					"nodeName":"node"}],"notReadyAddresses":[{"ip":"192.0.2.2"}],"ports":[
						{"port":81},{"name":"a","port":82,"protocol":"UDP"},
						{"name":"b","port":83,"protocol":"SCTP"}
					]}
			]}`, string(body))
		endpointsHTTPError(t, w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid)
	})
	resource := EndpointsResource{
		Metadata: EndpointsMetadata{Name: new("subject")},
		Subset: new([]EndpointsSubset{
			{Port: new([]EndpointsPort{{Port: 1<<32 + 80, Protocol: &badProtocol}})},
			{
				Address: new([]EndpointsAddress{
					{IP: "2001:db8::4", Hostname: new("host"), NodeName: new("node")},
					{IP: "192.0.2.1", Hostname: new(""), NodeName: new("")},
				}),
				NotReadyAddress: new([]EndpointsAddress{{IP: "192.0.2.2"}}),
				Port: new([]EndpointsPort{
					{Name: new("b"), Port: 83, Protocol: new("SCTP")},
					{Name: new(""), Port: 81, Protocol: new("")},
					{Name: new("a"), Port: 82, Protocol: new("UDP")},
				}),
			},
		}),
	}
	_, err := resource.Create(t.Context(), c)
	require.True(t, apierrors.IsInvalid(err), "%v", err)
	require.ErrorContains(t, err, "create Endpoints")
	require.Equal(t, []string{"POST " + endpointsPath}, calls)
}

func TestEndpointsReadUsesSavedFiltersAndIdentity(t *testing.T) {
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "Bearer rotated", req.Header.Get("Authorization"))
		require.Equal(t, "GET "+endpointsPath+"/actual", req.Method+" "+req.URL.Path)
		respond(t, w, &endpointsObject{ObjectMeta: metav1.ObjectMeta{
			Name: "actual", GenerateName: "prior-", Namespace: "default", UID: "uid",
			Generation: 99, ResourceVersion: "30",
			Annotations: map[string]string{
				"control.kubernetes.io/internal":  "hidden",
				"app.kubernetes.io/name":          "exempt",
				"service.beta.kubernetes.io/keep": "exempt",
				"ignored/a":                       "hidden",
				"ignored/prior":                   "tracked",
				"visible":                         "plain",
			},
			Labels: map[string]string{
				"control.kubernetes.io/internal":  "hidden",
				"app.kubernetes.io/name":          "exempt",
				"service.beta.kubernetes.io/keep": "exempt",
				"ignored/a":                       "unmatched",
				"filtered/a":                      "hidden",
				"filtered/managed":                "tracked",
			},
		}, Subsets: []endpointsAPISubset{{
			Addresses: []corev1.EndpointAddress{{IP: "2001:db8::4",
				TargetRef: &corev1.ObjectReference{Name: "unmanaged"}}},
			Ports: []corev1.EndpointPort{{Port: 80, AppProtocol: new("unmanaged")}},
		}, {}}})
	})
	target := endpointsTestTarget(*c.Server)
	target.Metadata.Annotations = map[string]string{"ignored/prior": "old"}
	c.Server, c.Token = new("https://unused.invalid"), new("rotated")
	resource := &EndpointsResource{
		IgnoreAnnotations: new([]string{"."}), IgnoreLabels: new([]string{"."}),
	}
	saved := EndpointsResource{
		Metadata:     EndpointsMetadata{Labels: new(map[string]*string{"filtered/managed": new("old")})},
		IgnoreLabels: new([]string{"^filtered/", "["}), IgnoreAnnotations: new([]string{"^ignored/"}),
	}
	out, err := resource.Read(t.Context(), c, endpointsPrior{Outputs: target, Inputs: saved})
	require.NoError(t, err)
	require.Equal(t, &EndpointsResourceOutput{
		Server: target.Server, APIVersion: "v1", Kind: "Endpoints", Name: "actual",
		Namespace: "default", UID: "uid",
		Metadata: EndpointsMetadataOutput{
			Name: "actual", GenerateName: "prior-", Namespace: "default",
			Annotations: map[string]string{"app.kubernetes.io/name": "exempt",
				"service.beta.kubernetes.io/keep": "exempt", "ignored/prior": "tracked",
				"visible": "plain"},
			Labels: map[string]string{"app.kubernetes.io/name": "exempt",
				"service.beta.kubernetes.io/keep": "exempt", "ignored/a": "unmatched",
				"filtered/managed": "tracked"},
		},
		Subset: []EndpointsSubsetOutput{{Address: []EndpointsAddressOutput{{IP: "2001:db8::4"}},
			NotReadyAddress: []EndpointsAddressOutput{}, Port: []EndpointsPortOutput{{Port: 80}}}},
	}, out)
}

func endpointsTestTarget(server string) *EndpointsResourceOutput {
	return &EndpointsResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Endpoints", Name: "actual",
		Namespace: "default", UID: "uid",
	}
}

func endpointsHTTPError(t *testing.T, w http.ResponseWriter, code int, reason metav1.StatusReason) {
	t.Helper()
	w.WriteHeader(code)
	respond(t, w, metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   "Failure", Code: int32(code), Reason: reason, Message: string(reason),
	})
}

func TestEndpointsLifecycleFailures(t *testing.T) {
	resource := &EndpointsResource{Metadata: EndpointsMetadata{
		Name: new("actual"), Labels: new(map[string]*string{"changed": new("value")}),
	}}
	for _, test := range []struct {
		name, method, context string
		code                  int
		reason                metav1.StatusReason
		wantError             func(error) bool
	}{
		{"read-missing", "GET", "", 404, metav1.StatusReasonNotFound,
			func(err error) bool { return errors.Is(err, runtime.ErrNotFound) }},
		{"read-forbidden", "GET", "read Endpoints", 403, metav1.StatusReasonForbidden,
			apierrors.IsForbidden},
		{"already-exists", "POST", "create Endpoints", 409, metav1.StatusReasonAlreadyExists,
			apierrors.IsAlreadyExists},
		{"patch-server-error", "PATCH", "patch Endpoints", 500, metav1.StatusReasonInternalError,
			apierrors.IsInternalError},
		{"delete-missing", "DELETE", "", 404, metav1.StatusReasonNotFound, nil},
		{"delete-conflict", "DELETE", "delete Endpoints", 409, metav1.StatusReasonConflict,
			apierrors.IsConflict},
		{"delete-delayed-finalization", "DELETE", "", 202, "", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodDelete {
					raw, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.JSONEq(t, `{"kind":"DeleteOptions","apiVersion":"v1",
						"preconditions":{"uid":"uid"}}`, string(raw))
				}
				if test.code == http.StatusAccepted {
					w.WriteHeader(test.code)
					respond(t, w, &endpointsObject{ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: new(metav1.Now()), Finalizers: []string{"waiting"},
					}})
					return
				}
				endpointsHTTPError(t, w, test.code, test.reason)
			})
			p := endpointsPrior{Outputs: endpointsTestTarget(*c.Server), Inputs: EndpointsResource{
				Metadata: EndpointsMetadata{Name: new("actual")},
			}}
			var err error
			path := endpointsPath + "/actual"
			switch test.method {
			case http.MethodGet:
				_, err = resource.Read(t.Context(), c, p)
			case http.MethodPost:
				path = endpointsPath
				_, err = resource.Create(t.Context(), c)
			case http.MethodPatch:
				_, err = resource.Update(t.Context(), c, p)
			case http.MethodDelete:
				err = resource.Delete(t.Context(), c, p)
			}
			if test.wantError == nil {
				require.NoError(t, err)
			} else {
				require.True(t, test.wantError(err), "%v", err)
				require.ErrorContains(t, err, test.context)
			}
			require.Equal(t, []string{test.method + " " + path}, calls)
		})
	}
}

func TestEndpointsRejectsMissingIdentityBeforeHTTP(t *testing.T) {
	c := configMapAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected request with missing saved identity")
	})
	resource := &EndpointsResource{}
	for _, target := range []*EndpointsResourceOutput{nil, {}, {Server: *c.Server}} {
		p := endpointsPrior{Outputs: target}
		_, err := resource.Read(t.Context(), c, p)
		require.ErrorContains(t, err, "missing Endpoints identity")
		_, err = resource.Update(t.Context(), c, p)
		require.ErrorContains(t, err, "missing Endpoints identity")
		require.ErrorContains(t, resource.Delete(t.Context(), c, p), "missing Endpoints identity")
	}
}

func TestEndpointsRefreshFailureAndCancellation(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == http.MethodPost || req.Method == http.MethodPatch {
			respond(t, w, &endpointsObject{ObjectMeta: metav1.ObjectMeta{
				Name: "actual", Namespace: "default", UID: "uid",
			}})
			return
		}
		_, err := w.Write([]byte("{"))
		require.NoError(t, err)
	})
	resource := &EndpointsResource{Metadata: EndpointsMetadata{
		Name: new("actual"), Labels: new(map[string]*string{"changed": new("value")}),
	}}
	_, err := resource.Create(t.Context(), c)
	require.ErrorContains(t, err, "read Endpoints")
	require.ErrorContains(t, err, "unexpected end of JSON input")
	p := endpointsPrior{Outputs: endpointsTestTarget(*c.Server), Inputs: EndpointsResource{
		Metadata: EndpointsMetadata{Name: new("actual")},
	}}
	_, err = resource.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "read Endpoints")
	require.ErrorContains(t, err, "unexpected end of JSON input")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = resource.Read(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{
		"POST " + endpointsPath, "GET " + endpointsPath + "/actual",
		"PATCH " + endpointsPath + "/actual", "GET " + endpointsPath + "/actual",
	}, calls)
	_, err = resource.Create(t.Context(), &config.Configuration{Server: new("://bad")})
	require.ErrorContains(t, err, "Endpoints client")
}
