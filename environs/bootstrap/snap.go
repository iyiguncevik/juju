// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package bootstrap

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/juju/errors"
)

var runSnapInfoCommand = func(packageName string) (string, error) {
	cmd := exec.Command("snap", "info", packageName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", errors.Annotatef(err, "snap info failed: %s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func resolveSnapChannelVersion(channel string) (string, error) {
	out, err := runSnapInfoCommand(ControllerSnapPackageName)
	if err != nil {
		return "", errors.Trace(err)
	}

	pattern := fmt.Sprintf(`(?m)^\s*%s:\s*([^\s]+)`, regexp.QuoteMeta(channel))
	matches := regexp.MustCompile(pattern).FindStringSubmatch(out)
	if len(matches) < 2 {
		return "", errors.Errorf("unable to resolve controller snap version in channel %q", channel)
	}

	return matches[1], nil
}
