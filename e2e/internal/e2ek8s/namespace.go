package e2ek8s

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	namespaceSuffixBytes = 5
	maxNamespaceNameLen  = 63
)

func CreateUniqueNamespace(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	prefix string,
) (*corev1.Namespace, error) {
	if err := validateNamespacePrefix(prefix); err != nil {
		return nil, err
	}

	suffix, err := randomSuffix(namespaceSuffixBytes)
	if err != nil {
		return nil, fmt.Errorf("generate namespace suffix: %w", err)
	}

	name := prefix + suffix
	if len(name) > maxNamespaceNameLen {
		return nil, fmt.Errorf("namespace prefix %q is too long for generated names", prefix)
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: fixtureLabels(name, "namespace", nil),
		},
	}

	if createErr := kubeClient.Create(ctx, namespace); createErr != nil {
		return nil, fmt.Errorf("create namespace %q: %w", name, createErr)
	}

	return namespace, nil
}

func DeleteNamespacesWithPrefix(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	prefix string,
) ([]string, error) {
	if err := validateNamespacePrefix(prefix); err != nil {
		return nil, err
	}

	var namespaces corev1.NamespaceList
	if err := kubeClient.List(ctx, &namespaces); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}

	deleted := make([]string, 0)
	for i := range namespaces.Items {
		namespace := &namespaces.Items[i]
		if !strings.HasPrefix(namespace.Name, prefix) {
			continue
		}

		if err := kubeClient.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("delete namespace %q: %w", namespace.Name, err)
		}

		deleted = append(deleted, namespace.Name)
	}

	sort.Strings(deleted)

	return deleted, nil
}

func validateNamespacePrefix(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return errors.New("namespace prefix must not be empty")
	}

	if len(prefix) >= maxNamespaceNameLen {
		return fmt.Errorf("namespace prefix %q is too long", prefix)
	}

	return nil
}

func randomSuffix(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return hex.EncodeToString(bytes), nil
}
