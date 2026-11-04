//go:build !linux

package cli

import "errors"

func readSELinuxContext(string) (string, error) {
	return "", errors.New("SELinux context inspection requires your Linux host and a mounted guest file")
}
