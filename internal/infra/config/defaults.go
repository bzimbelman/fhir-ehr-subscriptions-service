// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package config

// defaults returns the built-in defaults layer (lowest precedence). Per LLD
// §4.4 the defaults follow the architecture's example YAML — exponential
// backoff (10s, 30s, 2m, 10m, 1h), max 8 attempts, 30-day event retention,
// 7-year audit retention, etc.
//
// An operator who supplies only the hard-required fields gets a complete
// effective config from defaults plus those fields.
func defaults() map[string]interface{} {
	return map[string]interface{}{
		"deployment": map[string]interface{}{
			"environment": "production",
			"log_level":   "info",
			"log_format":  "json",
		},
		"server": map[string]interface{}{
			"http": map[string]interface{}{
				"bind":       "0.0.0.0:8443",
				"probe_bind": nil,
			},
			"websocket": map[string]interface{}{
				"enabled":         true,
				"max_connections": 10000,
			},
		},
		"lifecycle": map[string]interface{}{
			"shutdown_grace_period":  "30s",
			"postgres_probe_timeout": "2s",
		},
		"storage": map[string]interface{}{
			"postgres": map[string]interface{}{
				"pool_size":         16,
				"statement_timeout": "30s",
			},
			"retention": map[string]interface{}{
				"hl7_message_queue": "7d",
				"resource_changes":  "30d",
				"ehr_events":        "30d",
				"deliveries":        "90d",
				"dead_letters":      "180d",
				"audit_log":         "7y",
			},
		},
		"auth": map[string]interface{}{
			"schemes": []interface{}{"smart-backend-services"},
			"jwks": map[string]interface{}{
				"cache_ttl": "1h",
			},
		},
		"topics": map[string]interface{}{
			"value_sets_dir": "/etc/fhir-subs/value-sets",
		},
		"delivery": map[string]interface{}{
			"default_max_count": 1,
			"max_batch_wait":    "30s",
			"retry": map[string]interface{}{
				"max_attempts": 8,
				"backoff": map[string]interface{}{
					"kind":    "exponential",
					"initial": "10s",
					"max":     "1h",
					"jitter":  0.2,
				},
			},
			"heartbeat": map[string]interface{}{
				"default_period": "5m",
				"min_period":     "1m",
				"max_period":     "1h",
			},
		},
		"observability": map[string]interface{}{
			"metrics": map[string]interface{}{
				"bind": "0.0.0.0:9090",
			},
			"tracing": map[string]interface{}{
				"sample_rate": 0.1,
			},
			"audit_log": map[string]interface{}{
				"sink": "stdout",
			},
		},
	}
}
