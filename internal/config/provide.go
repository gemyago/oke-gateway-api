package config

import (
	"fmt"

	"github.com/gemyago/oke-gateway-api/internal/di"
	"github.com/spf13/viper"
	"go.uber.org/dig"
)

type configValueProvider struct {
	cfg        *viper.Viper
	configPath string
	diPath     string
}

func provideConfigValue(cfg *viper.Viper, path string) configValueProvider {
	if !cfg.IsSet(path) {
		panic(fmt.Errorf("config key not found: %s", path))
	}
	return configValueProvider{cfg, path, "config." + path}
}

func (p configValueProvider) asInt() di.ConstructorWithOpts {
	return di.ProvideValue(p.cfg.GetInt(p.configPath), dig.Name(p.diPath))
}

func (p configValueProvider) asInt32() di.ConstructorWithOpts {
	return di.ProvideValue(p.cfg.GetInt32(p.configPath), dig.Name(p.diPath))
}

func (p configValueProvider) asString() di.ConstructorWithOpts {
	return di.ProvideValue(p.cfg.GetString(p.configPath), dig.Name(p.diPath))
}

func (p configValueProvider) asBool() di.ConstructorWithOpts {
	return di.ProvideValue(p.cfg.GetBool(p.configPath), dig.Name(p.diPath))
}

func (p configValueProvider) asDuration() di.ConstructorWithOpts {
	return di.ProvideValue(p.cfg.GetDuration(p.configPath), dig.Name(p.diPath))
}

func Provide(container *dig.Container, cfg *viper.Viper) error {
	return di.ProvideAll(container,
		provideConfigValue(cfg, "gracefulShutdownTimeout").asDuration(),

		// http server config
		provideConfigValue(cfg, "httpServer.host").asString(),
		provideConfigValue(cfg, "httpServer.port").asInt(),
		provideConfigValue(cfg, "httpServer.idleTimeout").asDuration(),
		provideConfigValue(cfg, "httpServer.readHeaderTimeout").asDuration(),
		provideConfigValue(cfg, "httpServer.readTimeout").asDuration(),
		provideConfigValue(cfg, "httpServer.writeTimeout").asDuration(),
		provideConfigValue(cfg, "httpServer.mode").asString(),
		provideConfigValue(cfg, "httpServer.accessLogsLevel").asString(),

		// k8sapi config
		provideConfigValue(cfg, "k8sapi.noop").asBool(),
		provideConfigValue(cfg, "k8sapi.inCluster").asBool(),
		// ociapi config
		provideConfigValue(cfg, "ociapi.noop").asBool(),

		// features config
		provideConfigValue(cfg, "features.reconcileGatewayClass").asBool(),
		provideConfigValue(cfg, "features.reconcileGateway").asBool(),
		provideConfigValue(cfg, "features.reconcileHTTPRoute").asBool(),
	)
}
