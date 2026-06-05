package config

import (
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		NodeName:       "irods-1",
		ZoneName:       "weftZone",
		DC:             "dc1",
		EtcdEndpoints:  []string{"http://etcd:2379"},
		AdminPassword:  "x",
		ICATDBHost:     "pg.weft",
		ICATDBPort:     5432,
		ICATDBUser:     "irods",
		ICATDBPassword: "y",
		APIAddr:        ":8009",
		MetricsAddr:    ":9102",
	}
}

func TestValidate_AcceptsHappyPath(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
}

func TestValidate_RejectsMissingFields(t *testing.T) {
	cases := map[string]func(*Config){
		"node-name":      func(c *Config) { c.NodeName = "" },
		"zone-name":      func(c *Config) { c.ZoneName = "" },
		"dc":             func(c *Config) { c.DC = "" },
		"etcd":           func(c *Config) { c.EtcdEndpoints = nil },
		"admin-password": func(c *Config) { c.AdminPassword = "" },
		"icat-db-host":   func(c *Config) { c.ICATDBHost = "" },
		"icat-db-port":   func(c *Config) { c.ICATDBPort = 0 },
		"icat-db-user":   func(c *Config) { c.ICATDBUser = "" },
		"icat-db-pwd":    func(c *Config) { c.ICATDBPassword = "" },
		"api-addr":       func(c *Config) { c.APIAddr = "" },
		"metrics-addr":   func(c *Config) { c.MetricsAddr = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected error for missing %s", name)
			}
			// Accept either "must" or "required" — both phrasings
			// explain the constraint clearly to operators.
			msg := err.Error()
			if !strings.Contains(msg, "must") && !strings.Contains(msg, "required") {
				t.Errorf("error should explain the requirement, got: %v", err)
			}
		})
	}
}
