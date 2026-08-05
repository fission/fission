// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// SumMetricLines sums every Prometheus exposition line for name, labeled or
// not (both "foo 3" and `foo{a="b"} 3` match), skipping comment lines.
// Returns 0 if the metric is absent.
//
// Promoted from the three local copies (parseGaugeValue in dynamic_tenant_test,
// sumMetricLines in alias_routing_test, parseMetric in memory_soak_test) so
// tests share one correct implementation — sum-all semantics handle labeled
// metrics correctly, unlike the first-match variants that silently under-report.
func SumMetricLines(raw []byte, name string) float64 {
	var total float64
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		rest, ok := strings.CutPrefix(line, name)
		if !ok {
			continue
		}
		if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '{') {
			continue // matched a longer metric name sharing this prefix
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		total += v
	}
	return total
}

// MetricHasLabels reports whether the Prometheus exposition contains a sample
// for name with every requested label. Extra labels on the sample are allowed.
func MetricHasLabels(raw []byte, name string, want map[string]string) (bool, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	family := families[name]
	if family == nil {
		return false, nil
	}
	for _, metric := range family.GetMetric() {
		labels := make(map[string]string, len(metric.GetLabel()))
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		matched := true
		for label, value := range want {
			if labels[label] != value {
				matched = false
				break
			}
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
