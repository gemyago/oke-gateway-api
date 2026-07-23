package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/samber/lo"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	configtypes "github.com/gemyago/oke-gateway-api/internal/types"
)

func withRelevantGatewayClass(gw *gatewayv1.Gateway) {
	if gw.Annotations == nil {
		gw.Annotations = make(map[string]string)
	}
	gw.Annotations[ControllerClassName] = "true"
}

func TestWatchesModel(t *testing.T) {
	makeMockDeps := func(t *testing.T) WatchesModelDeps {
		return WatchesModelDeps{
			K8sClient: NewMockk8sClient(t),
			Logger:    diag.RootTestLogger(),
		}
	}

	t.Run("RegisterFieldIndexers", func(t *testing.T) {
		t.Run("registers indexer for HTTPRoute backend service references", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TCPRoute{},
				tcpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.UDPRoute{},
				udpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.Gateway{},
				gatewayCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.NoError(t, err)
		})

		t.Run("skips L4 route indexers when disabled", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.Gateway{},
				gatewayCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer, RegisterFieldIndexersOptions{})
			require.NoError(t, err)
		})

		t.Run("registers TLSRoute indexer when enabled", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TLSRoute{},
				tlsRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TLSRoute{},
				tlsRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.Gateway{},
				gatewayCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer, RegisterFieldIndexersOptions{
				EnableTLSRoute: true,
			})
			require.NoError(t, err)
		})

		t.Run("registers ListenerSet indexers when enabled", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.Gateway{},
				gatewayCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.ListenerSet{},
				listenerSetParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.ListenerSet{},
				listenerSetCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer, RegisterFieldIndexersOptions{
				EnableListenerSet: true,
			})
			require.NoError(t, err)
		})

		t.Run("registered ListenerSet-enabled index callbacks handle their resource types", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			expectCallback := func(obj client.Object, field string, sample client.Object) {
				mockIndexer.EXPECT().
					IndexField(t.Context(), obj, field, mock.AnythingOfType("client.IndexerFunc")).
					Run(func(_ context.Context, _ client.Object, _ string, extractValue client.IndexerFunc) {
						_ = extractValue(sample)
					}).
					Return(nil)
			}

			expectCallback(&gatewayv1.HTTPRoute{}, httpRouteBackendServiceIndexKey, &gatewayv1.HTTPRoute{})
			expectCallback(&gatewayv1.GRPCRoute{}, grpcRouteBackendServiceIndexKey, &gatewayv1.GRPCRoute{})
			expectCallback(&gatewayv1.HTTPRoute{}, httpRouteParentGatewayIndexKey, &gatewayv1.HTTPRoute{})
			expectCallback(&gatewayv1.GRPCRoute{}, grpcRouteParentGatewayIndexKey, &gatewayv1.GRPCRoute{})
			expectCallback(&gatewayv1.TCPRoute{}, tcpRouteBackendServiceIndexKey, &gatewayv1.TCPRoute{})
			expectCallback(&gatewayv1.UDPRoute{}, udpRouteBackendServiceIndexKey, &gatewayv1.UDPRoute{})
			expectCallback(&gatewayv1.TLSRoute{}, tlsRouteBackendServiceIndexKey, &gatewayv1.TLSRoute{})
			expectCallback(&gatewayv1.TLSRoute{}, tlsRouteParentGatewayIndexKey, &gatewayv1.TLSRoute{})
			expectCallback(&gatewayv1.Gateway{}, gatewayCertificateIndexKey, &gatewayv1.Gateway{})
			expectCallback(&gatewayv1.ListenerSet{}, listenerSetParentGatewayIndexKey, &gatewayv1.ListenerSet{})
			expectCallback(&gatewayv1.ListenerSet{}, listenerSetCertificateIndexKey, &gatewayv1.ListenerSet{})

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer, RegisterFieldIndexersOptions{
				EnableTCPRoute:    true,
				EnableUDPRoute:    true,
				EnableTLSRoute:    true,
				EnableListenerSet: true,
			})

			require.NoError(t, err)
		})

		t.Run("returns error if ListenerSet parent Gateway indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.Gateway{},
				gatewayCertificateIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.ListenerSet{},
				listenerSetParentGatewayIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer, RegisterFieldIndexersOptions{
				EnableListenerSet: true,
			})

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to index ListenerSet by parent Gateway")
		})

		t.Run("returns error if ListenerSet certificate indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.Gateway{},
				gatewayCertificateIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.ListenerSet{},
				listenerSetParentGatewayIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(nil)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(t.Context(), &gatewayv1.ListenerSet{},
				listenerSetCertificateIndexKey, mock.AnythingOfType("client.IndexerFunc")).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer, RegisterFieldIndexersOptions{
				EnableListenerSet: true,
			})

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to index ListenerSet by certificate")
		})

		t.Run("returns error if HTTPRoute indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if GRPCRoute indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.ErrorContains(t, err, "failed to index GRPCRoute by backend service")
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if HTTPRoute parent Gateway indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)

			require.ErrorContains(t, err, "failed to index HTTPRoute by parent Gateway")
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if GRPCRoute parent Gateway indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)

			require.ErrorContains(t, err, "failed to index GRPCRoute by parent Gateway")
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if Gateway certificate indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TCPRoute{},
				tcpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.UDPRoute{},
				udpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.Gateway{},
				gatewayCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if L4 route indexer registration fails", func(t *testing.T) {
			for name, tc := range map[string]struct {
				failTCP bool
				err     string
			}{
				"tcp": {failTCP: true, err: "failed to index TCPRoute by backend service"},
				"udp": {err: "failed to index UDPRoute by backend service"},
			} {
				t.Run(name, func(t *testing.T) {
					deps := makeMockDeps(t)
					model := NewWatchesModel(deps)
					mockIndexer := k8sapi.NewMockFieldIndexer(t)
					wantErr := errors.New(faker.New().Lorem().Sentence(10))

					mockIndexer.EXPECT().IndexField(
						t.Context(),
						&gatewayv1.HTTPRoute{},
						httpRouteBackendServiceIndexKey,
						mock.AnythingOfType("client.IndexerFunc"),
					).Return(nil)
					mockIndexer.EXPECT().IndexField(
						t.Context(),
						&gatewayv1.GRPCRoute{},
						grpcRouteBackendServiceIndexKey,
						mock.AnythingOfType("client.IndexerFunc"),
					).Return(nil)
					mockIndexer.EXPECT().IndexField(
						t.Context(),
						&gatewayv1.HTTPRoute{},
						httpRouteParentGatewayIndexKey,
						mock.AnythingOfType("client.IndexerFunc"),
					).Return(nil)
					mockIndexer.EXPECT().IndexField(
						t.Context(),
						&gatewayv1.GRPCRoute{},
						grpcRouteParentGatewayIndexKey,
						mock.AnythingOfType("client.IndexerFunc"),
					).Return(nil)
					if tc.failTCP {
						mockIndexer.EXPECT().IndexField(
							t.Context(),
							&gatewayv1.TCPRoute{},
							tcpRouteBackendServiceIndexKey,
							mock.AnythingOfType("client.IndexerFunc"),
						).Return(wantErr)
					} else {
						mockIndexer.EXPECT().IndexField(
							t.Context(),
							&gatewayv1.TCPRoute{},
							tcpRouteBackendServiceIndexKey,
							mock.AnythingOfType("client.IndexerFunc"),
						).Return(nil)
						mockIndexer.EXPECT().IndexField(
							t.Context(),
							&gatewayv1.UDPRoute{},
							udpRouteBackendServiceIndexKey,
							mock.AnythingOfType("client.IndexerFunc"),
						).Return(wantErr)
					}

					err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
					require.ErrorContains(t, err, tc.err)
					require.ErrorIs(t, err, wantErr)
				})
			}
		})

		t.Run("returns error if TLSRoute indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TLSRoute{},
				tlsRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer, RegisterFieldIndexersOptions{
				EnableTLSRoute: true,
			})
			require.ErrorContains(t, err, "failed to index TLSRoute by backend service")
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if TLSRoute parent Gateway indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TLSRoute{},
				tlsRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TLSRoute{},
				tlsRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.registerTLSRouteIndexers(t.Context(), mockIndexer)

			require.ErrorContains(t, err, "failed to index TLSRoute by parent Gateway")
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("registers TLSRoute indexers directly", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TLSRoute{},
				tlsRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.TLSRoute{},
				tlsRouteParentGatewayIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			require.NoError(t, model.registerTLSRouteIndexers(t.Context(), mockIndexer))
		})
	})

	t.Run("indexHTTPRouteByBackendService", func(t *testing.T) {
		withRelevantRouteParentStatus := func(h *gatewayv1.HTTPRoute) {
			h.Status.Parents = append(h.Status.Parents,
				makeRandomRouteParentStatus(),
				makeRandomRouteParentStatus(
					randomRouteParentStatusWithConditionOpt(
						string(gatewayv1.RouteConditionResolvedRefs),
						metav1.ConditionTrue,
					),
					randomRouteParentStatusWithControllerNameOpt(ControllerClassName),
				),
			)
		}

		t.Run("build index of all backend refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs1 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			refs2 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			refs3 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				withRelevantRouteParentStatus,
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs1...),
					),
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs2...),
					),
				),
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs3...),
					),
				),
			)

			allRefs := make([]gatewayv1.HTTPBackendRef, 0, len(refs1)+len(refs2)+len(refs3))
			allRefs = append(allRefs, refs1...)
			allRefs = append(allRefs, refs2...)
			allRefs = append(allRefs, refs3...)
			wantIndices := lo.Map(allRefs, func(ref gatewayv1.HTTPBackendRef, _ int) string {
				return fmt.Sprintf("%v/%v",
					*ref.BackendObjectReference.Namespace,
					ref.BackendObjectReference.Name,
				)
			})

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)

			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("uses namespace from route as fallback", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs1 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(
					randomBackendRefWithNillNamespaceOpt(),
				),
				makeRandomBackendRef(
					randomBackendRefWithNillNamespaceOpt(),
				),
			}

			route := makeRandomHTTPRoute(
				withRelevantRouteParentStatus,
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs1...),
					),
				),
			)

			wantIndices := lo.Map(refs1, func(ref gatewayv1.HTTPBackendRef, _ int) string {
				return fmt.Sprintf("%v/%v",
					route.Namespace,
					ref.BackendObjectReference.Name,
				)
			})

			result := model.indexHTTPRouteByBackendService(t.Context(), &route)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("deduplicate backend refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				withRelevantRouteParentStatus,
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...),
					),
				),
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...),
					),
				),
			)

			wantIndices := lo.Map(refs, func(ref gatewayv1.HTTPBackendRef, _ int) string {
				return fmt.Sprintf("%v/%v",
					*ref.BackendObjectReference.Namespace,
					ref.BackendObjectReference.Name,
				)
			})

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("ignore non route objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.indexHTTPRouteByBackendService(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})

		t.Run("ignores deleted routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				withRelevantRouteParentStatus,
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...),
					),
				),
			)

			// Mark the route for deletion
			deletionTimestamp := metav1.Now()
			httpRoute.DeletionTimestamp = &deletionTimestamp

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)
			require.Nil(t, result)
		})

		t.Run("ignores routes without relevant parent status", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...)),
				),
			)

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)
			require.Nil(t, result)
		})

		t.Run("ignores routes with relevant but not accepted parent status", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				func(h *gatewayv1.HTTPRoute) {
					h.Status.Parents = append(h.Status.Parents,
						makeRandomRouteParentStatus(),
						makeRandomRouteParentStatus(
							randomRouteParentStatusWithConditionOpt(
								string(gatewayv1.RouteConditionResolvedRefs),
								metav1.ConditionFalse,
							),
							randomRouteParentStatusWithControllerNameOpt(ControllerClassName),
						),
					)
				},
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...)),
				),
			)

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)
			require.Nil(t, result)
		})
	})

	t.Run("indexGRPCRouteByBackendService", func(t *testing.T) {
		withRelevantGRPCRouteParentStatus := func(route *gatewayv1.GRPCRoute) {
			route.Status.Parents = append(route.Status.Parents,
				makeRandomRouteParentStatus(),
				makeRandomRouteParentStatus(
					randomRouteParentStatusWithConditionOpt(
						string(gatewayv1.RouteConditionResolvedRefs),
						metav1.ConditionTrue,
					),
					randomRouteParentStatusWithControllerNameOpt(ControllerClassName),
				),
			)
		}

		t.Run("build index of all backend refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.GRPCBackendRef{
				makeRandomGRPCBackendRef(),
				makeRandomGRPCBackendRef(randomGRPCBackendRefWithNilNamespaceOpt()),
			}

			grpcRoute := makeRandomGRPCRoute(
				withRelevantGRPCRouteParentStatus,
				randomGRPCRouteWithRulesOpt(
					makeRandomGRPCRouteRule(randomGRPCRouteRuleWithRandomBackendRefsOpt(refs...)),
				),
			)

			wantIndices := lo.Map(refs, func(ref gatewayv1.GRPCBackendRef, _ int) string {
				namespace := grpcRoute.Namespace
				if ref.BackendObjectReference.Namespace != nil {
					namespace = string(*ref.BackendObjectReference.Namespace)
				}
				return fmt.Sprintf("%v/%v", namespace, ref.BackendObjectReference.Name)
			})

			result := model.indexGRPCRouteByBackendService(t.Context(), &grpcRoute)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("ignores routes without relevant parent status", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			grpcRoute := makeRandomGRPCRoute(
				randomGRPCRouteWithRulesOpt(
					makeRandomGRPCRouteRule(randomGRPCRouteRuleWithRandomBackendRefsOpt(
						makeRandomGRPCBackendRef(),
					)),
				),
			)

			result := model.indexGRPCRouteByBackendService(t.Context(), &grpcRoute)
			require.Nil(t, result)
		})

		t.Run("ignores deleting routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			grpcRoute := makeRandomGRPCRoute(withRelevantGRPCRouteParentStatus)
			deletionTimestamp := metav1.Now()
			grpcRoute.DeletionTimestamp = &deletionTimestamp

			result := model.indexGRPCRouteByBackendService(t.Context(), &grpcRoute)
			require.Nil(t, result)
		})

		t.Run("ignores non route objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.indexGRPCRouteByBackendService(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})
	})

	t.Run("index route by parent Gateway", func(t *testing.T) {
		t.Run("indexes HTTPRoute parent Gateway refs with default namespace and deduplication", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			routeNamespace := faker.New().Internet().Slug()
			gatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			otherNamespace := gatewayv1.Namespace(faker.New().Internet().Slug())
			otherGatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			listenerSetKind := gatewayv1.Kind("ListenerSet")
			listenerSetName := gatewayv1.ObjectName("listeners-" + faker.New().Internet().Slug())
			unsupportedKind := gatewayv1.Kind("Service")
			unsupportedGroup := gatewayv1.Group("other.example.com")
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithNamespaceOpt(routeNamespace),
				randomHTTPRouteWithRandomParentRefsOpt(
					gatewayv1.ParentReference{Name: gatewayName},
					gatewayv1.ParentReference{Name: gatewayName},
					gatewayv1.ParentReference{Namespace: &otherNamespace, Name: otherGatewayName},
					gatewayv1.ParentReference{Name: listenerSetName, Kind: &listenerSetKind},
					gatewayv1.ParentReference{Name: gatewayName, Kind: &unsupportedKind},
					gatewayv1.ParentReference{Name: gatewayName, Group: &unsupportedGroup},
				),
			)

			result := model.indexHTTPRouteByParentGateway(t.Context(), &route)

			require.ElementsMatch(t, []string{
				fmt.Sprintf("%s/%s", routeNamespace, gatewayName),
				fmt.Sprintf("%s/%s", otherNamespace, otherGatewayName),
				fmt.Sprintf("%s/%s", routeNamespace, listenerSetName),
			}, result)
		})

		t.Run("indexes GRPCRoute parent Gateway refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			routeNamespace := faker.New().Internet().Slug()
			gatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			route := makeRandomGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Namespace = routeNamespace
				route.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: gatewayName}}
			})

			result := model.indexGRPCRouteByParentGateway(t.Context(), &route)

			require.ElementsMatch(t, []string{fmt.Sprintf("%s/%s", routeNamespace, gatewayName)}, result)
		})

		t.Run("indexes TLSRoute parent Gateway refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			routeNamespace := faker.New().Internet().Slug()
			gatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			route := gatewayv1.TLSRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: routeNamespace},
				Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: gatewayName}},
				}},
			}

			result := model.indexTLSRouteByParentGateway(t.Context(), &route)

			require.ElementsMatch(t, []string{fmt.Sprintf("%s/%s", routeNamespace, gatewayName)}, result)
		})

		t.Run("ignores deleted and non route objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			deletionTimestamp := metav1.Now()
			httpRoute := makeRandomHTTPRoute()
			httpRoute.DeletionTimestamp = &deletionTimestamp
			grpcRoute := makeRandomGRPCRoute()
			grpcRoute.DeletionTimestamp = &deletionTimestamp
			tlsRoute := &gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &deletionTimestamp}}

			require.Nil(t, model.indexHTTPRouteByParentGateway(t.Context(), &httpRoute))
			require.Nil(t, model.indexHTTPRouteByParentGateway(t.Context(), &corev1.Service{}))
			require.Nil(t, model.indexGRPCRouteByParentGateway(t.Context(), &grpcRoute))
			require.Nil(t, model.indexGRPCRouteByParentGateway(t.Context(), &corev1.Service{}))
			require.Nil(t, model.indexTLSRouteByParentGateway(t.Context(), tlsRoute))
			require.Nil(t, model.indexTLSRouteByParentGateway(t.Context(), &corev1.Service{}))
		})
	})

	t.Run("MapEndpointSliceToHTTPRoute", func(t *testing.T) {
		t.Run("finds matching HTTPRoutes based on service index", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			wantRoutes := []gatewayv1.HTTPRoute{
				makeRandomHTTPRoute(),
				makeRandomHTTPRoute(),
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(wantRoutes))
				return nil
			})

			wantRequests := lo.Map(wantRoutes, func(route gatewayv1.HTTPRoute, _ int) reconcile.Request {
				return reconcile.Request{
					NamespacedName: apitypes.NamespacedName{
						Name:      route.Name,
						Namespace: route.Namespace,
					},
				}
			})

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &endpointSlice)
			require.ElementsMatch(t, wantRequests, result)
		})

		t.Run("ignores HTTPRoutes marked for deletion", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			// One route not marked for deletion, one route marked for deletion
			routeToDelete := makeRandomHTTPRoute()
			deletionTimestamp := metav1.Now()
			routeToDelete.DeletionTimestamp = &deletionTimestamp

			validRoute := makeRandomHTTPRoute()

			allRoutes := []gatewayv1.HTTPRoute{
				validRoute,
				routeToDelete,
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(allRoutes))
				return nil
			})

			// Only the validRoute should be reconciled
			wantRequests := []reconcile.Request{
				{
					NamespacedName: apitypes.NamespacedName{
						Name:      validRoute.Name,
						Namespace: validRoute.Namespace,
					},
				},
			}

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &endpointSlice)
			require.ElementsMatch(t, wantRequests, result)
		})

		t.Run("returns nil if k8s client returns error", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
			).Return(wantErr)

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &endpointSlice)
			require.Nil(t, result)
		})

		t.Run("returns nil when no routes found", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				// Ensure Items field is explicitly set to an empty slice
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.HTTPRoute{}))
				return nil
			})

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &endpointSlice)
			require.Nil(t, result)
		})

		t.Run("returns nil if object is not an EndpointSlice", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})

		t.Run("ignore EndpointSlices without service name label", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &discoveryv1.EndpointSlice{})
			require.Nil(t, result)
		})
	})

	t.Run("MapEndpointSliceToGRPCRoute", func(t *testing.T) {
		t.Run("finds matching GRPCRoutes and ignores deleted routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			validRoute := makeRandomGRPCRoute()
			routeToDelete := makeRandomGRPCRoute()
			deletionTimestamp := metav1.Now()
			routeToDelete.DeletionTimestamp = &deletionTimestamp

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GRPCRouteList{},
				client.MatchingFields{grpcRouteBackendServiceIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.GRPCRoute{
					validRoute,
					routeToDelete,
				}))
				return nil
			})

			result := model.MapEndpointSliceToGRPCRoute(t.Context(), &endpointSlice)
			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: apitypes.NamespacedName{
					Name:      validRoute.Name,
					Namespace: validRoute.Namespace,
				},
			}}, result)
		})

		t.Run("returns nil if k8s client returns error", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GRPCRouteList{},
				client.MatchingFields{grpcRouteBackendServiceIndexKey: indexKey},
			).Return(wantErr)

			result := model.MapEndpointSliceToGRPCRoute(t.Context(), &endpointSlice)
			require.Nil(t, result)
		})
	})

	t.Run("MapHTTPRouteToGRPCRoute", func(t *testing.T) {
		t.Run("queues GRPCRoutes with matching parent Gateway refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			deletedRoute := makeRandomGRPCRoute()
			deletionTimestamp := metav1.Now()
			deletedRoute.DeletionTimestamp = &deletionTimestamp
			gatewayNamespace := gatewayv1.Namespace(faker.New().Internet().Slug())
			gatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			otherGatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			httpRoute := makeRandomHTTPRoute(randomHTTPRouteWithRandomParentRefsOpt(
				gatewayv1.ParentReference{Namespace: &gatewayNamespace, Name: gatewayName},
				gatewayv1.ParentReference{Namespace: &gatewayNamespace, Name: otherGatewayName},
			))
			duplicateRoute := makeRandomGRPCRoute()
			wantRoutes := []gatewayv1.GRPCRoute{duplicateRoute, makeRandomGRPCRoute()}
			gatewayIndexKey := fmt.Sprintf("%s/%s", gatewayNamespace, gatewayName)
			otherGatewayIndexKey := fmt.Sprintf("%s/%s", gatewayNamespace, otherGatewayName)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GRPCRouteList{},
				client.MatchingFields{grpcRouteParentGatewayIndexKey: gatewayIndexKey},
			).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(
						reflect.ValueOf([]gatewayv1.GRPCRoute{duplicateRoute, deletedRoute}),
					)
					return nil
				})
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GRPCRouteList{},
				client.MatchingFields{grpcRouteParentGatewayIndexKey: otherGatewayIndexKey},
			).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(wantRoutes))
					return nil
				})

			result := model.MapHTTPRouteToGRPCRoute(t.Context(), &httpRoute)

			require.ElementsMatch(t, lo.Map(wantRoutes, func(route gatewayv1.GRPCRoute, _ int) reconcile.Request {
				return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)}
			}), result)
		})

		t.Run("returns nil for non HTTPRoute objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapHTTPRouteToGRPCRoute(t.Context(), &corev1.Service{})

			require.Nil(t, result)
		})

		t.Run("returns nil when GRPCRoute list fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			httpRoute := makeRandomHTTPRoute(randomHTTPRouteWithRandomParentRefsOpt(
				gatewayv1.ParentReference{Name: gatewayName},
			))
			gatewayIndexKey := fmt.Sprintf("%s/%s", httpRoute.Namespace, gatewayName)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GRPCRouteList{},
				client.MatchingFields{grpcRouteParentGatewayIndexKey: gatewayIndexKey},
			).
				Return(errors.New(faker.New().Lorem().Sentence(10)))

			result := model.MapHTTPRouteToGRPCRoute(t.Context(), &httpRoute)

			require.Nil(t, result)
		})
	})

	t.Run("MapGRPCRouteToHTTPRoute", func(t *testing.T) {
		t.Run("queues HTTPRoutes with matching parent Gateway refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			deletedRoute := makeRandomHTTPRoute()
			deletionTimestamp := metav1.Now()
			deletedRoute.DeletionTimestamp = &deletionTimestamp
			gatewayNamespace := gatewayv1.Namespace(faker.New().Internet().Slug())
			gatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			otherGatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			grpcRoute := makeRandomGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.ParentRefs = []gatewayv1.ParentReference{
					{Namespace: &gatewayNamespace, Name: gatewayName},
					{Namespace: &gatewayNamespace, Name: otherGatewayName},
				}
			})
			duplicateRoute := makeRandomHTTPRoute()
			wantRoutes := []gatewayv1.HTTPRoute{duplicateRoute, makeRandomHTTPRoute()}
			gatewayIndexKey := fmt.Sprintf("%s/%s", gatewayNamespace, gatewayName)
			otherGatewayIndexKey := fmt.Sprintf("%s/%s", gatewayNamespace, otherGatewayName)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteParentGatewayIndexKey: gatewayIndexKey},
			).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(
						reflect.ValueOf([]gatewayv1.HTTPRoute{duplicateRoute, deletedRoute}),
					)
					return nil
				})
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteParentGatewayIndexKey: otherGatewayIndexKey},
			).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(wantRoutes))
					return nil
				})

			result := model.MapGRPCRouteToHTTPRoute(t.Context(), &grpcRoute)

			require.ElementsMatch(t, lo.Map(wantRoutes, func(route gatewayv1.HTTPRoute, _ int) reconcile.Request {
				return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)}
			}), result)
		})

		t.Run("returns nil for non GRPCRoute objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapGRPCRouteToHTTPRoute(t.Context(), &corev1.Service{})

			require.Nil(t, result)
		})

		t.Run("returns nil when HTTPRoute list fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gatewayName := gatewayv1.ObjectName(faker.New().Internet().Domain())
			grpcRoute := makeRandomGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: gatewayName}}
			})
			gatewayIndexKey := fmt.Sprintf("%s/%s", grpcRoute.Namespace, gatewayName)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteParentGatewayIndexKey: gatewayIndexKey},
			).
				Return(errors.New(faker.New().Lorem().Sentence(10)))

			result := model.MapGRPCRouteToHTTPRoute(t.Context(), &grpcRoute)

			require.Nil(t, result)
		})
	})

	t.Run("indexGatewayByCertificate", func(t *testing.T) {
		t.Run("indexes all referenced Secret namespaced names from HTTPS listeners", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			// Create HTTPS listeners with random secrets
			listener1 := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			listener2 := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway(
				withRelevantGatewayClass,
				randomGatewayWithListenersOpt(listener1, listener2),
			)

			// Collect all referenced secrets
			var wantIndices []string
			for _, l := range gateway.Spec.Listeners {
				if l.TLS != nil {
					for _, ref := range l.TLS.CertificateRefs {
						ns := gateway.Namespace
						if ref.Namespace != nil {
							ns = string(*ref.Namespace)
						}
						wantIndices = append(wantIndices, ns+"/"+string(ref.Name))
					}
				}
			}

			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("deduplicates secrets", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			listener1 := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			listener2 := makeRandomListener(func(l *gatewayv1.Listener) {
				l.TLS = &gatewayv1.ListenerTLSConfig{
					CertificateRefs: listener1.TLS.CertificateRefs,
				}
			})
			gateway := newRandomGateway(
				withRelevantGatewayClass,
				randomGatewayWithListenersOpt(listener1, listener2),
			)

			wantIndices := lo.Map(
				listener1.TLS.CertificateRefs,
				func(ref gatewayv1.SecretObjectReference, _ int) string {
					ns := gateway.Namespace
					if ref.Namespace != nil {
						ns = string(*ref.Namespace)
					}
					return ns + "/" + string(ref.Name)
				},
			)

			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("ignores non-Gateway objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			result := model.indexGatewayByCertificateSecrets(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})

		t.Run("ignores Gateways marked for deletion", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := newRandomGateway(withRelevantGatewayClass)
			deletionTimestamp := metav1.Now()
			gateway.DeletionTimestamp = &deletionTimestamp
			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.Nil(t, result)
		})

		t.Run("returns empty slice if no HTTPS listeners or no certificate refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := newRandomGateway(withRelevantGatewayClass) // Only HTTP listeners by default
			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.Empty(t, result)
		})

		t.Run("ignores Gateways without correct controller class", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := newRandomGateway() // No controller class set
			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.Nil(t, result)
		})
	})

	t.Run("indexListenerSet", func(t *testing.T) {
		t.Run("indexes parent Gateway", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			parentNamespace := gatewayv1.Namespace("infra-" + faker.New().Lorem().Word())
			parentName := gatewayv1.ObjectName("edge-" + faker.New().Lorem().Word())
			listenerSet := &gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps-" + faker.New().Lorem().Word(),
					Name:      "listeners-" + faker.New().Lorem().Word(),
				},
				Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{
					Namespace: &parentNamespace,
					Name:      parentName,
				}},
			}

			result := model.indexListenerSetByParentGateway(t.Context(), listenerSet)

			require.ElementsMatch(t, []string{fmt.Sprintf("%s/%s", parentNamespace, parentName)}, result)
		})

		t.Run("indexes certificate refs from HTTPS and TLS listeners", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			certNamespace := gatewayv1.Namespace("certs-" + faker.New().Lorem().Word())
			listenerSet := &gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps-" + faker.New().Lorem().Word(),
					Name:      "listeners-" + faker.New().Lorem().Word(),
				},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
					Listeners: []gatewayv1.ListenerEntry{
						{
							Name:     "https",
							Protocol: gatewayv1.HTTPSProtocolType,
							TLS: &gatewayv1.ListenerTLSConfig{CertificateRefs: []gatewayv1.SecretObjectReference{
								{Name: gatewayv1.ObjectName("same-ns-" + faker.New().Lorem().Word())},
							}},
						},
						{
							Name:     "tls",
							Protocol: gatewayv1.TLSProtocolType,
							TLS: &gatewayv1.ListenerTLSConfig{CertificateRefs: []gatewayv1.SecretObjectReference{
								{
									Name:      gatewayv1.ObjectName("shared-" + faker.New().Lorem().Word()),
									Namespace: &certNamespace,
								},
							}},
						},
						{
							Name:     "tcp",
							Protocol: gatewayv1.TCPProtocolType,
							TLS: &gatewayv1.ListenerTLSConfig{CertificateRefs: []gatewayv1.SecretObjectReference{
								{Name: gatewayv1.ObjectName("ignored-" + faker.New().Lorem().Word())},
							}},
						},
					},
				},
			}

			result := model.indexListenerSetByCertificateSecrets(t.Context(), listenerSet)

			require.ElementsMatch(t, []string{
				fmt.Sprintf("%s/%s", listenerSet.Namespace, listenerSet.Spec.Listeners[0].TLS.CertificateRefs[0].Name),
				fmt.Sprintf("%s/%s", certNamespace, listenerSet.Spec.Listeners[1].TLS.CertificateRefs[0].Name),
			}, result)
		})

		t.Run("ignores invalid or deleting ListenerSets", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			deletionTimestamp := metav1.Now()
			deletingListenerSet := &gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         "apps",
					Name:              "deleting",
					DeletionTimestamp: &deletionTimestamp,
					Finalizers:        []string{"test-finalizer"},
				},
				Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"}},
			}
			invalidKind := gatewayv1.Kind("Service")
			invalidParentRefListenerSet := &gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "invalid"},
				Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{
					Kind: &invalidKind,
					Name: "edge",
				}},
			}

			require.Nil(t, model.indexListenerSetByParentGateway(t.Context(), &corev1.Service{}))
			require.Nil(t, model.indexListenerSetByParentGateway(t.Context(), deletingListenerSet))
			require.Nil(t, model.indexListenerSetByParentGateway(t.Context(), invalidParentRefListenerSet))
			require.Nil(t, model.indexListenerSetByCertificateSecrets(t.Context(), &corev1.Service{}))
			require.Nil(t, model.indexListenerSetByCertificateSecrets(t.Context(), deletingListenerSet))
		})
	})

	t.Run("MapSecretToGateway", func(t *testing.T) {
		t.Run("finds matching Gateways based on certificate index", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)

			wantGateways := []gatewayv1.Gateway{
				*newRandomGateway(withRelevantGatewayClass),
				*newRandomGateway(withRelevantGatewayClass),
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(wantGateways))
				return nil
			})

			wantRequests := lo.Map(wantGateways, func(gateway gatewayv1.Gateway, _ int) reconcile.Request {
				return reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&gateway),
				}
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.ElementsMatch(t, wantRequests, result)
		})

		t.Run("ignores non-TLS secrets", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(func(s *corev1.Secret) {
				s.Type = corev1.SecretTypeOpaque
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("ignores TLS secrets without certificate data", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(func(s *corev1.Secret) {
				s.Type = corev1.SecretTypeTLS
				s.Data = map[string][]byte{
					corev1.TLSPrivateKeyKey: []byte("private-key"),
				}
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("ignores TLS secrets without private key data", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(func(s *corev1.Secret) {
				s.Type = corev1.SecretTypeTLS
				s.Data = map[string][]byte{
					corev1.TLSCertKey: []byte("certificate"),
				}
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("ignores Gateways marked for deletion", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)

			// One gateway not marked for deletion, one gateway marked for deletion
			gatewayToDelete := *newRandomGateway(withRelevantGatewayClass)
			deletionTimestamp := metav1.Now()
			gatewayToDelete.DeletionTimestamp = &deletionTimestamp

			validGateway := *newRandomGateway(withRelevantGatewayClass)

			allGateways := []gatewayv1.Gateway{
				validGateway,
				gatewayToDelete,
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(allGateways))
				return nil
			})

			// Only the validGateway should be reconciled
			wantRequests := []reconcile.Request{
				{
					NamespacedName: client.ObjectKeyFromObject(&validGateway),
				},
			}

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.ElementsMatch(t, wantRequests, result)
		})

		t.Run("returns nil if k8s client returns error", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).Return(wantErr)

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("returns nil when no gateways found", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				// Ensure Items field is explicitly set to an empty slice
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.Gateway{}))
				return nil
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("returns nil if object is not a Secret", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapSecretToGateway(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})
	})

	t.Run("MapSecretToGatewayWithListenerSets", func(t *testing.T) {
		t.Run("queues parent Gateways for ListenerSets referencing the Secret", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)
			parentNamespace := gatewayv1.Namespace("infra-" + faker.New().Lorem().Word())
			parentName := gatewayv1.ObjectName("edge-" + faker.New().Lorem().Word())
			listenerSets := []gatewayv1.ListenerSet{{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps-" + faker.New().Lorem().Word(),
					Name:      "listeners-" + faker.New().Lorem().Word(),
				},
				Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{
					Namespace: &parentNamespace,
					Name:      parentName,
				}},
			}}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.Gateway{}))
				return nil
			})
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.ListenerSetList{},
				client.MatchingFields{listenerSetCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(listenerSets))
				return nil
			})

			result := model.MapSecretToGatewayWithListenerSets(t.Context(), &secret)

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: apitypes.NamespacedName{
					Namespace: string(parentNamespace),
					Name:      string(parentName),
				},
			}}, result)
		})

		t.Run("returns direct requests for invalid Secret inputs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			opaqueSecret := makeRandomSecret(func(secret *corev1.Secret) {
				secret.Type = corev1.SecretTypeOpaque
			})
			certOnlySecret := makeRandomSecret(func(secret *corev1.Secret) {
				secret.Type = corev1.SecretTypeTLS
				secret.Data = map[string][]byte{corev1.TLSCertKey: []byte("certificate")}
			})
			keyOnlySecret := makeRandomSecret(func(secret *corev1.Secret) {
				secret.Type = corev1.SecretTypeTLS
				secret.Data = map[string][]byte{corev1.TLSPrivateKeyKey: []byte("key")}
			})

			require.Nil(t, model.MapSecretToGatewayWithListenerSets(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapSecretToGatewayWithListenerSets(t.Context(), &opaqueSecret))
			require.Nil(t, model.MapSecretToGatewayWithListenerSets(t.Context(), &certOnlySecret))
			require.Nil(t, model.MapSecretToGatewayWithListenerSets(t.Context(), &keyOnlySecret))
		})

		t.Run("keeps direct Gateway requests when ListenerSet lookup fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)
			gateway := *newRandomGateway()
			wantErr := errors.New("listenerset list failed")

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.Gateway{gateway}))
				return nil
			})
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.ListenerSetList{},
				client.MatchingFields{listenerSetCertificateIndexKey: indexKey},
			).Return(wantErr)

			result := model.MapSecretToGatewayWithListenerSets(t.Context(), &secret)

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: client.ObjectKeyFromObject(&gateway),
			}}, result)
		})
	})

	t.Run("MapSecretToListenerSet", func(t *testing.T) {
		t.Run("queues ListenerSets referencing the Secret", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
			}
			deletedAt := metav1.Now()
			deletingListenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         "apps",
					Name:              "deleting",
					DeletionTimestamp: &deletedAt,
				},
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.ListenerSetList{},
				client.MatchingFields{listenerSetCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.ListenerSet{
					listenerSet,
					deletingListenerSet,
				}))
				return nil
			})

			result := model.MapSecretToListenerSet(t.Context(), &secret)

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: client.ObjectKeyFromObject(&listenerSet),
			}}, result)
		})

		t.Run("ignores invalid Secret inputs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			opaqueSecret := makeRandomSecret(func(secret *corev1.Secret) {
				secret.Type = corev1.SecretTypeOpaque
			})
			certOnlySecret := makeRandomSecret(func(secret *corev1.Secret) {
				secret.Type = corev1.SecretTypeTLS
				secret.Data = map[string][]byte{corev1.TLSCertKey: []byte("certificate")}
			})
			keyOnlySecret := makeRandomSecret(func(secret *corev1.Secret) {
				secret.Type = corev1.SecretTypeTLS
				secret.Data = map[string][]byte{corev1.TLSPrivateKeyKey: []byte("key")}
			})

			require.Nil(t, model.MapSecretToListenerSet(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapSecretToListenerSet(t.Context(), &opaqueSecret))
			require.Nil(t, model.MapSecretToListenerSet(t.Context(), &certOnlySecret))
			require.Nil(t, model.MapSecretToListenerSet(t.Context(), &keyOnlySecret))
		})

		t.Run("handles list errors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.ListenerSetList{},
				client.MatchingFields{listenerSetCertificateIndexKey: indexKey},
			).Return(errors.New("listenerset list failed"))

			require.Nil(t, model.MapSecretToListenerSet(t.Context(), &secret))
		})
	})

	t.Run("MapListenerSetToGateway", func(t *testing.T) {
		t.Run("queues parent Gateway", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			parentNamespace := gatewayv1.Namespace("infra-" + faker.New().Lorem().Word())
			parentName := gatewayv1.ObjectName("edge-" + faker.New().Lorem().Word())
			listenerSet := &gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{
					Namespace: &parentNamespace,
					Name:      parentName,
				}},
			}

			result := model.MapListenerSetToGateway(t.Context(), listenerSet)

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: apitypes.NamespacedName{Namespace: string(parentNamespace), Name: string(parentName)},
			}}, result)
		})

		t.Run("queues current and previous parent Gateways", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			parentNamespace := gatewayv1.Namespace("infra-" + faker.New().Lorem().Word())
			parentName := gatewayv1.ObjectName("edge-" + faker.New().Lorem().Word())
			previousParent := "old-infra/old-edge"
			listenerSet := &gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps",
					Name:      "extra",
					Annotations: map[string]string{
						ListenerSetParentGatewayAnnotation: previousParent,
					},
				},
				Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{
					Namespace: &parentNamespace,
					Name:      parentName,
				}},
			}

			result := model.MapListenerSetToGateway(t.Context(), listenerSet)

			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: string(parentNamespace), Name: string(parentName)}},
				{NamespacedName: apitypes.NamespacedName{Namespace: "old-infra", Name: "old-edge"}},
			}, result)
		})

		t.Run("ignores invalid inputs and parent refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			invalidKind := gatewayv1.Kind("Service")
			listenerSet := &gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{
					Kind: &invalidKind,
					Name: "edge",
				}},
			}

			require.Nil(t, model.MapListenerSetToGateway(t.Context(), listenerSet))
			require.Nil(t, model.MapListenerSetToGateway(t.Context(), &corev1.Service{}))
		})
	})

	t.Run("MapGatewayToListenerSet", func(t *testing.T) {
		t.Run("queues attached ListenerSets", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}}
			listenerSet := gatewayv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"}}
			deletedAt := metav1.Now()
			deletingListenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         "apps",
					Name:              "deleting",
					DeletionTimestamp: &deletedAt,
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}, client.MatchingFields{
					listenerSetParentGatewayIndexKey: client.ObjectKeyFromObject(gateway).String(),
				}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.ListenerSet{
						listenerSet,
						deletingListenerSet,
					}))
					return nil
				})

			result := model.MapGatewayToListenerSet(t.Context(), gateway)

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: client.ObjectKeyFromObject(&listenerSet),
			}}, result)
			require.Nil(t, model.MapGatewayToListenerSet(t.Context(), &corev1.Service{}))
		})

		t.Run("handles list errors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}, mock.Anything).
				Return(errors.New("listenerset list failed"))

			require.Nil(t, model.MapGatewayToListenerSet(t.Context(), gateway))
		})
	})

	t.Run("MapNamespaceToListenerSet", func(t *testing.T) {
		t.Run("queues ListenerSets in changed namespace", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}}
			listenerSet := gatewayv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"}}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}, client.InNamespace("apps")).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.ListenerSet{
						listenerSet,
					}))
					return nil
				})

			result := model.MapNamespaceToListenerSet(t.Context(), namespace)

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: client.ObjectKeyFromObject(&listenerSet),
			}}, result)
			require.Nil(t, model.MapNamespaceToListenerSet(t.Context(), &gatewayv1.Gateway{}))
		})

		t.Run("handles list errors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}, client.InNamespace("apps")).
				Return(errors.New("listenerset list failed"))

			require.Nil(t, model.MapNamespaceToListenerSet(t.Context(), namespace))
		})
	})

	t.Run("MapReferenceGrantToGatewayWithListenerSets", func(t *testing.T) {
		t.Run("queues parent Gateways for matching ListenerSet certificateRefs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			certNamespace := gatewayv1.Namespace("certs")
			parentNamespace := gatewayv1.Namespace("infra")
			parentName := gatewayv1.ObjectName("edge")
			grant := &gatewayv1beta1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{Namespace: string(certNamespace), Name: "allow"},
				Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{
						Group:     gatewayv1.Group(gatewayAPIGroup),
						Kind:      gatewayv1.Kind("ListenerSet"),
						Namespace: gatewayv1.Namespace("apps"),
					}},
					To: []gatewayv1beta1.ReferenceGrantTo{{
						Group: gatewayv1.Group(""),
						Kind:  gatewayv1.Kind("Secret"),
						Name:  lo.ToPtr(gatewayv1.ObjectName("tls-cert")),
					}},
				},
			}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{
						Namespace: &parentNamespace,
						Name:      parentName,
					},
					Listeners: []gatewayv1.ListenerEntry{{
						Name:     "https",
						Protocol: gatewayv1.HTTPSProtocolType,
						Port:     443,
						TLS: &gatewayv1.ListenerTLSConfig{CertificateRefs: []gatewayv1.SecretObjectReference{{
							Namespace: &certNamespace,
							Name:      "tls-cert",
						}}},
					}},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.ListenerSet{
						listenerSet,
					}))
					return nil
				})

			result := model.MapReferenceGrantToGatewayWithListenerSets(t.Context(), grant)

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: apitypes.NamespacedName{Namespace: string(parentNamespace), Name: string(parentName)},
			}}, result)
		})

		t.Run("ignores non matching grants and handles list errors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			serviceGrant := &gatewayv1beta1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{Namespace: "certs", Name: "allow"},
				Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{
						Group:     gatewayv1.Group(gatewayAPIGroup),
						Kind:      gatewayv1.Kind("ListenerSet"),
						Namespace: gatewayv1.Namespace("apps"),
					}},
					To: []gatewayv1beta1.ReferenceGrantTo{{
						Group: gatewayv1.Group(""),
						Kind:  gatewayv1.Kind(serviceKind),
					}},
				},
			}
			require.Nil(t, model.MapReferenceGrantToGatewayWithListenerSets(t.Context(), serviceGrant))
			require.Nil(t, model.MapReferenceGrantToGatewayWithListenerSets(t.Context(), &corev1.Service{}))

			secretGrant := serviceGrant.DeepCopy()
			secretGrant.Spec.To[0].Kind = gatewayv1.Kind("Secret")
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}).
				Return(errors.New("listenerset list failed"))

			require.Nil(t, model.MapReferenceGrantToGatewayWithListenerSets(t.Context(), secretGrant))
		})
	})

	t.Run("MapReferenceGrantToListenerSet", func(t *testing.T) {
		t.Run("queues ListenerSets for matching certificateRefs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			certNamespace := gatewayv1.Namespace("certs")
			grant := &gatewayv1beta1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{Namespace: string(certNamespace), Name: "allow"},
				Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{
						Group:     gatewayv1.Group(gatewayAPIGroup),
						Kind:      gatewayv1.Kind("ListenerSet"),
						Namespace: gatewayv1.Namespace("apps"),
					}},
					To: []gatewayv1beta1.ReferenceGrantTo{{
						Group: gatewayv1.Group(""),
						Kind:  gatewayv1.Kind("Secret"),
						Name:  lo.ToPtr(gatewayv1.ObjectName("tls-cert")),
					}},
				},
			}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					Listeners: []gatewayv1.ListenerEntry{{
						Name:     "https",
						Protocol: gatewayv1.HTTPSProtocolType,
						Port:     443,
						TLS: &gatewayv1.ListenerTLSConfig{CertificateRefs: []gatewayv1.SecretObjectReference{{
							Namespace: &certNamespace,
							Name:      "tls-cert",
						}}},
					}},
				},
			}
			deletedAt := metav1.Now()
			deletingListenerSet := listenerSet
			deletingListenerSet.Name = "deleting"
			deletingListenerSet.DeletionTimestamp = &deletedAt
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.ListenerSet{
						listenerSet,
						deletingListenerSet,
					}))
					return nil
				})

			result := model.MapReferenceGrantToListenerSet(t.Context(), grant)

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: client.ObjectKeyFromObject(&listenerSet),
			}}, result)
		})

		t.Run("ignores non matching grants and handles list errors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			serviceGrant := &gatewayv1beta1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{Namespace: "certs", Name: "allow"},
				Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{
						Group:     gatewayv1.Group(gatewayAPIGroup),
						Kind:      gatewayv1.Kind("ListenerSet"),
						Namespace: gatewayv1.Namespace("apps"),
					}},
					To: []gatewayv1beta1.ReferenceGrantTo{{
						Group: gatewayv1.Group(""),
						Kind:  gatewayv1.Kind(serviceKind),
					}},
				},
			}
			require.Nil(t, model.MapReferenceGrantToListenerSet(t.Context(), serviceGrant))
			require.Nil(t, model.MapReferenceGrantToListenerSet(t.Context(), &corev1.Service{}))

			secretGrant := serviceGrant.DeepCopy()
			secretGrant.Spec.To[0].Kind = gatewayv1.Kind("Secret")
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}).
				Return(errors.New("listenerset list failed"))

			require.Nil(t, model.MapReferenceGrantToListenerSet(t.Context(), secretGrant))
		})
	})

	t.Run("MapListenerSetToRoutes", func(t *testing.T) {
		makeListenerSet := func() *gatewayv1.ListenerSet {
			return &gatewayv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{
				Namespace: "apps-" + faker.New().Lorem().Word(),
				Name:      "listeners-" + faker.New().Lorem().Word(),
			}}
		}
		listenerSetKind := gatewayv1.Kind("ListenerSet")

		t.Run("queues indexed L7 and TLS routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			listenerSet := makeListenerSet()
			listenerSetKey := client.ObjectKeyFromObject(listenerSet).String()
			httpRoute := makeRandomHTTPRoute()
			grpcRoute := makeRandomGRPCRoute()
			tlsRoute := gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "routes", Name: "tls"}}
			deletedAt := metav1.Now()
			deletedHTTPRoute := makeRandomHTTPRoute(func(route *gatewayv1.HTTPRoute) {
				route.DeletionTimestamp = &deletedAt
			})

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.HTTPRouteList{},
					client.MatchingFields{httpRouteParentGatewayIndexKey: listenerSetKey}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.HTTPRoute{
						httpRoute,
						deletedHTTPRoute,
					}))
					return nil
				})
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.GRPCRouteList{},
					client.MatchingFields{grpcRouteParentGatewayIndexKey: listenerSetKey}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).
						Elem().
						FieldByName("Items").
						Set(reflect.ValueOf([]gatewayv1.GRPCRoute{grpcRoute}))
					return nil
				})
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TLSRouteList{},
					client.MatchingFields{tlsRouteParentGatewayIndexKey: listenerSetKey}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).
						Elem().
						FieldByName("Items").
						Set(reflect.ValueOf([]gatewayv1.TLSRoute{tlsRoute}))
					return nil
				})

			require.ElementsMatch(t,
				[]reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(&httpRoute)}},
				model.MapListenerSetToHTTPRoute(t.Context(), listenerSet),
			)
			require.ElementsMatch(t,
				[]reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(&grpcRoute)}},
				model.MapListenerSetToGRPCRoute(t.Context(), listenerSet),
			)
			require.ElementsMatch(t,
				[]reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(&tlsRoute)}},
				model.MapListenerSetToTLSRoute(t.Context(), listenerSet),
			)
			require.Nil(t, model.MapListenerSetToHTTPRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("queues L4 routes with ListenerSet parent refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			listenerSet := makeListenerSet()
			otherName := gatewayv1.ObjectName("other-" + faker.New().Lorem().Word())
			tcpRoute := gatewayv1.TCPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: listenerSet.Namespace, Name: "tcp"},
				Spec: gatewayv1.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{
						Kind: &listenerSetKind,
						Name: gatewayv1.ObjectName(listenerSet.Name),
					}},
				}},
			}
			udpRoute := gatewayv1.UDPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: listenerSet.Namespace, Name: "udp"},
				Spec: gatewayv1.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{
						Kind: &listenerSetKind,
						Name: gatewayv1.ObjectName(listenerSet.Name),
					}},
				}},
			}
			otherTCPRoute := gatewayv1.TCPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: listenerSet.Namespace, Name: "other"},
				Spec: gatewayv1.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Kind: &listenerSetKind, Name: otherName}},
				}},
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.TCPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.TCPRoute{
						tcpRoute,
						otherTCPRoute,
					}))
					return nil
				})
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.UDPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).
						Elem().
						FieldByName("Items").
						Set(reflect.ValueOf([]gatewayv1.UDPRoute{udpRoute}))
					return nil
				})

			require.ElementsMatch(t,
				[]reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(&tcpRoute)}},
				model.MapListenerSetToTCPRoute(t.Context(), listenerSet),
			)
			require.ElementsMatch(t,
				[]reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(&udpRoute)}},
				model.MapListenerSetToUDPRoute(t.Context(), listenerSet),
			)
			require.Nil(t, model.MapListenerSetToTCPRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("returns nil when route list fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			listenerSet := makeListenerSet()
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.HTTPRouteList{},
					client.MatchingFields{
						httpRouteParentGatewayIndexKey: client.ObjectKeyFromObject(listenerSet).String(),
					}).
				Return(errors.New(faker.New().Lorem().Sentence(10)))

			require.Nil(t, model.MapListenerSetToHTTPRoute(t.Context(), listenerSet))
		})
	})

	t.Run("MapNamespaceToGateway", func(t *testing.T) {
		t.Run("queues Gateways using allowedListeners namespace selectors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			selectedFrom := gatewayv1.NamespacesFromSelector
			sameFrom := gatewayv1.NamespacesFromSame
			wantGateway := *newRandomGateway(func(gateway *gatewayv1.Gateway) {
				gateway.Spec.AllowedListeners = &gatewayv1.AllowedListeners{
					Namespaces: &gatewayv1.ListenerNamespaces{
						From:     &selectedFrom,
						Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "media"}},
					},
				}
			})
			otherGateway := *newRandomGateway(func(gateway *gatewayv1.Gateway) {
				gateway.Spec.AllowedListeners = &gatewayv1.AllowedListeners{
					Namespaces: &gatewayv1.ListenerNamespaces{From: &sameFrom},
				}
			})

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.GatewayList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").
						Set(reflect.ValueOf([]gatewayv1.Gateway{wantGateway, otherGateway}))
					return nil
				})

			result := model.MapNamespaceToGateway(t.Context(), &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"team": "media"}},
			})

			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: client.ObjectKeyFromObject(&wantGateway),
			}}, result)
		})
	})

	t.Run("MapSecretToTLSRoute", func(t *testing.T) {
		t.Run("maps certificate Secret changes to TLSRoutes attached to referencing Gateways", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)
			gateway := gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Namespace: secret.Namespace, Name: "edge"},
			}
			matchingRoute := gatewayv1.TLSRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: secret.Namespace, Name: "rtmps"},
				Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: gatewayv1.ObjectName(gateway.Name)}},
				}},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.GatewayList{}, client.MatchingFields{gatewayCertificateIndexKey: indexKey}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.Gateway{gateway}))
					return nil
				})
			mockK8sClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&gateway), &gatewayv1.Gateway{}).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.Gateway) = gateway
					return nil
				})
			gatewayIndexKey := client.ObjectKeyFromObject(&gateway).String()
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TLSRouteList{},
					client.MatchingFields{tlsRouteParentGatewayIndexKey: gatewayIndexKey}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").
						Set(reflect.ValueOf([]gatewayv1.TLSRoute{matchingRoute}))
					return nil
				})

			require.Equal(t, []reconcile.Request{{
				NamespacedName: client.ObjectKeyFromObject(&matchingRoute),
			}}, model.MapSecretToTLSRoute(t.Context(), &secret))
		})

		t.Run("returns nil when Secret does not map to Gateways", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			require.Nil(t, model.MapSecretToTLSRoute(t.Context(), &corev1.Secret{}))
		})

		t.Run("skips Gateways that cannot be fetched", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)
			gateway := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: secret.Namespace, Name: "edge"}}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.GatewayList{}, client.MatchingFields{gatewayCertificateIndexKey: indexKey}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.Gateway{gateway}))
					return nil
				})
			mockK8sClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&gateway), &gatewayv1.Gateway{}).
				Return(errors.New(faker.New().Lorem().Sentence(10)))

			require.Empty(t, model.MapSecretToTLSRoute(t.Context(), &secret))
		})
	})

	t.Run("L4 route watches", func(t *testing.T) {
		backendPort := gatewayv1.PortNumber(1935)
		crossNamespace := gatewayv1.Namespace("media")

		t.Run("indexes TCPRoute and UDPRoute backend services", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			tcpRoute := &gatewayv1.TCPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
				Spec: gatewayv1.TCPRouteSpec{
					Rules: []gatewayv1.TCPRouteRule{
						{
							BackendRefs: []gatewayv1.BackendRef{
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Name: "rtmp-primary",
										Port: &backendPort,
									},
								},
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Namespace: &crossNamespace,
										Name:      "rtmp-secondary",
										Port:      &backendPort,
									},
								},
							},
						},
					},
				},
			}
			udpRoute := &gatewayv1.UDPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
				Spec: gatewayv1.UDPRouteSpec{
					Rules: []gatewayv1.UDPRouteRule{
						{
							BackendRefs: []gatewayv1.BackendRef{
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Name: "coap-primary",
										Port: &backendPort,
									},
								},
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Namespace: &crossNamespace,
										Name:      "coap-secondary",
										Port:      &backendPort,
									},
								},
							},
						},
					},
				},
			}

			require.ElementsMatch(t,
				[]string{"iot/rtmp-primary", "media/rtmp-secondary"},
				model.indexTCPRouteByBackendService(t.Context(), tcpRoute),
			)
			require.ElementsMatch(t,
				[]string{"iot/coap-primary", "media/coap-secondary"},
				model.indexUDPRouteByBackendService(t.Context(), udpRoute),
			)
			require.Nil(t, model.indexTCPRouteByBackendService(t.Context(), &corev1.Service{}))
			require.Nil(t, model.indexUDPRouteByBackendService(t.Context(), &corev1.Service{}))

			deletionTimestamp := metav1.Now()
			tcpRoute.DeletionTimestamp = &deletionTimestamp
			udpRoute.DeletionTimestamp = &deletionTimestamp
			require.Nil(t, model.indexTCPRouteByBackendService(t.Context(), tcpRoute))
			require.Nil(t, model.indexUDPRouteByBackendService(t.Context(), udpRoute))
		})

		t.Run("maps EndpointSlices to TCPRoute and UDPRoute requests", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt("iot"),
				randomEndpointSliceWithServiceNameOpt("backend"),
			)
			tcpRoutes := []gatewayv1.TCPRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"}},
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "srt"}},
			}
			udpRoutes := []gatewayv1.UDPRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"}},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TCPRouteList{},
					client.MatchingFields{tcpRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tcpRoutes))
					return nil
				})
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.UDPRouteList{},
					client.MatchingFields{udpRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(udpRoutes))
					return nil
				})

			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}},
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "srt"}},
			}, model.MapEndpointSliceToTCPRoute(t.Context(), &endpointSlice))
			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"}},
			}, model.MapEndpointSliceToUDPRoute(t.Context(), &endpointSlice))
			require.Nil(t, model.MapEndpointSliceToTCPRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapEndpointSliceToUDPRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapEndpointSliceToTCPRoute(t.Context(), &discoveryv1.EndpointSlice{}))
			require.Nil(t, model.MapEndpointSliceToUDPRoute(t.Context(), &discoveryv1.EndpointSlice{}))
		})

		t.Run("handles L4 EndpointSlice mapping errors and deleted routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt("iot"),
				randomEndpointSliceWithServiceNameOpt("backend"),
			)
			now := metav1.Now()
			tcpRoutes := []gatewayv1.TCPRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "active"}},
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "deleting", DeletionTimestamp: &now}},
			}
			udpRoutes := []gatewayv1.UDPRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "active"}},
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "deleting", DeletionTimestamp: &now}},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TCPRouteList{},
					client.MatchingFields{tcpRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tcpRoutes))
					return nil
				})
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.UDPRouteList{},
					client.MatchingFields{udpRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(udpRoutes))
					return nil
				})
			require.Equal(
				t,
				[]reconcile.Request{{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "active"}}},
				model.MapEndpointSliceToTCPRoute(t.Context(), &endpointSlice),
			)
			require.Equal(
				t,
				[]reconcile.Request{{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "active"}}},
				model.MapEndpointSliceToUDPRoute(t.Context(), &endpointSlice),
			)

			deps = makeMockDeps(t)
			model = NewWatchesModel(deps)
			mockK8sClient, _ = deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TCPRouteList{},
					client.MatchingFields{tcpRouteBackendServiceIndexKey: "iot/backend"}).
				Return(errors.New("tcp list failed"))
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.UDPRouteList{},
					client.MatchingFields{udpRouteBackendServiceIndexKey: "iot/backend"}).
				Return(errors.New("udp list failed"))
			require.Nil(t, model.MapEndpointSliceToTCPRoute(t.Context(), &endpointSlice))
			require.Nil(t, model.MapEndpointSliceToUDPRoute(t.Context(), &endpointSlice))
		})

		t.Run("maps ReferenceGrants to cross-namespace L4 routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			grant := &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "allow"}}
			tcpRoutes := []gatewayv1.TCPRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
					Spec: gatewayv1.TCPRouteSpec{Rules: []gatewayv1.TCPRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
							Namespace: &crossNamespace,
							Name:      "rtmp",
							Port:      &backendPort,
						}}},
					}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "local"},
					Spec: gatewayv1.TCPRouteSpec{Rules: []gatewayv1.TCPRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "local",
							Port: &backendPort,
						}}},
					}}},
				},
			}
			udpRoutes := []gatewayv1.UDPRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
					Spec: gatewayv1.UDPRouteSpec{Rules: []gatewayv1.UDPRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
							Namespace: &crossNamespace,
							Name:      "coap",
							Port:      &backendPort,
						}}},
					}}},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.TCPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tcpRoutes))
					return nil
				})
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.UDPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(udpRoutes))
					return nil
				})

			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}},
			}, model.MapReferenceGrantToTCPRoute(t.Context(), grant))
			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"}},
			}, model.MapReferenceGrantToUDPRoute(t.Context(), grant))
			require.Nil(t, model.MapReferenceGrantToTCPRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapReferenceGrantToUDPRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("handles ReferenceGrant L4 route list errors", func(t *testing.T) {
			grant := &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "allow"}}
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.TCPRouteList{}).
				Return(errors.New("tcp list failed"))
			require.Nil(t, model.MapReferenceGrantToTCPRoute(t.Context(), grant))

			deps = makeMockDeps(t)
			model = NewWatchesModel(deps)
			mockK8sClient, _ = deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.UDPRouteList{}).
				Return(errors.New("udp list failed"))
			require.Nil(t, model.MapReferenceGrantToUDPRoute(t.Context(), grant))
		})

		t.Run("maps Gateway changes to attached L4 routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}}
			tcpRoutes := []gatewayv1.TCPRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
					Spec: gatewayv1.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
					}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "other"},
					Spec: gatewayv1.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Name: "other"}},
					}},
				},
			}
			udpRoutes := []gatewayv1.UDPRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
					Spec: gatewayv1.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
					}},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.TCPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tcpRoutes))
					return nil
				})
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.UDPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(udpRoutes))
					return nil
				})

			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}},
			}, model.MapGatewayToTCPRoute(t.Context(), gateway))
			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"}},
			}, model.MapGatewayToUDPRoute(t.Context(), gateway))
			require.Nil(t, model.MapGatewayToTCPRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapGatewayToUDPRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("handles Gateway L4 route list errors", func(t *testing.T) {
			gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}}
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.TCPRouteList{}).
				Return(errors.New("tcp list failed"))
			require.Nil(t, model.MapGatewayToTCPRoute(t.Context(), gateway))

			deps = makeMockDeps(t)
			model = NewWatchesModel(deps)
			mockK8sClient, _ = deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.UDPRouteList{}).
				Return(errors.New("udp list failed"))
			require.Nil(t, model.MapGatewayToUDPRoute(t.Context(), gateway))
		})

		t.Run("indexes TLSRoute backend refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			tlsBackendPort := gatewayv1.PortNumber(443)
			tlsCrossNamespace := gatewayv1.Namespace("media")
			tlsRoute := &gatewayv1.TLSRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "tls"},
				Spec: gatewayv1.TLSRouteSpec{
					Rules: []gatewayv1.TLSRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{
							{BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: "tls-primary",
								Port: &tlsBackendPort,
							}},
							{BackendObjectReference: gatewayv1.BackendObjectReference{
								Namespace: &tlsCrossNamespace,
								Name:      "tls-secondary",
								Port:      &tlsBackendPort,
							}},
						},
					}},
				},
			}

			require.ElementsMatch(t,
				[]string{"iot/tls-primary", "media/tls-secondary"},
				model.indexTLSRouteByBackendService(t.Context(), tlsRoute),
			)
			require.Nil(t, model.indexTLSRouteByBackendService(t.Context(), &corev1.Service{}))

			deletionTimestamp := metav1.Now()
			tlsRoute.DeletionTimestamp = &deletionTimestamp
			require.Nil(t, model.indexTLSRouteByBackendService(t.Context(), tlsRoute))
		})

		t.Run("maps EndpointSlices to TLSRoute requests", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt("iot"),
				randomEndpointSliceWithServiceNameOpt("backend"),
			)
			now := metav1.Now()
			tlsRoutes := []gatewayv1.TLSRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "active"}},
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "deleting", DeletionTimestamp: &now}},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TLSRouteList{},
					client.MatchingFields{tlsRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tlsRoutes))
					return nil
				})

			require.Equal(
				t,
				[]reconcile.Request{{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "active"}}},
				model.MapEndpointSliceToTLSRoute(t.Context(), &endpointSlice),
			)
			require.Nil(t, model.MapEndpointSliceToTLSRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapEndpointSliceToTLSRoute(t.Context(), &discoveryv1.EndpointSlice{}))
		})

		t.Run("maps ReferenceGrants to cross-namespace TLSRoutes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			tlsBackendPort := gatewayv1.PortNumber(443)
			tlsCrossNamespace := gatewayv1.Namespace("media")
			grant := &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "allow"}}
			tlsRoutes := []gatewayv1.TLSRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "tls"},
					Spec: gatewayv1.TLSRouteSpec{Rules: []gatewayv1.TLSRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
							Namespace: &tlsCrossNamespace,
							Name:      "tls",
							Port:      &tlsBackendPort,
						}}},
					}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "local"},
					Spec: gatewayv1.TLSRouteSpec{Rules: []gatewayv1.TLSRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "local",
							Port: &tlsBackendPort,
						}}},
					}}},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TLSRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tlsRoutes))
					return nil
				})

			require.Equal(t,
				[]reconcile.Request{{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "tls"}}},
				model.MapReferenceGrantToTLSRoute(t.Context(), grant),
			)
			require.Nil(t, model.MapReferenceGrantToTLSRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("maps Gateway changes to referencing TLSRoutes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}}
			tlsRoutes := []gatewayv1.TLSRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "matched"},
					Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
					}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "other"},
					Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Name: "other"}},
					}},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TLSRouteList{},
					client.MatchingFields{tlsRouteParentGatewayIndexKey: "iot/edge"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tlsRoutes[:1]))
					return nil
				})

			require.Equal(t,
				[]reconcile.Request{{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "matched"}}},
				model.MapGatewayToTLSRoute(t.Context(), gateway),
			)
			require.Nil(t, model.MapGatewayToTLSRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("returns nil when indexed TLSRoute list fails for Gateway changes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TLSRouteList{},
					client.MatchingFields{tlsRouteParentGatewayIndexKey: "iot/edge"}).
				Return(errors.New(faker.New().Lorem().Sentence(10)))

			require.Nil(t, model.MapGatewayToTLSRoute(t.Context(), gateway))
		})

		t.Run("maps Secret changes to TLSRoutes through referencing Gateways", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "cert"},
				Type:       corev1.SecretTypeTLS,
				Data: map[string][]byte{
					corev1.TLSCertKey:       []byte("cert"),
					corev1.TLSPrivateKeyKey: []byte("key"),
				},
			}
			gateway := gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   "iot",
					Name:        "edge",
					Annotations: map[string]string{ControllerClassName: "true"},
				},
			}
			tlsRoutes := []gatewayv1.TLSRoute{{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "tls"},
				Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				}},
			}}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.GatewayList{},
					client.MatchingFields{gatewayCertificateIndexKey: "iot/cert"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.Gateway{gateway}))
					return nil
				})
			mockK8sClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, &gatewayv1.Gateway{}).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.Gateway) = gateway
					return nil
				})
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.TLSRouteList{},
					client.MatchingFields{tlsRouteParentGatewayIndexKey: "iot/edge"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tlsRoutes))
					return nil
				})

			require.Equal(t,
				[]reconcile.Request{{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "tls"}}},
				model.MapSecretToTLSRoute(t.Context(), secret),
			)
			require.Nil(t, model.MapSecretToTLSRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("maps GatewayConfig changes to referencing Gateways", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			config := &configtypes.GatewayConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge-config"},
			}
			now := metav1.Now()
			gateways := []gatewayv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:   "iot",
						Name:        "edge",
						Annotations: map[string]string{ControllerClassName: "true"},
					},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "edge-config",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "iot",
						Name:      "edge-l4",
						Annotations: map[string]string{
							NetworkLoadBalancerControllerClassName: "true",
						},
					},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "edge-config",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:   "iot",
						Name:        "unsupported",
						Annotations: map[string]string{"example.com/controller": "true"},
					},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "edge-config",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "other"},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "other-config",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "missing-ref"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:         "iot",
						Name:              "deleting",
						DeletionTimestamp: &now,
					},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "edge-config",
							},
						},
					},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.GatewayList{}, client.InNamespace("iot")).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(gateways))
					return nil
				})

			require.Equal(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "edge"}},
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "edge-l4"}},
			}, model.MapGatewayConfigToGateway(t.Context(), config))
			require.Nil(t, model.MapGatewayConfigToGateway(t.Context(), &corev1.Service{}))
		})

		t.Run("handles GatewayConfig Gateway list errors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			config := &configtypes.GatewayConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge-config"},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.GatewayList{}, client.InNamespace("iot")).
				Return(errors.New("gateway list failed"))

			require.Nil(t, model.MapGatewayConfigToGateway(t.Context(), config))
		})
	})

	t.Run("BackendTLSPolicy watches", func(t *testing.T) {
		namespace := "iot"
		serviceName := "backend"
		serviceKey := "iot/backend"
		httpRoute := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "http"}}
		deletionTime := metav1.Now()
		deletingHTTPRoute := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              "http-deleting",
			DeletionTimestamp: &deletionTime,
			Finalizers:        []string{"test-finalizer"},
		}}
		grpcRoute := &gatewayv1.GRPCRoute{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "grpc"}}
		tlsRoute := &gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "tls"}}
		policy := &gatewayv1.BackendTLSPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "backend-tls"},
			Spec: gatewayv1.BackendTLSPolicySpec{
				TargetRefs: []gatewayv1.LocalPolicyTargetReferenceWithSectionName{{
					LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
						Group: "",
						Kind:  "Service",
						Name:  gatewayv1.ObjectName(serviceName),
					},
				}},
				Validation: gatewayv1.BackendTLSPolicyValidation{
					CACertificateRefs: []gatewayv1.LocalObjectReference{{
						Group: "",
						Kind:  "ConfigMap",
						Name:  "ca",
					}},
				},
			},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(httpRoute, deletingHTTPRoute, grpcRoute, tlsRoute, policy).
			WithIndex(&gatewayv1.HTTPRoute{}, httpRouteBackendServiceIndexKey, func(_ client.Object) []string {
				return []string{serviceKey}
			}).
			WithIndex(&gatewayv1.GRPCRoute{}, grpcRouteBackendServiceIndexKey, func(_ client.Object) []string {
				return []string{serviceKey}
			}).
			WithIndex(&gatewayv1.TLSRoute{}, tlsRouteBackendServiceIndexKey, func(_ client.Object) []string {
				return []string{serviceKey}
			}).
			Build()
		model := NewWatchesModel(WatchesModelDeps{
			K8sClient: k8sClient,
			Logger:    diag.RootTestLogger(),
		})

		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "http"},
		}}, model.MapBackendTLSPolicyToHTTPRoute(t.Context(), policy))
		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "grpc"},
		}}, model.MapBackendTLSPolicyToGRPCRoute(t.Context(), policy))
		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "tls"},
		}}, model.MapBackendTLSPolicyToTLSRoute(t.Context(), policy))

		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"}}
		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "http"},
		}}, model.MapConfigMapToHTTPRoute(t.Context(), configMap))
		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "grpc"},
		}}, model.MapConfigMapToGRPCRoute(t.Context(), configMap))
		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "tls"},
		}}, model.MapConfigMapToTLSRoute(t.Context(), configMap))

		service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: serviceName}}
		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "http"},
		}}, model.MapServiceToHTTPRoute(t.Context(), service))
		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "grpc"},
		}}, model.MapServiceToGRPCRoute(t.Context(), service))
		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: apitypes.NamespacedName{Namespace: namespace, Name: "tls"},
		}}, model.MapServiceToTLSRoute(t.Context(), service))

		require.Nil(t, model.MapBackendTLSPolicyToHTTPRoute(t.Context(), &corev1.Service{}))
		require.Nil(t, model.MapConfigMapToHTTPRoute(t.Context(), &corev1.Service{}))
		require.Nil(t, model.MapServiceToHTTPRoute(t.Context(), &corev1.ConfigMap{}))
		require.False(t, backendTLSPolicyReferencesConfigMap(*policy, "other"))
		require.Nil(t, objectListItems(&corev1.ServiceList{}))
	})

	t.Run("BackendTLSPolicy watch error and skip paths", func(t *testing.T) {
		deps := makeMockDeps(t)
		model := NewWatchesModel(deps)
		mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		policy := &gatewayv1.BackendTLSPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "backend-tls"},
			Spec: gatewayv1.BackendTLSPolicySpec{
				TargetRefs: []gatewayv1.LocalPolicyTargetReferenceWithSectionName{
					{LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
						Group: "apps",
						Kind:  "Deployment",
						Name:  "ignored",
					}},
					{LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
						Group: "",
						Kind:  "Service",
						Name:  "backend",
					}},
				},
				Validation: gatewayv1.BackendTLSPolicyValidation{
					CACertificateRefs: []gatewayv1.LocalObjectReference{{
						Group: "",
						Kind:  "ConfigMap",
						Name:  "ca",
					}},
				},
			},
		}
		mockK8sClient.EXPECT().
			List(t.Context(), &gatewayv1.HTTPRouteList{}, client.MatchingFields{
				httpRouteBackendServiceIndexKey: "iot/backend",
			}).
			Return(errors.New("route list failed"))

		require.Nil(t, model.MapBackendTLSPolicyToHTTPRoute(t.Context(), policy))

		mockK8sClient.EXPECT().
			List(t.Context(), &gatewayv1.BackendTLSPolicyList{}, client.InNamespace("iot")).
			Return(errors.New("policy list failed"))

		require.Nil(t, model.MapConfigMapToHTTPRoute(
			t.Context(),
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "ca"}},
		))

		mockK8sClient.EXPECT().
			List(t.Context(), &gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: "iot/backend"}).
			Return(errors.New("route list failed"))
		require.Nil(t, model.MapServiceToHTTPRoute(
			t.Context(),
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "backend"}},
		))
	})
}
