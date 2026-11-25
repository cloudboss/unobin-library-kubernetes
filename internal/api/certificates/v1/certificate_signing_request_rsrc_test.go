package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	certificates "github.com/cloudboss/unobin-library-kubernetes/api/certificates/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/certificates/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/api/certificates/v1/testdata/ub/token"
)

const (
	csrPath    = "/apis/certificates.k8s.io/v1/certificatesigningrequests"
	csrItem    = csrPath + "/actual"
	requestPEM = "-----BEGIN CERTIFICATE REQUEST-----\nc3ludGhldGlj\n" +
		"-----END CERTIFICATE REQUEST-----\n"
	certPEM = "-----BEGIN CERTIFICATE-----\nZmlyc3Q=\n-----END CERTIFICATE-----\n" +
		"-----BEGIN CERTIFICATE-----\nc2Vjb25k\n-----END CERTIFICATE-----\n"
)

type csrCall struct {
	Method string
	Path   string
	Body   []byte
}

type csrResponse struct {
	csrCall
	status int
	reply  any
}

func scriptedCSR(t *testing.T, steps []csrResponse) *config.Configuration {
	t.Helper()
	var mu sync.Mutex
	var calls []csrCall
	c := csrServer(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, err := io.ReadAll(req.Body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		index := len(calls)
		calls = append(calls, csrCall{req.Method, req.URL.Path, body})
		if index >= len(steps) {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		step := steps[index]
		if step.status != 0 {
			w.WriteHeader(step.status)
		}
		if step.reply != nil {
			assert.NoError(t, json.NewEncoder(w).Encode(step.reply))
		}
	})
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		var expected, actual []string
		for _, step := range steps {
			expected = append(expected, step.Method+" "+step.Path)
		}
		for _, call := range calls {
			actual = append(actual, call.Method+" "+call.Path)
		}
		if assert.Equal(t, expected, actual) {
			for i, step := range steps {
				if step.Body == nil {
					assert.Empty(t, calls[i].Body, "call %d", i)
					continue
				}
				assert.JSONEq(t, string(step.Body), string(calls[i].Body), "call %d", i)
			}
		}
	})
	return c
}

func csrServer(t *testing.T, handler http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, req)
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, InsecureSkipTLSVerify: new(true)}
}

func csrJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}

func csrFailure(call csrCall, code int, reason metav1.StatusReason) csrResponse {
	return csrResponse{
		csrCall: call, status: code,
		reply: metav1.Status{Status: "Failure", Code: int32(code), Reason: reason},
	}
}

func basicCSR() *svc.CertificateSigningRequestResource {
	return &svc.CertificateSigningRequestResource{
		Metadata: svc.CertificateSigningRequestMetadata{Name: new("actual")},
		Spec: svc.CertificateSigningRequestSpec{
			Request: requestPEM, SignerName: "example.test/test-signer",
		},
	}
}

func expectedCSR() *certv1.CertificateSigningRequest {
	return &certv1.CertificateSigningRequest{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "certificates.k8s.io/v1", Kind: "CertificateSigningRequest",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "actual"},
		Spec: certv1.CertificateSigningRequestSpec{
			Request: []byte(requestPEM), SignerName: "example.test/test-signer",
		},
	}
}

func createdCSR() *certv1.CertificateSigningRequest {
	obj := expectedCSR()
	obj.UID = "issued-uid"
	obj.ResourceVersion = "1"
	obj.Spec.Username = "api-client"
	obj.Labels = map[string]string{"server-added": "true"}
	return obj
}

func csrPost(t *testing.T, obj *certv1.CertificateSigningRequest) csrCall {
	t.Helper()
	return csrCall{http.MethodPost, csrPath, csrJSON(t, obj)}
}

func csrGet(reply *certv1.CertificateSigningRequest) csrResponse {
	return csrResponse{csrCall: csrCall{Method: http.MethodGet, Path: csrItem}, reply: reply}
}

func csrDelete(t *testing.T) csrCall {
	t.Helper()
	return csrCall{http.MethodDelete, csrItem,
		csrJSON(t, &metav1.DeleteOptions{
			TypeMeta:      metav1.TypeMeta{APIVersion: "certificates.k8s.io/v1", Kind: "DeleteOptions"},
			Preconditions: &metav1.Preconditions{UID: new(createdCSR().UID)},
		}),
	}
}

func issuedCSR(certificate string) *certv1.CertificateSigningRequest {
	obj := createdCSR()
	obj.Status.Certificate = []byte(certificate)
	obj.Status.Conditions = []certv1.CertificateSigningRequestCondition{{
		Type: certv1.CertificateApproved, Status: corev1.ConditionFalse,
	}}
	return obj
}

func csrApproval(t *testing.T, observed *certv1.CertificateSigningRequest) csrCall {
	t.Helper()
	want := observed.DeepCopy()
	want.Status.Certificate = nil
	want.Status.Conditions = append(want.Status.Conditions, certv1.CertificateSigningRequestCondition{
		Type: certv1.CertificateApproved, Status: corev1.ConditionTrue,
		Reason:  "TerraformAutoApprove",
		Message: "This CertificateSigningRequest was auto-approved by Terraform",
	})
	return csrCall{http.MethodPut, csrItem + "/approval", csrJSON(t, want)}
}

func TestCertificateSigningRequestApprovalAndFinalGet(t *testing.T) {
	input := basicCSR()
	input.Metadata.Name = nil
	input.Metadata.GenerateName = new("fixture-")
	input.Metadata.Annotations = &map[string]string{"MIXED.EXAMPLE/Ready": "yes"}
	input.Metadata.Labels = &map[string]string{"empty": ""}
	request := expectedCSR()
	request.Name, request.GenerateName = "", "fixture-"
	request.Annotations = map[string]string{"MIXED.EXAMPLE/Ready": "yes"}
	request.Labels = map[string]string{"empty": ""}
	fetched := createdCSR()
	fetched.GenerateName = "fixture-"
	fetched.Status.Conditions = []certv1.CertificateSigningRequestCondition{{
		Type: certv1.CertificateFailed, Status: corev1.ConditionTrue, Reason: "external",
	}}
	fetched.Status.Certificate = []byte("must be cleared")
	final := createdCSR()
	final.Status.Certificate = []byte(certPEM)
	config := scriptedCSR(t, []csrResponse{
		{csrCall: csrPost(t, request), reply: createdCSR()},
		csrGet(fetched),
		{csrCall: csrApproval(t, fetched), reply: fetched},
		csrGet(issuedCSR("poll bytes")),
		csrGet(final),
		csrFailure(csrDelete(t), http.StatusForbidden, metav1.StatusReasonForbidden),
	})
	got, err := input.Create(t.Context(), config)
	require.NoError(t, err)
	require.Equal(t, &svc.CertificateSigningRequestResourceOutput{
		Server: *config.Server, APIVersion: "certificates.k8s.io/v1",
		Kind: "CertificateSigningRequest", Certificate: certPEM,
		Metadata: svc.CertificateSigningRequestMetadataOutput{
			Name: "actual", UID: "issued-uid", GenerateName: "fixture-",
			Annotations: map[string]string{"MIXED.EXAMPLE/Ready": "yes"},
			Labels:      map[string]string{"empty": ""},
		},
	}, got)
}

func TestCertificateSigningRequestExpirationAndUsages(t *testing.T) {
	for _, tc := range []struct {
		name           string
		expiration     *int64
		wantExpiration *int32
		usages         *[]string
		wantUsages     []certv1.KeyUsage
	}{
		{"omitted", nil, nil, nil, nil},
		{"zero", new(int64(0)), nil, &[]string{}, nil},
		{"negative", new(int64(-1)), nil, nil, nil},
		{"below-minimum", new(int64(599)), nil, nil, nil},
		{"minimum", new(int64(600)), new(int32(600)),
			&[]string{"digital signature", "client auth", "digital signature"},
			[]certv1.KeyUsage{"client auth", "digital signature"}},
		{"week", new(int64(604800)), new(int32(604800)),
			&[]string{"client auth", "digital signature"},
			[]certv1.KeyUsage{"client auth", "digital signature"}},
		{"narrowed", new(int64(2147483648)), new(int32(-2147483648)), nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := basicCSR()
			input.AutoApprove = new(false)
			input.Metadata.Labels = &map[string]string{}
			input.Metadata.Annotations = &map[string]string{}
			input.Spec.ExpirationSeconds, input.Spec.Usages = tc.expiration, tc.usages
			want := expectedCSR()
			want.Spec.ExpirationSeconds, want.Spec.Usages = tc.wantExpiration, tc.wantUsages
			c := scriptedCSR(t, []csrResponse{
				{csrCall: csrPost(t, want), reply: createdCSR()},
				csrGet(issuedCSR(certPEM)), csrGet(issuedCSR(certPEM)),
				{csrCall: csrDelete(t), reply: metav1.Status{Status: "Success"}},
			})
			got, err := input.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, certPEM, got.Certificate)
		})
	}
}

func TestCertificateSigningRequestPostFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		reason metav1.StatusReason
		edit   func(*svc.CertificateSigningRequestResource, *certv1.CertificateSigningRequest)
	}{
		{"already-exists", http.StatusConflict, metav1.StatusReasonAlreadyExists, nil},
		{"request", http.StatusUnprocessableEntity, metav1.StatusReasonInvalid,
			func(r *svc.CertificateSigningRequestResource, w *certv1.CertificateSigningRequest) {
				r.Spec.Request = "malformed"
				w.Spec.Request = []byte("malformed")
			}},
		{"signer", http.StatusUnprocessableEntity, metav1.StatusReasonInvalid,
			func(r *svc.CertificateSigningRequestResource, w *certv1.CertificateSigningRequest) {
				r.Spec.SignerName, w.Spec.SignerName = "invalid", "invalid"
			}},
		{"usage", http.StatusUnprocessableEntity, metav1.StatusReasonInvalid,
			func(r *svc.CertificateSigningRequestResource, w *certv1.CertificateSigningRequest) {
				r.Spec.Usages = &[]string{"unsupported"}
				w.Spec.Usages = []certv1.KeyUsage{"unsupported"}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, want := basicCSR(), expectedCSR()
			if tc.edit != nil {
				tc.edit(r, want)
			}
			c := scriptedCSR(t, []csrResponse{csrFailure(csrPost(t, want), tc.code, tc.reason)})
			out, err := r.Create(t.Context(), c)
			require.Equal(t, tc.reason, apierrors.ReasonForError(err))
			require.Nil(t, out)
		})
	}
}

func TestCertificateSigningRequestUIDProtection(t *testing.T) {
	for _, stage := range []string{"approval", "poll", "final"} {
		t.Run(stage, func(t *testing.T) {
			r := basicCSR()
			r.AutoApprove = new(stage == "approval")
			other := issuedCSR("successor certificate")
			other.UID = "different-uid"
			steps := []csrResponse{{csrCall: csrPost(t, expectedCSR()), reply: createdCSR()}}
			if stage == "final" {
				steps = append(steps, csrGet(issuedCSR(certPEM)))
			}
			steps = append(steps, csrGet(other),
				csrFailure(csrDelete(t), http.StatusConflict, metav1.StatusReasonConflict))
			out, err := r.Create(t.Context(), scriptedCSR(t, steps))
			require.ErrorContains(t, err, "UID changed from issued-uid to different-uid")
			require.Nil(t, out)
		})
	}
}

func TestCertificateSigningRequestApprovalRetries(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		conflicts               int
		getError, approvalError bool
	}{
		{name: "updated-version", conflicts: 1},
		{name: "exhausted", conflicts: 5},
		{name: "get-error", getError: true},
		{name: "approval-error", approvalError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []csrResponse{{csrCall: csrPost(t, expectedCSR()), reply: createdCSR()}}
			for i := range tc.conflicts {
				obj := createdCSR()
				obj.ResourceVersion = fmt.Sprint(i + 1)
				steps = append(steps, csrGet(obj), csrFailure(csrApproval(t, obj),
					http.StatusConflict, metav1.StatusReasonConflict))
			}
			var reason metav1.StatusReason
			switch {
			case tc.conflicts == 5:
				reason = metav1.StatusReasonConflict
			case tc.getError:
				reason = metav1.StatusReasonForbidden
				steps = append(steps, csrFailure(csrGet(nil).csrCall, http.StatusForbidden, reason))
			case tc.approvalError:
				reason = metav1.StatusReasonInvalid
				obj := createdCSR()
				steps = append(steps, csrGet(obj), csrFailure(csrApproval(t, obj),
					http.StatusUnprocessableEntity, reason))
			default:
				obj := createdCSR()
				obj.ResourceVersion = "2"
				steps = append(steps, csrGet(obj), csrResponse{csrCall: csrApproval(t, obj), reply: obj},
					csrGet(issuedCSR(certPEM)), csrGet(issuedCSR(certPEM)))
			}
			steps = append(steps,
				csrFailure(csrDelete(t), http.StatusNotFound, metav1.StatusReasonNotFound))
			out, err := basicCSR().Create(t.Context(), scriptedCSR(t, steps))
			if reason != "" {
				require.Equal(t, reason, apierrors.ReasonForError(err))
				require.Nil(t, out)
				return
			}
			require.NoError(t, err)
			require.Equal(t, certPEM, out.Certificate)
		})
	}
}

func TestCertificateSigningRequestWaitsForApprovalAndCertificate(t *testing.T) {
	input := basicCSR()
	input.AutoApprove = new(false)
	withoutApproval := createdCSR()
	withoutApproval.Status.Certificate = []byte(certPEM)
	c := scriptedCSR(t, []csrResponse{
		{csrCall: csrPost(t, expectedCSR()), reply: createdCSR()},
		csrGet(createdCSR()),
		csrGet(issuedCSR("")),
		csrGet(withoutApproval),
		csrGet(issuedCSR(certPEM)),
		csrGet(issuedCSR(certPEM)),
		{csrCall: csrDelete(t), reply: metav1.Status{Status: "Success"}},
	})
	out, err := input.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, certPEM, out.Certificate)
}

func TestCertificateSigningRequestGetFailure(t *testing.T) {
	for _, stage := range []string{"poll", "final"} {
		t.Run(stage, func(t *testing.T) {
			r := basicCSR()
			r.AutoApprove = new(false)
			steps := []csrResponse{{csrCall: csrPost(t, expectedCSR()), reply: createdCSR()}}
			if stage == "final" {
				steps = append(steps, csrGet(issuedCSR(certPEM)))
			}
			steps = append(steps,
				csrFailure(csrGet(nil).csrCall, http.StatusNotFound, metav1.StatusReasonNotFound),
				csrFailure(csrDelete(t), http.StatusForbidden, metav1.StatusReasonForbidden))
			out, err := r.Create(t.Context(), scriptedCSR(t, steps))
			require.ErrorContains(t, err, "wait for CertificateSigningRequest issuance")
			require.True(t, apierrors.IsNotFound(err), "%v", err)
			require.Nil(t, out)
		})
	}
}

func TestCertificateSigningRequestPendingAndCancellation(t *testing.T) {
	for _, condition := range []certv1.RequestConditionType{
		certv1.CertificateDenied, certv1.CertificateFailed, certv1.CertificateApproved,
	} {
		t.Run(string(condition), func(t *testing.T) {
			input := basicCSR()
			input.AutoApprove = new(false)
			input.CreateTimeout = new("175ms")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			var calls []string
			c := csrServer(t, func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, req.Method+" "+req.URL.Path)
				obj := createdCSR()
				if req.Method == http.MethodGet {
					obj.Status.Conditions = []certv1.CertificateSigningRequestCondition{{
						Type: condition, Status: corev1.ConditionTrue,
					}}
					if condition == certv1.CertificateApproved {
						cancel()
					}
				}
				assert.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			start := time.Now()
			out, err := input.Create(ctx, c)
			require.ErrorContains(t, err, "wait for CertificateSigningRequest issuance")
			if condition == certv1.CertificateApproved {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.GreaterOrEqual(t, time.Since(start), 175*time.Millisecond)
			}
			require.Nil(t, out)
			mu.Lock()
			defer mu.Unlock()
			require.GreaterOrEqual(t, len(calls), 2)
			require.Equal(t, "POST "+csrPath, calls[0])
			for _, call := range calls[1:] {
				require.Equal(t, "GET "+csrItem, call)
			}
		})
	}
}

func TestCertificateSigningRequestPostUsesCreateDeadline(t *testing.T) {
	input := basicCSR()
	input.CreateTimeout = new("175ms")
	received := make(chan csrCall, 1)
	c := csrServer(t, func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- csrCall{req.Method, req.URL.Path, body}
		select {
		case <-req.Context().Done():
		case <-time.After(time.Second):
		}
	})
	out, err := input.Create(t.Context(), c)
	require.Nil(t, out)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)
	select {
	case call := <-received:
		require.Equal(t, http.MethodPost, call.Method)
		require.Equal(t, csrPath, call.Path)
		require.JSONEq(t, string(csrJSON(t, expectedCSR())), string(call.Body))
	default:
		t.Fatal("create did not submit the CSR before the deadline")
	}
}

var csrSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/certificates/v1")
	if err == nil && len(warnings) > 0 {
		err = fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, err
})

func csrLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := csrSchema()
	require.NoError(t, err)
	lib := certificates.Library()
	resource := schema.Resources["certificate-signing-request"]
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.certificate-signing-request": resource.Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"resource.certificate-signing-request": resource.Defaults,
	}
	return lib
}

func csrInputs(server string) map[string]any {
	return map[string]any{
		"server": server, "metadata": map[string]any{"name": "actual"},
		"request": requestPEM, "signer-name": "example.test/test-signer", "auto-approve": false,
		"expiration-seconds": nil, "usages": nil, "create-timeout": nil,
	}
}

func csrExecutor(
	t *testing.T, fixture, dir string, lib *runtime.Library, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub", fixture))
	require.NoError(t, err)
	src, err := syntax.ParseSource(filepath.Base(fixture), body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"certificates": lib, "tokens": token.Library()}
	store, err := local.NewStore(dir, "certificate-signing-request", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(src.Factory.Body, libs), SyntaxSource: &src.Factory.Body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "certificate-signing-request", Version: "v0"},
	}
}

func applyCSR(t *testing.T, e *runtime.Executor, p *runtime.Plan) (*runtime.ExecResult, error) {
	t.Helper()
	encoded, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	return e.ApplyPlan(t.Context(), saved)
}

func planCSR(
	t *testing.T, e *runtime.Executor, decision runtime.Decision,
) (*runtime.Plan, *runtime.PlanStep) {
	t.Helper()
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range p.Steps {
		if step.Address == "resource.subject" {
			require.Equal(t, decision, step.Decision)
			return p, step
		}
	}
	t.Fatal("plan omitted resource.subject")
	return nil, nil
}

func applyCSRDecision(
	t *testing.T, e *runtime.Executor, decision runtime.Decision,
) *runtime.ExecResult {
	t.Helper()
	p, _ := planCSR(t, e, decision)
	result, err := applyCSR(t, e, p)
	require.NoError(t, err)
	return result
}

func csrIssuance(
	t *testing.T, requested *certv1.CertificateSigningRequest,
	name string, uid types.UID, certificate string, autoApprove bool,
) []csrResponse {
	t.Helper()
	obj := requested.DeepCopy()
	obj.Name, obj.UID, obj.ResourceVersion = name, uid, "1"
	item := csrPath + "/" + name
	get := csrCall{Method: http.MethodGet, Path: item}
	steps := []csrResponse{{csrCall: csrPost(t, requested), reply: obj}}
	if autoApprove {
		approval := csrApproval(t, obj)
		approval.Path = item + "/approval"
		steps = append(steps, csrResponse{csrCall: get, reply: obj},
			csrResponse{csrCall: approval, reply: obj})
	}
	issued := obj.DeepCopy()
	issued.Status = issuedCSR(certificate).Status
	deleteOptions := &metav1.DeleteOptions{
		TypeMeta:      metav1.TypeMeta{APIVersion: "certificates.k8s.io/v1", Kind: "DeleteOptions"},
		Preconditions: &metav1.Preconditions{UID: &uid},
	}
	return append(steps,
		csrResponse{csrCall: get, reply: issued},
		csrResponse{csrCall: get, reply: issued},
		csrResponse{csrCall: csrCall{http.MethodDelete, item, csrJSON(t, deleteOptions)},
			reply: metav1.Status{Status: "Success"}},
	)
}

func TestCertificateSigningRequestStateOnlyLifecycle(t *testing.T) {
	c := scriptedCSR(t, csrIssuance(t, expectedCSR(), "actual", "first-uid", certPEM, false))
	inputs := csrInputs(*c.Server)
	dir, lib := t.TempDir(), csrLibrary(t)
	executor := func() *runtime.Executor {
		return csrExecutor(t, "factory.ub", dir, lib, inputs)
	}
	first := applyCSRDecision(t, executor(), runtime.DecisionCreate)
	require.Equal(t, map[string]any{"certificate": certPEM}, first.Outputs)

	// Later operations can use configuration that cannot establish a Kubernetes client.
	inputs["server"] = "https://127.0.0.1:1"
	unchanged := applyCSRDecision(t, executor(), runtime.DecisionNoOp)
	require.Equal(t, first.Outputs, unchanged.Outputs)
	inputs["create-timeout"] = "30s"
	updated := applyCSRDecision(t, executor(), runtime.DecisionUpdate)
	require.Equal(t, first.Outputs, updated.Outputs)
	snapshot, err := executor().Store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.subject")
	require.Equal(t, "30s", entry.Inputs["create-timeout"])
	require.Equal(t, *c.Server, entry.Outputs["server"])
	require.Equal(t, map[string]any{"name": "actual", "uid": "first-uid", "generate-name": "",
		"labels": map[string]any{}, "annotations": map[string]any{}}, entry.Outputs["metadata"])

	destroy := executor()
	destroy.Destroy = true
	applyCSRDecision(t, destroy, runtime.DecisionDestroy)
	snapshot, err = destroy.Store.Current()
	require.NoError(t, err)
	require.Nil(t, snapshot.Find("resource.subject"))
}

func TestCertificateSigningRequestReplacement(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reason    string
		edit      func(map[string]any, *certv1.CertificateSigningRequest)
		generated bool
	}{
		{"name", "metadata", func(in map[string]any, obj *certv1.CertificateSigningRequest) {
			in["metadata"], obj.Name = map[string]any{"name": "new-name"}, "new-name"
		}, false},
		{"generate-name", "metadata", func(in map[string]any, obj *certv1.CertificateSigningRequest) {
			in["metadata"], obj.GenerateName = map[string]any{"generate-name": "new-"}, "new-"
		}, true},
		{"labels", "metadata", func(in map[string]any, obj *certv1.CertificateSigningRequest) {
			obj.Labels = map[string]string{"a": "b"}
			in["metadata"].(map[string]any)["labels"] = map[string]any{"a": "b"}
		}, false},
		{"annotations", "metadata", func(in map[string]any, obj *certv1.CertificateSigningRequest) {
			obj.Annotations = map[string]string{"x": "y"}
			in["metadata"].(map[string]any)["annotations"] = map[string]any{"x": "y"}
		}, false},
		{"request", "spec", func(in map[string]any, obj *certv1.CertificateSigningRequest) {
			in["request"], obj.Spec.Request = requestPEM+"\n", []byte(requestPEM+"\n")
		}, false},
		{"signer-name", "spec", func(in map[string]any, obj *certv1.CertificateSigningRequest) {
			in["signer-name"], obj.Spec.SignerName = "changed.test/signer", "changed.test/signer"
		}, false},
		{"expiration-seconds omitted on request", "spec",
			func(in map[string]any, _ *certv1.CertificateSigningRequest) {
				in["expiration-seconds"] = int64(599)
			}, false},
		{"usages membership", "spec", func(in map[string]any, obj *certv1.CertificateSigningRequest) {
			in["usages"] = []any{"client auth", "server auth"}
			obj.Spec.Usages = []certv1.KeyUsage{"client auth", "server auth"}
		}, false},
		{"approval", "auto-approve",
			func(in map[string]any, _ *certv1.CertificateSigningRequest) {
				in["auto-approve"] = true
			}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initialRequest := expectedCSR()
			if tc.generated {
				initialRequest.Name, initialRequest.GenerateName = "", "initial-"
			}
			initialRequest.Spec.Usages = []certv1.KeyUsage{"client auth", "digital signature"}
			initialInputs := csrInputs("")
			initialInputs["usages"] = []any{"digital signature", "client auth"}
			if tc.generated {
				initialInputs["metadata"] = map[string]any{"generate-name": "initial-"}
			}
			inputs := csrInputs("")
			inputs["metadata"] = maps.Clone(initialInputs["metadata"].(map[string]any))
			inputs["usages"] = initialInputs["usages"]
			newRequest := initialRequest.DeepCopy()
			tc.edit(inputs, newRequest)
			initialName := initialRequest.Name
			if tc.generated {
				initialName = "initial-actual"
			}
			steps := csrIssuance(t, initialRequest, initialName, "first-uid", certPEM, false)
			actualName := newRequest.Name
			if tc.generated {
				actualName = "new-actual"
			}
			steps = append(steps, csrIssuance(t, newRequest, actualName,
				"second-uid", certPEM+"\n", inputs["auto-approve"] == true)...)
			c := scriptedCSR(t, steps)
			initialInputs["server"], inputs["server"] = *c.Server, *c.Server
			dir, lib := t.TempDir(), csrLibrary(t)
			initial := csrExecutor(t, "factory.ub", dir, lib, initialInputs)
			applyCSRDecision(t, initial, runtime.DecisionCreate)
			executor := csrExecutor(t, "factory.ub", dir, lib, inputs)
			plan, step := planCSR(t, executor, runtime.DecisionReplace)
			require.Equal(t, []string{tc.reason}, step.ReplacementReasons)
			out, err := applyCSR(t, executor, plan)
			require.NoError(t, err)
			require.Equal(t, map[string]any{"certificate": certPEM + "\n"}, out.Outputs)
			snapshot, err := executor.Store.Current()
			require.NoError(t, err)
			metadata := snapshot.Find("resource.subject").Outputs["metadata"].(map[string]any)
			require.Equal(t, actualName, metadata["name"])
			require.Equal(t, "second-uid", metadata["uid"])
		})
	}
}

func TestCertificateSigningRequestEquivalentInputs(t *testing.T) {
	want := expectedCSR()
	want.Spec.Usages = []certv1.KeyUsage{"client auth", "digital signature"}
	c := scriptedCSR(t, csrIssuance(t, want, "actual", "first-uid", certPEM, true))
	inputs := csrInputs(*c.Server)
	inputs["usages"] = []any{"digital signature", "client auth"}
	inputs["auto-approve"] = nil
	dir, lib := t.TempDir(), csrLibrary(t)
	makeExecutor := func() *runtime.Executor {
		return csrExecutor(t, "factory.ub", dir, lib, inputs)
	}
	applyCSRDecision(t, makeExecutor(), runtime.DecisionCreate)
	inputs["auto-approve"] = true
	inputs["usages"] = []any{"client auth", "digital signature", "client auth"}
	inputs["metadata"].(map[string]any)["labels"] = map[string]any{}
	inputs["metadata"].(map[string]any)["annotations"] = map[string]any{}
	result := applyCSRDecision(t, makeExecutor(), runtime.DecisionNoOp)
	require.Equal(t, map[string]any{"certificate": certPEM}, result.Outputs)
}

func assertCSRConstraint(t *testing.T, err error, rule string) {
	t.Helper()
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "constraints[")
	require.ErrorContains(t, err, rule)
}

func TestCertificateSigningRequestEvaluatedPrefix(t *testing.T) {
	want := expectedCSR()
	want.Name, want.GenerateName = "", "evaluated-"
	want.Labels = map[string]string{"empty": ""}
	want.Annotations = map[string]string{"EXAMPLE.TEST/Case": "configured"}
	want.Spec.ExpirationSeconds = new(int32(600))
	want.Spec.Usages = []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageDigitalSignature}
	c := scriptedCSR(t, csrIssuance(t, want, "evaluated-actual", "uid", certPEM, false))
	inputs := map[string]any{
		"server": *c.Server, "prefix": "INVALID", "request": requestPEM,
	}
	dir, lib := t.TempDir(), csrLibrary(t)
	executor := func() *runtime.Executor {
		return csrExecutor(t, "evaluated/factory.ub", dir, lib, inputs)
	}
	_, err := executor().Plan(t.Context())
	assertCSRConstraint(t, err, "metadata.generate-name")

	inputs["prefix"] = "evaluated-"
	result := applyCSRDecision(t, executor(), runtime.DecisionCreate)
	require.Equal(t, map[string]any{
		"api-version": "certificates.k8s.io/v1",
		"kind":        "CertificateSigningRequest",
		"server":      *c.Server,
		"certificate": certPEM,
		"request":     requestPEM,
		"signer":      "example.test/test-signer",
		"timeout":     "5m",
		"metadata": map[string]any{
			"name": "evaluated-actual", "generate-name": "evaluated-", "uid": "uid",
			"labels":      map[string]any{"empty": ""},
			"annotations": map[string]any{"EXAMPLE.TEST/Case": "configured"},
		},
	}, result.Outputs)

	inputs["prefix"] = "INVALID"
	_, err = executor().Plan(t.Context())
	assertCSRConstraint(t, err, "metadata.generate-name")
}

func TestCertificateSigningRequestSavedNoOpAndHistoricalDestroy(t *testing.T) {
	c := scriptedCSR(t, csrIssuance(t, expectedCSR(), "actual", "first-uid", certPEM, false))
	inputs := csrInputs(*c.Server)
	dir, lib := t.TempDir(), csrLibrary(t)
	executor := func() *runtime.Executor {
		return csrExecutor(t, "factory.ub", dir, lib, inputs)
	}
	applyCSRDecision(t, executor(), runtime.DecisionCreate)
	applyCSRDecision(t, executor(), runtime.DecisionNoOp)

	metadata := inputs["metadata"].(map[string]any)
	metadata["generate-name"] = ""
	constraints := lib.Constraints
	lib.Constraints = nil
	savedNoOp, _ := planCSR(t, executor(), runtime.DecisionNoOp)
	lib.Constraints = constraints
	_, err := executor().Plan(t.Context())
	assertCSRConstraint(t, err, "exactly one")
	_, err = applyCSR(t, executor(), savedNoOp)
	assertCSRConstraint(t, err, "exactly one")

	lib.Constraints = nil
	_, err = applyCSR(t, executor(), savedNoOp)
	require.NoError(t, err)
	lib.Constraints = constraints
	destroy := executor()
	destroy.Destroy = true
	plan, step := planCSR(t, destroy, runtime.DecisionDestroy)
	require.Equal(t, metadata, step.PriorInputs["metadata"])
	_, err = applyCSR(t, destroy, plan)
	require.NoError(t, err)
	snapshot, err := destroy.Store.Current()
	require.NoError(t, err)
	require.Nil(t, snapshot.Find("resource.subject"))
}

func TestCertificateSigningRequestDeferredInputs(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		decision     runtime.Decision
		valid        bool
	}{
		{"create-valid", "pending-", runtime.DecisionCreate, true},
		{"create-invalid", "INVALID", runtime.DecisionCreate, false},
		{"replace-valid", "pending-", runtime.DecisionReplace, true},
		{"replace-invalid", "INVALID", runtime.DecisionReplace, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []csrResponse
			if tc.decision == runtime.DecisionReplace {
				steps = csrIssuance(t, expectedCSR(), "actual", "first-uid", certPEM, false)
			}
			if tc.valid {
				want := expectedCSR()
				want.Name, want.GenerateName = "", tc.prefix
				want.Spec.Request = []byte(requestPEM + "\n")
				steps = append(steps, csrIssuance(t, want, "pending-actual",
					"new-uid", certPEM+"\n", false)...)
			}
			c := scriptedCSR(t, steps)
			dir, lib := t.TempDir(), csrLibrary(t)
			if tc.decision == runtime.DecisionReplace {
				e := csrExecutor(t, "factory.ub", dir, lib, csrInputs(*c.Server))
				applyCSRDecision(t, e, runtime.DecisionCreate)
			}
			inputs := csrInputs(*c.Server)
			inputs["request"], inputs["prefix"] = requestPEM+"\n", tc.prefix
			executor := csrExecutor(t, "deferred/factory.ub", dir, lib, inputs)
			plan, step := planCSR(t, executor, tc.decision)
			require.Equal(t, map[string][]string{
				"metadata": {"action.prefix.token"},
			}, step.UnresolvedInputs)
			_, err := applyCSR(t, executor, plan)
			if tc.valid {
				require.NoError(t, err)
			} else {
				assertCSRConstraint(t, err, "metadata.generate-name")
			}
			snapshot, err := executor.Store.Current()
			if !tc.valid && tc.decision == runtime.DecisionCreate {
				require.ErrorIs(t, err, state.ErrNoCurrent)
				return
			}
			require.NoError(t, err)
			entry := snapshot.Find("resource.subject")
			if tc.valid {
				require.Equal(t, certPEM+"\n", entry.Outputs["certificate"])
			} else {
				require.Equal(t, certPEM, entry.Outputs["certificate"])
				require.Equal(t, map[string]any{"name": "actual"}, entry.Inputs["metadata"])
			}
		})
	}
}

func TestCertificateSigningRequestFinalFailureDoesNotSaveAnIssuance(t *testing.T) {
	c := scriptedCSR(t, []csrResponse{
		{csrCall: csrPost(t, expectedCSR()), reply: createdCSR()},
		csrGet(issuedCSR(certPEM)),
		csrFailure(csrGet(nil).csrCall, http.StatusForbidden, metav1.StatusReasonForbidden),
		{csrCall: csrDelete(t), reply: metav1.Status{Status: "Success"}},
	})
	e := csrExecutor(t, "factory.ub", t.TempDir(), csrLibrary(t), csrInputs(*c.Server))
	plan, _ := planCSR(t, e, runtime.DecisionCreate)
	result, err := applyCSR(t, e, plan)
	require.ErrorContains(t, err, "read issued certificate")
	require.Nil(t, result)
	_, err = e.Store.Current()
	require.ErrorIs(t, err, state.ErrNoCurrent)
}
