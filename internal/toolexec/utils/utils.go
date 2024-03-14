// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

package utils

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"
)

// ExitIfError calls os.Exit(1) if err is not nil
func ExitIfError(err error) {
	if err == nil {
		return
	}
	log.Fatalln(err)
}

// GoBuild builds in provided dir and returns the work directory's true path
// The underlying go build always:
// - preserves the go work directory (-work)
// - forces recompilation of all dependencies (-a)
func GoBuild(dir string, args ...string) (string, error) {
	args = append([]string{"build", "-work", "-a"}, args...)
	return execCommandWithCache(dir, args...)
}

func execCommandWithCache(dir string, args ...string) (string, error) {
	// Try to get if the build was already made previously, and we have and existing build temp folder available
	key := strings.Join(args, "|")
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	fileKey := path.Join(dir, fmt.Sprintf(".%s.build", hash))
	if s, error := os.Stat(fileKey); error == nil && !s.IsDir() {
		log.Printf("Opening '%s' file.\n", fileKey)
		if byteContent, error := os.ReadFile(fileKey); error == nil {
			builderTmpFolder := strings.TrimSpace(strings.Trim(string(byteContent), "\n"))
			if dirInfo, error := os.Stat(builderTmpFolder); error == nil && dirInfo.IsDir() {
				log.Printf("Using '%s' cached folder.\n", builderTmpFolder)
				return builderTmpFolder, nil
			}
		}
	}

	cmd := exec.Command("go", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	log.Println(string(out))
	if err != nil {
		return "", err
	}

	// Extract work dir from output
	wDir := strings.Split(string(out), "=")[1]
	wDir = strings.TrimSuffix(wDir, "\n")

	// Writing build cache
	if os.WriteFile(fileKey, []byte(wDir), 0666) == nil {
		log.Printf("Writing '%s' with the build temp folder.\n", fileKey)
	}

	return wDir, nil
}
