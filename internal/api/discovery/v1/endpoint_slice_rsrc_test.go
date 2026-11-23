package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func TestEndpointSliceCreate(t *testing.T) {
	for _, tt := range []struct {
		name, namespace, queryNamespace, generated, savedName, metadata string
	}{
		{"default namespace", "", "default", "", "example",
			`"name":"example","namespace":"default"`},
		{"generated name", "team", "team", "example-", "example-abc",
			`"generateName":"example-","namespace":"team"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == "POST" {
					require.JSONEq(t,
						`{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSlice",`+
							`"metadata":{"creationTimestamp":null,`+tt.metadata+
							`},"addressType":"IPv4","endpoints":[{"addresses":["192.0.2.5",`+
							`"192.0.2.4"],"conditions":{"ready":true,"serving":false,`+
							`"terminating":false},"hostname":"test","nodeName":"node","zone":"zone",`+
							`"targetRef":{"name":"pod","namespace":"default","resourceVersion":"123",`+
							`"uid":"pod-uid","fieldPath":"spec.containers{web}"}},{"addresses":null,"conditions":{}}],`+
							`"ports":[{"name":"","port":90,"appProtocol":"http"},`+
							`{"name":"second","port":0,"appProtocol":""}]}`, endpointSliceBody(t, req))
					endpointSliceResponse(t, w, tt.savedName, tt.queryNamespace, "uid", "FQDN",
						`[{"addresses":["from-post"]}]`, `[]`)
					return
				}
				endpointSliceResponse(t, w, tt.savedName, tt.queryNamespace, "uid", "IPv6",
					`[{"addresses":["192.0.2.5","192.0.2.4"],"conditions":{"ready":false},`+
						`"hostname":"test","nodeName":"node","zone":"zone",`+
						`"targetRef":{"namespace":"hidden","uid":"only-uid"}},`+
						`{"addresses":["192.0.2.8"],"targetRef":{}}]`,
					`[{"name":"","port":90,"protocol":"TCP","appProtocol":"http"},`+
						`{"name":"second","port":0,"protocol":"TCP","appProtocol":""},{}]`)
			})
			r := endpointSlice()
			if tt.generated != "" {
				r.Metadata = EndpointSliceMetadata{
					GenerateName: &tt.generated, Namespace: &tt.namespace,
				}
			}
			r.Endpoints = []EndpointSliceEndpoint{
				{Addresses: []string{"192.0.2.5", "192.0.2.4"},
					Conditions: &EndpointSliceConditions{Ready: new(true)}, Hostname: new("test"),
					NodeName: new("node"), Zone: new("zone"), TargetRef: &EndpointSliceReference{
						Name: "pod", ResourceVersion: new("123"), UID: new("pod-uid"),
						FieldPath: new("spec.containers{web}"),
					}},
				{Addresses: []string{}},
			}
			r.Ports = []EndpointSlicePort{
				{Port: "+0090", AppProtocol: "http", Protocol: new("UDP")},
				{Port: "-0", Name: new("second")},
			}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			path := "/apis/discovery.k8s.io/v1/namespaces/" + tt.queryNamespace + "/endpointslices"
			require.Equal(t, []string{"POST " + path, "GET " + path + "/" + tt.savedName}, calls)
			require.Equal(t, &EndpointSliceResourceOutput{
				Server: *c.Server, APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice",
				Name: tt.savedName, Namespace: tt.queryNamespace, UID: "uid", AddressType: "IPv4",
				Metadata: EndpointSliceMetadata{Name: &tt.savedName, Namespace: &tt.queryNamespace,
					GenerateName: nonempty(tt.generated), Labels: &map[string]string{},
					Annotations: &map[string]string{}},
				Endpoints: []EndpointSliceEndpointOutput{
					{Addresses: []string{"192.0.2.5", "192.0.2.4"},
						Conditions: &EndpointSliceConditions{Ready: new(false)}, Hostname: new("test"),
						NodeName: new("node"), Zone: new("zone"),
						TargetRef: &EndpointSliceReferenceOutput{UID: new("only-uid")}},
					{Addresses: []string{"192.0.2.8"}, TargetRef: &EndpointSliceReferenceOutput{}},
				},
				Ports: []EndpointSlicePortOutput{
					{Port: new("90"), Protocol: new("TCP"), AppProtocol: new("http")},
					{Port: new("0"), Protocol: new("TCP"), Name: new("second"), AppProtocol: new("")},
					{},
				},
			}, out)
		})
	}
}

func endpointSlice() *EndpointSliceResource {
	return &EndpointSliceResource{
		Metadata:    EndpointSliceMetadata{Name: new("example")},
		AddressType: "IPv4",
		Endpoints:   []EndpointSliceEndpoint{{Addresses: []string{"192.0.2.1"}}},
		Ports:       []EndpointSlicePort{{Port: "80", AppProtocol: "http"}},
	}
}

func endpointSliceAPI(t *testing.T, handler http.HandlerFunc) *config.Configuration {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, req)
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL}
}

func endpointSliceBody(t *testing.T, req *http.Request) string {
	t.Helper()
	var body json.RawMessage
	require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	return string(body)
}

func endpointSliceResponse(t *testing.T, w http.ResponseWriter,
	name, namespace, uid, addressType, endpoints, ports string,
) {
	t.Helper()
	metadata := map[string]string{"name": name, "namespace": namespace, "uid": uid}
	if name == "example-abc" {
		metadata["generateName"] = "example-"
	}
	meta, err := json.Marshal(metadata)
	require.NoError(t, err)
	_, err = w.Write([]byte(`{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSlice","metadata":` +
		string(meta) + `,"addressType":"` + addressType + `","endpoints":` + endpoints +
		`,"ports":` + ports + `}`))
	require.NoError(t, err)
}

func TestEndpointSlicePatch(t *testing.T) {
	r := endpointSlice()
	r.Metadata.Annotations = &map[string]string{"a/b~c": "", "new": "yes"}
	r.Metadata.Labels = &map[string]string{"first": ""}
	r.Ports[0].Protocol = new("UDP")

	saved := &EndpointSliceResourceOutput{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice",
		Namespace: "default", Name: "example", UID: "uid", AddressType: "IPv4"}
	saved.Metadata.Annotations = &map[string]string{"a/b~c": "old", "remove": "old"}
	saved.Ports = []EndpointSlicePortOutput{
		{Port: new("80"), Protocol: new("TCP"), AppProtocol: new("http")},
	}
	observed := *saved
	observed.Endpoints = []EndpointSliceEndpointOutput{
		{Addresses: []string{"192.0.2.1"}, Hostname: new("old"), NodeName: new("old"),
			Zone: new("old"), Conditions: &EndpointSliceConditions{Ready: new(false)},
			TargetRef: &EndpointSliceReferenceOutput{Name: new("old"), Namespace: new("default")}},
	}
	var calls []string
	c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "/apis/discovery.k8s.io/v1/namespaces/default/endpointslices/example",
			req.URL.Path)
		switch req.Method {
		case "PATCH":
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid"},`+
				`{"op":"remove","path":"/metadata/annotations/remove"},`+
				`{"op":"replace","path":"/metadata/annotations/a~1b~0c","value":""},`+
				`{"op":"add","path":"/metadata/annotations/new","value":"yes"},`+
				`{"op":"add","path":"/metadata/labels","value":{"first":""}},`+
				`{"op":"replace","path":"/endpoints","value":[{"addresses":["192.0.2.1"],"conditions":{}}]},`+
				`{"op":"replace","path":"/ports","value":[{"name":"","port":80,`+
				`"appProtocol":"http"}]}]`, endpointSliceBody(t, req))
			endpointSliceResponse(t, w, "example", "default", "uid", "IPv4", `[]`, `[]`)
		case "GET":
			endpointSliceResponse(t, w, "example", "default", "uid", "IPv4",
				`[{"addresses":["192.0.2.1"]}]`,
				`[{"name":"","port":80,"protocol":"TCP","appProtocol":"http"}]`)
		default:
			t.Errorf("unexpected request %s", req.Method)
		}
	})
	saved.Server, observed.Server = *c.Server, *c.Server
	old := endpointSlice()
	out, err := r.Update(t.Context(), c, endpointSlicePrior{
		Inputs: *old, Outputs: saved, Observed: &observed,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH", "GET"}, calls)
	require.Equal(t, []EndpointSliceEndpointOutput{{Addresses: []string{"192.0.2.1"}}},
		out.Endpoints)
	require.Equal(t, saved.Ports, out.Ports)
	require.Equal(t, saved.UID, out.UID)
}

func TestEndpointSliceUID(t *testing.T) {
	var calls []string
	c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		switch req.Method {
		case "GET":
			endpointSliceResponse(t, w, "example", "default", "different", "IPv4", `[]`, `[]`)
		case "DELETE":
			require.JSONEq(t, `{"apiVersion":"discovery.k8s.io/v1","kind":"DeleteOptions",`+
				`"preconditions":{"uid":"uid"}}`, endpointSliceBody(t, req))
			endpointSliceStatus(t, w, 409, "Conflict")
		case "PATCH":
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid"},`+
				`{"op":"add","path":"/endpoints","value":[{"addresses":["192.0.2.1"],"conditions":{}}]},`+
				`{"op":"add","path":"/ports","value":[{"name":"","port":80,`+
				`"appProtocol":"http"}]}]`, endpointSliceBody(t, req))
			endpointSliceStatus(t, w, 409, "Conflict")
		}
	})
	saved := &EndpointSliceResourceOutput{Server: *c.Server, APIVersion: "discovery.k8s.io/v1",
		Kind: "EndpointSlice", Name: "example", Namespace: "default", UID: "uid"}
	r := endpointSlice()
	p := endpointSlicePrior{Inputs: *r, Outputs: saved}
	_, err := r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed from uid to different")
	observed := *saved
	observed.UID = "different"
	p.Observed = &observed
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed from uid to different")
	p.Observed = nil
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "update endpoint slice")
	r.IgnoreLabels = &[]string{"^external$"}
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed from uid to different")
	require.ErrorContains(t, r.Delete(t.Context(), c, p), "delete endpoint slice")
	require.Equal(t, []string{"GET", "PATCH", "GET", "DELETE"}, calls)
}

func endpointSliceStatus(t *testing.T, w http.ResponseWriter, code int, reason string) {
	t.Helper()
	w.WriteHeader(code)
	_, err := io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure",`+
		`"reason":"`+reason+`","message":"`+reason+`"}`)
	require.NoError(t, err)
}

func TestEndpointSliceProjectedMetadata(t *testing.T) {
	var calls []string
	const remote = `{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSlice","metadata":{` +
		`"name":"example","namespace":"default","uid":"uid","labels":{` +
		`"kept.kubernetes.io/key":"","hidden.kubernetes.io/key":"a","ignored-key":"b",` +
		`"ignored-tracked":"c","app.kubernetes.io/key":"d","service.beta.kubernetes.io/key":"e"},` +
		`"annotations":{"Example.COM/Name":"mixed","old.kubernetes.io/hidden":"a",` +
		`"ignored-annotation":"b","ignored-tracked":"c"}},"addressType":"IPv4",` +
		`"endpoints":[{"addresses":["192.0.2.1"]}],"ports":[{"port":80,"appProtocol":"http"}]}`
	c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == "POST" {
			body := endpointSliceBody(t, req)
			require.Contains(t, body, `"annotations":{"Example.COM/Name":"mixed",`+
				`"ignored-tracked":"c"}`)
			require.Contains(t, body, `"labels":{"ignored-tracked":"c","kept.kubernetes.io/key":""}`)
		}
		_, err := io.WriteString(w, remote)
		require.NoError(t, err)
	})
	r := endpointSlice()
	r.IgnoreAnnotations, r.IgnoreLabels = new([]string{"^ignored"}), new([]string{"^ignored"})
	r.Metadata.Annotations = &map[string]string{"Example.COM/Name": "mixed", "ignored-tracked": "c"}
	r.Metadata.Labels = &map[string]string{"kept.kubernetes.io/key": "", "ignored-tracked": "c"}
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET"}, calls)
	require.Equal(t, &map[string]string{"Example.COM/Name": "mixed", "ignored-tracked": "c"},
		out.Metadata.Annotations)
	require.Equal(t, &map[string]string{"kept.kubernetes.io/key": "", "ignored-tracked": "c",
		"app.kubernetes.io/key": "d", "service.beta.kubernetes.io/key": "e"}, out.Metadata.Labels)
	read, err := r.Read(t.Context(), c, endpointSlicePrior{Inputs: *r, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, out, read)
}

func TestEndpointSlicePatchMetadataUnrelated(t *testing.T) {
	r := endpointSlice()
	r.Metadata.Annotations = &map[string]string{"a/b~c": "", "new": "yes"}
	r.Metadata.Labels = &map[string]string{"first": ""}
	observed := &EndpointSliceResourceOutput{UID: "uid",
		Metadata:  EndpointSliceMetadata{Annotations: &map[string]string{"a/b~c": "old", "x": "old"}},
		Endpoints: []EndpointSliceEndpointOutput{{Addresses: []string{"192.0.2.1"}}},
		Ports: []EndpointSlicePortOutput{
			{Port: new("80"), Protocol: new("TCP"), AppProtocol: new("http")},
		},
	}
	ports, err := prepareEndpointSlicePorts(r.Ports)
	require.NoError(t, err)
	body, err := json.Marshal(r.patch(*r, observed, expandEndpoints(r.Endpoints), ports))
	require.NoError(t, err)
	patch, err := jsonpatch.DecodePatch(body)
	require.NoError(t, err)
	doc, err := patch.Apply([]byte(`{"metadata":{"uid":"uid","annotations":{` +
		`"a/b~c":"old","x":"old","external":"keep"},"labels":{"hidden":"overwrite"}}}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"metadata":{"uid":"uid","annotations":{`+
		`"a/b~c":"","new":"yes","external":"keep"},"labels":{"first":""}}}`, string(doc))
}

func TestEndpointSliceCreateVariants(t *testing.T) {
	for _, protocol := range []*string{nil, new("TCP"), new("UDP"), new("SCTP")} {
		t.Run(effectiveProtocol(protocol), func(t *testing.T) {
			var calls []string
			c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "POST" {
					body := endpointSliceBody(t, req)
					require.Contains(t, body, `"endpoints":[{"addresses":["192.0.2.1"],`+
						`"conditions":{"ready":false,"serving":false,"terminating":false},`+
						`"targetRef":{"namespace":"elsewhere","name":"pod"}}]`)
					require.Contains(t, body, `"ports":[{"name":"","port":80,"appProtocol":"http"}]`)
				}
				endpointSliceResponse(t, w, "example", "default", "uid", "IPv4",
					`[{"addresses":["192.0.2.1"],"conditions":{"ready":false,"serving":false,`+
						`"terminating":false},"targetRef":{"namespace":"elsewhere","name":"pod"}}]`,
					`[{"name":"","port":80,"appProtocol":"http","protocol":"TCP"}]`)
			})
			r := endpointSlice()
			r.Metadata.Namespace = new("default")
			r.Ports[0].Protocol = protocol
			r.Endpoints[0].Conditions = &EndpointSliceConditions{}
			r.Endpoints[0].NodeName, r.Endpoints[0].Zone = new(""), new("")
			r.Endpoints[0].TargetRef = &EndpointSliceReference{
				Name: "pod", Namespace: new("elsewhere"), FieldPath: new(""),
				ResourceVersion: new(""), UID: new(""),
			}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []string{"POST", "GET"}, calls)
			require.Equal(t, []EndpointSliceEndpointOutput{
				{Addresses: []string{"192.0.2.1"}, Conditions: &EndpointSliceConditions{
					Ready: new(false), Serving: new(false), Terminating: new(false),
				}, TargetRef: &EndpointSliceReferenceOutput{Name: new("pod"),
					Namespace: new("elsewhere")}},
			}, out.Endpoints)
			require.Equal(t, []EndpointSlicePortOutput{
				{Port: new("80"), Protocol: new("TCP"), AppProtocol: new("http")},
			}, out.Ports)
		})
	}
}

func TestEndpointSliceAPIErrors(t *testing.T) {
	r := endpointSlice()
	r.Endpoints[0].Addresses = []string{"2001:db8::1"}
	r.Ports[0].Port = "2147483648"
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls = append(calls, req.Method)
		require.Equal(t, "Bearer new-token", req.Header.Get("Authorization"))
		switch req.Method {
		case "POST":
			body := endpointSliceBody(t, req)
			require.Contains(t, body, `"ports":[{"name":"","port":2147483647,"appProtocol":"http"}]`)
			require.Contains(t, body, `"addresses":["2001:db8::1"]`)
			endpointSliceStatus(t, w, http.StatusUnprocessableEntity, "Invalid")
		case "PATCH", "GET", "DELETE":
			endpointSliceStatus(t, w, http.StatusNotFound, "NotFound")
		}
	}))
	t.Cleanup(server.Close)
	c := &config.Configuration{
		Server: &server.URL, Token: new("new-token"), InsecureSkipTLSVerify: new(true),
	}
	_, err := r.Create(t.Context(), c)
	require.True(t, apierrors.IsInvalid(err), "%v", err)
	saved := &EndpointSliceResourceOutput{Server: *c.Server, APIVersion: "discovery.k8s.io/v1",
		Kind: "EndpointSlice", Name: "example", Namespace: "default", UID: "uid"}
	c.Server = new("http://unused.invalid")
	p := endpointSlicePrior{Inputs: *r, Outputs: saved}
	_, err = r.Read(t.Context(), c, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	_, err = r.Update(t.Context(), c, p)
	require.True(t, apierrors.IsNotFound(err), "%v", err)
	require.ErrorContains(t, err, "update endpoint slice")
	require.NoError(t, r.Delete(t.Context(), c, p))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.Read(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"POST", "GET", "PATCH", "DELETE"}, calls)
}

func TestEndpointSliceReadbackError(t *testing.T) {
	var calls []string
	c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == "POST" {
			endpointSliceResponse(t, w, "example", "default", "uid", "IPv4", `[]`, `[]`)
			return
		}
		endpointSliceStatus(t, w, http.StatusForbidden, "Forbidden")
	})
	_, err := endpointSlice().Create(t.Context(), c)
	require.True(t, apierrors.IsForbidden(err), "%v", err)
	require.Equal(t, []string{"POST", "GET"}, calls)
}
