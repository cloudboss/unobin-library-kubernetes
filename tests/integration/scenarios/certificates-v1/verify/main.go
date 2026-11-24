package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	explicitName    = "unobin-it-csr-client"
	generatedPrefix = "unobin-it-csr-generated-"
	scenarioLabel   = "test.unobin.cloudboss.io/scenario"
	scenarioName    = "certificates-v1"
)

type issuance struct {
	Certificate string `json:"certificate"`
	Request     string `json:"request"`
	Metadata    struct {
		Name         string            `json:"name"`
		UID          string            `json:"uid"`
		GenerateName string            `json:"generate-name"`
		Labels       map[string]string `json:"labels"`
		Annotations  map[string]string `json:"annotations"`
	} `json:"metadata"`
}

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	dir, phase := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	if dir == "" || (phase != "applied" && phase != "updated" && phase != "destroyed") {
		return fmt.Errorf("VERIFY_BUILD_DIR and a valid VERIFY_PHASE are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clientset, err := client.New(nil)
	if err != nil {
		return err
	}
	api := clientset.CertificatesV1().CertificateSigningRequests()
	list, err := api.List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{scenarioLabel: scenarioName}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario CSRs: %w", err)
	}
	if len(list.Items) != 0 {
		return fmt.Errorf("temporary CSR %s is still present after %s", list.Items[0].Name, phase)
	}

	current, err := readIssuance(ctx, dir, phase)
	if err != nil {
		return err
	}
	if err := current.verifyMetadata(phase); err != nil {
		return err
	}
	if err := current.verifyCertificate(); err != nil {
		return err
	}
	if _, err := api.Get(ctx, current.Metadata.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected CSR %s to be absent, got %v", current.Metadata.Name, err)
	}
	if phase == "destroyed" {
		if err := verifyEmptyState(ctx, dir); err != nil {
			return err
		}
	}
	fmt.Printf("ok: CSR certificate and cleanup after %s\n", phase)
	return nil
}

func readIssuance(ctx context.Context, dir, phase string) (issuance, error) {
	path := filepath.Join(dir, "csr-issuance.json")
	var prior issuance
	if phase != "applied" {
		body, err := os.ReadFile(path)
		if err != nil {
			return issuance{}, fmt.Errorf("read previous CSR issuance: %w", err)
		}
		if err := json.Unmarshal(body, &prior); err != nil {
			return issuance{}, fmt.Errorf("decode previous CSR issuance: %w", err)
		}
	}
	if phase == "destroyed" {
		return prior, nil
	}

	config := "config.ub"
	if phase == "updated" {
		config = "update/config.ub"
	}
	var result struct {
		Outputs issuance `json:"outputs"`
	}
	if err := factoryResult(ctx, dir, &result, "output", "-c", config); err != nil {
		return issuance{}, err
	}
	current := result.Outputs
	if phase == "updated" && (current.Metadata.UID == prior.Metadata.UID ||
		current.Certificate == prior.Certificate || current.Request != prior.Request) {
		return issuance{}, fmt.Errorf("generated-name replacement did not reissue the certificate")
	}
	body, err := json.Marshal(current)
	if err != nil {
		return issuance{}, err
	}
	return current, os.WriteFile(path, body, 0o600)
}

func (out issuance) verifyMetadata(phase string) error {
	m := out.Metadata
	if phase == "applied" && (m.Name != explicitName || m.GenerateName != "") {
		return fmt.Errorf("expected explicit CSR name, got %q with prefix %q", m.Name, m.GenerateName)
	}
	if phase != "applied" && (m.GenerateName != generatedPrefix ||
		m.Name == generatedPrefix || !strings.HasPrefix(m.Name, generatedPrefix)) {
		return fmt.Errorf("expected generated CSR name, got %q with prefix %q", m.Name, m.GenerateName)
	}
	if m.UID == "" || !maps.Equal(m.Labels, map[string]string{scenarioLabel: scenarioName}) ||
		!maps.Equal(m.Annotations, map[string]string{
			"TEST.UNOBIN.CLOUDBOSS.IO/Case": "client-certificate",
		}) {
		return fmt.Errorf("unexpected CSR metadata: %+v", m)
	}
	return nil
}

func (out issuance) verifyCertificate() error {
	requestPEM, rest := pem.Decode([]byte(out.Request))
	if requestPEM == nil || requestPEM.Type != "CERTIFICATE REQUEST" || len(rest) != 0 {
		return fmt.Errorf("expected a PKCS#10 request in factory outputs")
	}
	request, err := x509.ParseCertificateRequest(requestPEM.Bytes)
	if err != nil {
		return fmt.Errorf("parse CSR fixture: %w", err)
	}
	if err := request.CheckSignature(); err != nil {
		return fmt.Errorf("verify CSR fixture signature: %w", err)
	}
	certificatePEM, _ := pem.Decode([]byte(out.Certificate))
	if certificatePEM == nil || certificatePEM.Type != "CERTIFICATE" {
		return fmt.Errorf("expected a PEM certificate in factory outputs")
	}
	certificate, err := x509.ParseCertificate(certificatePEM.Bytes)
	if err != nil {
		return fmt.Errorf("parse issued certificate: %w", err)
	}
	if !reflect.DeepEqual(certificate.Subject.Names, request.Subject.Names) {
		return fmt.Errorf("issued certificate subject differs from the request")
	}
	requestKey, ok := request.PublicKey.(ed25519.PublicKey)
	if !ok || !requestKey.Equal(certificate.PublicKey) {
		return fmt.Errorf("issued certificate public key differs from the Ed25519 request key")
	}
	if certificate.KeyUsage != x509.KeyUsageDigitalSignature ||
		!slices.Equal(certificate.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		return fmt.Errorf("unexpected usage of issued client certificate")
	}
	return nil
}

func verifyEmptyState(ctx context.Context, dir string) error {
	var result struct {
		Entries []struct{ Address string } `json:"entries"`
	}
	if err := factoryResult(ctx, dir, &result, "state", "list", "-c", "update/config.ub"); err != nil {
		return err
	}
	if len(result.Entries) != 0 {
		return fmt.Errorf("unexpected state entry %q after destroy", result.Entries[0].Address)
	}
	return nil
}

func factoryResult(ctx context.Context, dir string, result any, args ...string) error {
	args = append(args, "--format", "json")
	command := exec.CommandContext(ctx, filepath.Join(dir, scenarioName), args...)
	command.Dir = dir
	body, err := command.Output()
	if err != nil {
		return fmt.Errorf("run factory %s: %w\n%s", args[0], err, body)
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("decode factory %s result: %w", args[0], err)
	}
	return nil
}
