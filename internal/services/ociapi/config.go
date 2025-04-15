package ociapi

import (
	"github.com/oracle/oci-go-sdk/v65/common"
)

func newConfigProvider() (common.ConfigurationProvider, error) {
	// TODO: This needs more advanced setup and support in cluster config
	configProvider := common.DefaultConfigProvider()
	return configProvider, nil
}
