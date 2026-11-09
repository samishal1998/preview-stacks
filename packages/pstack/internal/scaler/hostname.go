package scaler

import (
	"errors"
	"regexp"
	"strings"
)

var topHostname = regexp.MustCompile(`(?m)^hostname:`)

// WithHostname adds `hostname: <name>` and `preserve_hostname: false` as top-level keys right
// after the `#cloud-config` line. Providers disagree on naming the machine after the server, and
// swarm matches nodes by hostname.
func WithHostname(cloudConfig, name string) (string, error) {
	first, rest, _ := strings.Cut(cloudConfig, "\n")
	if first != "#cloud-config" {
		return "", errors.New("join config does not start with #cloud-config")
	}
	if topHostname.MatchString(cloudConfig) {
		return "", errors.New("join config already sets hostname")
	}
	return first + "\nhostname: " + name + "\npreserve_hostname: false\n" + rest, nil
}
