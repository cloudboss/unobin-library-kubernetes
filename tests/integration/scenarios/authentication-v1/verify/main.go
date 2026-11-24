package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	auth "github.com/cloudboss/unobin-library-kubernetes/internal/api/authentication/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const namespace = "default"

type tokenRequestPrior = runtime.Prior[
	auth.TokenRequestResource, *auth.TokenRequestResourceOutput, *config.Configuration,
]

type identity struct {
	Name string
	UID  string
}

type fixtures struct {
	Account identity
	Binding identity
}

type outputs struct {
	Fixtures  fixtures
	Audiences []string
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	if os.Getenv("UNOBIN_KUBERNETES_INTEGRATION") != "1" || os.Getenv("KUBECONFIG") == "" {
		return fmt.Errorf("integration verification requires an explicit Kubernetes test cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "token-request-fixtures.json")
	var initial fixtures
	if phase != "applied" {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read initial fixture identities: %w", err)
		}
		if err := json.Unmarshal(body, &initial); err != nil {
			return fmt.Errorf("decode initial fixture identities: %w", err)
		}
	}
	if phase == "destroyed" {
		return verifyFixtures(ctx, api.CoreV1(), initial, "")
	}
	configPath, revision := "config.ub", "initial"
	var wantAudiences []string
	if phase == "updated" {
		configPath, revision = "update/config.ub", "updated"
		wantAudiences = []string{"unobin-test-audience"}
	}
	current, err := loadOutputs(ctx, dir, configPath)
	if err != nil {
		return err
	}
	if !slices.Equal(current.Audiences, wantAudiences) {
		return fmt.Errorf("factory audiences differ from the %s configuration", phase)
	}
	if phase == "updated" && current.Fixtures != initial {
		return fmt.Errorf("fixture identities changed after update")
	}
	// Issuing from the verifier keeps real credentials out of persisted factory state.
	if err := verifyIssuance(ctx, current); err != nil {
		return err
	}
	if err := verifyFixtures(ctx, api.CoreV1(), current.Fixtures, revision); err != nil {
		return err
	}
	if phase == "applied" {
		body, err := json.Marshal(current.Fixtures)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("ok: TokenRequest issuance matches the %s configuration\n", phase)
	return nil
}

func loadOutputs(ctx context.Context, dir, configPath string) (outputs, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "authentication-v1"),
		"output", "-c", configPath, "--format", "json")
	cmd.Dir = dir
	body, err := cmd.Output()
	if err != nil {
		return outputs{}, fmt.Errorf("read factory outputs: %w", err)
	}
	var result struct{ Outputs outputs }
	if err := json.Unmarshal(body, &result); err != nil {
		return outputs{}, fmt.Errorf("decode factory outputs: %w", err)
	}
	for _, id := range []identity{result.Outputs.Fixtures.Account, result.Outputs.Fixtures.Binding} {
		if id.Name == "" || id.UID == "" {
			return outputs{}, fmt.Errorf("missing factory fixture identity")
		}
	}
	return result.Outputs, nil
}

func verifyIssuance(ctx context.Context, current outputs) error {
	request := auth.TokenRequestResource{
		Metadata: auth.TokenRequestMetadata{Name: current.Fixtures.Account.Name},
	}
	initial, err := request.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("issue default TokenRequest: %w", err)
	}
	if initial.Token == "" || initial.Metadata.Name != request.Metadata.Name ||
		initial.Metadata.Namespace != namespace || len(initial.Spec.Audiences) == 0 ||
		ptr.Deref(initial.Spec.ExpirationSeconds, 0) <= 0 || initial.Spec.BoundObjectRef != nil {
		return fmt.Errorf("default TokenRequest issuance or projected fields differ")
	}
	if err := verifyLocalTeardown(ctx, request, initial); err != nil {
		return err
	}
	if len(current.Audiences) == 0 {
		return nil
	}

	binding := current.Fixtures.Binding
	request.Spec = &auth.TokenRequestSpec{
		Audiences: &current.Audiences, ExpirationSeconds: new(int64(600)),
		BoundObjectRef: &auth.TokenRequestBoundObjectRef{
			APIVersion: new("v1"), Kind: new("Secret"), Name: &binding.Name, UID: &binding.UID,
		},
	}
	replaced, err := request.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("issue replacement TokenRequest: %w", err)
	}
	if replaced.Token == "" || replaced.Token == initial.Token ||
		!slices.Equal(replaced.Spec.Audiences, current.Audiences) ||
		ptr.Deref(replaced.Spec.ExpirationSeconds, 0) <= 0 ||
		replaced.Spec.BoundObjectRef == nil ||
		*replaced.Spec.BoundObjectRef != (auth.TokenRequestBoundObjectRefOutput{
			APIVersion: "v1", Kind: "Secret", Name: binding.Name, UID: binding.UID,
		}) {
		return fmt.Errorf("replacement TokenRequest issuance or projected fields differ")
	}
	return verifyLocalTeardown(ctx, request, replaced)
}

func verifyLocalTeardown(
	ctx context.Context, request auth.TokenRequestResource, issued *auth.TokenRequestResourceOutput,
) error {
	unreachable := &config.Configuration{Server: new("https://127.0.0.1:1")}
	prior := tokenRequestPrior{Inputs: request, Outputs: issued}
	observed, err := request.Read(ctx, unreachable, prior)
	if err != nil {
		return fmt.Errorf("refresh TokenRequest: %w", err)
	}
	if observed == nil || !reflect.DeepEqual(observed, issued) {
		return fmt.Errorf("refresh changed the issued TokenRequest")
	}
	return request.Delete(ctx, unreachable, prior)
}

func verifyFixtures(
	ctx context.Context, api typedcorev1.CoreV1Interface, f fixtures, revision string,
) error {
	account, accountErr := api.ServiceAccounts(namespace).Get(ctx, f.Account.Name, metav1.GetOptions{})
	binding, bindingErr := api.Secrets(namespace).Get(ctx, f.Binding.Name, metav1.GetOptions{})
	if revision == "" {
		if !apierrors.IsNotFound(accountErr) || !apierrors.IsNotFound(bindingErr) {
			return fmt.Errorf("factory destroy did not remove the ServiceAccount and bound Secret")
		}
		return nil
	}
	if accountErr != nil {
		return fmt.Errorf("get ServiceAccount after TokenRequest teardown: %w", accountErr)
	}
	if bindingErr != nil {
		return fmt.Errorf("get bound Secret after TokenRequest teardown: %w", bindingErr)
	}
	if string(account.UID) != f.Account.UID || string(binding.UID) != f.Binding.UID ||
		account.Annotations["test.unobin.cloudboss.io/revision"] != revision {
		return fmt.Errorf("fixture identities or ServiceAccount revision differ after teardown")
	}
	return nil
}
